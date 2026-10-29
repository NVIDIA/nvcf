/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.icms.service.gating;

import static com.nvidia.icms.inbound.rest.converters.ErrorDataConverter.toUnifiedErrorData;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_GPU;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE;
import static com.nvidia.icms.util.InstanceServiceUtil.isListEmptyOrNull;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;

import com.nvidia.icms.configuration.bean.CapacityGatingProperties;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.GpuResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups.InstanceTypeResponse;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.GpuResponseSchema;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse.InstanceTypeResponseSchema;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.uec.IcmsUnifiedError;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.HashSet;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Service;

/**
 * Applies {@code icms.capacity-gating} to everything a gated NCA ID can see or allocate.
 *
 * <p>Clusters and GPUs are filtered in a single pass over each response so that the listing APIs
 * and the scheduling path reach the same decision by construction rather than by keeping two
 * sequential filters in agreement.
 */
@Service
@AllArgsConstructor
@Slf4j
public class CapacityGatingService {

    private final CapacityGatingProperties capacityGatingProperties;
    private final UnifiedErrorReporter unifiedErrorReporter;
    private final ClusterRepository clusterRepository;

    /**
     * Removes GPUs, instance types and clusters withheld from this NCA ID from an account-info
     * response. A region left with no clusters is dropped, as is an instance type left with no
     * regions and a GPU left with no instance types.
     */
    public void removeGatedFromAccountInfo(@NotNull InstanceTypeAvailabilityResponse result,
                                           @NotNull String ncaId) {
        if (!capacityGatingProperties.hasCapacityGating(ncaId) || result.getGpus() == null) {
            return;
        }

        boolean filterClusters = restrictsClusters(ncaId);
        Set<String> ownedClusterIds = ownedClusterIds(ncaId);

        result.getGpus().removeIf(gpu -> {
            if (!capacityGatingProperties.isGpuAllowedForNca(ncaId, gpu.getGpuName())) {
                return true;
            }

            if (gpu.getInstanceTypes() != null) {
                gpu.getInstanceTypes().removeIf(instanceType ->
                        !capacityGatingProperties.isInstanceTypeAllowedForNca(
                                ncaId, gpu.getGpuName(), instanceType.getInstanceName())
                                || (filterClusters
                                && isInstanceTypeEmptyAfterGating(instanceType, ncaId,
                                                                  ownedClusterIds)));
            }

            boolean noInstanceTypesLeft = isSetEmptyOrNull(gpu.getInstanceTypes());
            if (noInstanceTypesLeft) {
                log.info("NcaId {}: GPU {} removed from account info response because capacity "
                                 + "gating left it with nothing available", ncaId,
                         gpu.getGpuName());
            }
            return noInstanceTypesLeft;
        });

    }

    /**
     * Removes GPUs, instance types and clusters withheld from this NCA ID from the cluster-group
     * response, dropping groups left with no GPUs or no clusters. Applies to BYOC groups too,
     * which is what keeps a gated org out of publicly accessible BYOC clusters.
     */
    public void removeGatedClusterGroups(@NotNull Set<ClusterGroups> clusterGroupsSet,
                                         @NotNull String ncaId) {
        if (!capacityGatingProperties.hasCapacityGating(ncaId)) {
            return;
        }

        Set<String> ownedClusterIds = ownedClusterIds(ncaId);

        clusterGroupsSet.removeIf(clusterGroup -> {
            clusterGroup.setGpus(retainAllowedGpus(clusterGroup.getGpus(), ncaId));

            if (isSetEmptyOrNull(clusterGroup.getGpus())) {
                log.info("NcaId {}: clusterGroup {} removed from response because capacity "
                                 + "gating left it with no GPUs", ncaId, clusterGroup.getName());
                return true;
            }

            // A group that never listed clusters is left alone, so this only ever removes a
            // group whose entire cluster list was gated out.
            if (isSetEmptyOrNull(clusterGroup.getClusters())) {
                return false;
            }

            clusterGroup.getClusters().removeIf(
                    cluster -> !isClusterAllowed(ncaId, cluster.getId(), ownedClusterIds));

            boolean noClustersLeft = isSetEmptyOrNull(clusterGroup.getClusters());
            if (noClustersLeft) {
                log.info("NcaId {}: clusterGroup {} removed from response because capacity "
                                 + "gating left it with no clusters", ncaId,
                         clusterGroup.getName());
            }
            return noClustersLeft;
        });

    }

    /**
     * Removes GPUs, instance types and clusters withheld from this NCA ID from the authorized
     * cluster listing. Clusters the NCA ID owns are left untouched unless the account is denied
     * outright: gating controls what an org may borrow from others, not whether it can still see
     * and manage the clusters it registered itself.
     */
    public void removeGatedAuthorizedClusters(@NotNull List<GetClusterResponse> clusters,
                                              @NotNull String ncaId) {
        if (!capacityGatingProperties.hasCapacityGating(ncaId)) {
            return;
        }

        boolean orgDenied = capacityGatingProperties.isOrgDenied(ncaId);

        // The response carries the owning NCA ID per cluster, so ownership is settled inline and
        // this path never needs the owned-cluster lookup.
        clusters.removeIf(cluster -> {
            if (!orgDenied && Objects.equals(cluster.getNcaId(), ncaId)) {
                return false;
            }

            if (!capacityGatingProperties.isClusterAllowedForNca(ncaId,
                                                                    cluster.getClusterId())) {
                log.trace("NcaId {}: cluster {} removed from authorized cluster listing by "
                                  + "capacity gating", ncaId, cluster.getClusterId());
                return true;
            }

            // Order matters: the capacity map is pruned against the GPUs that actually survived,
            // so it has to run after the GPU set has been rebuilt.
            cluster.setGpus(retainAllowedClusterGpus(cluster.getGpus(), ncaId));
            removeGatedGpuUsage(cluster);

            boolean noGpusLeft = isSetEmptyOrNull(cluster.getGpus());
            if (noGpusLeft) {
                log.trace("NcaId {}: cluster {} removed from authorized cluster listing because "
                                  + "capacity gating left it with no GPUs", ncaId,
                          cluster.getClusterId());
            }
            return noGpusLeft;
        });

    }

    /**
     * Removes GPUs, instance types and clusters withheld from this NCA ID from a GPU usage
     * response. A region left with no clusters is dropped, as is an instance left with no regions
     * and a GPU left with no instances.
     */
    public void removeGatedFromGpuUsage(@Nullable List<GpuUsageResponse.Gpu> gpus,
                                        @NotNull String ncaId) {
        if (!capacityGatingProperties.hasCapacityGating(ncaId) || gpus == null) {
            return;
        }

        boolean filterClusters = restrictsClusters(ncaId);
        Set<String> ownedClusterIds = ownedClusterIds(ncaId);

        gpus.removeIf(gpu -> {
            if (!capacityGatingProperties.isGpuAllowedForNca(ncaId, gpu.getGpuName())) {
                return true;
            }

            if (gpu.getInstances() != null) {
                gpu.getInstances().removeIf(instance ->
                        !capacityGatingProperties.isInstanceTypeAllowedForNca(
                                ncaId, gpu.getGpuName(), instance.getInstanceName())
                                || (filterClusters
                                && isUsageInstanceEmptyAfterGating(instance, ncaId,
                                                                   ownedClusterIds)));
            }

            boolean noInstancesLeft = isListEmptyOrNull(gpu.getInstances());
            if (noInstancesLeft) {
                log.info("NcaId {}: GPU {} removed from gpu usage response because capacity "
                                 + "gating left it with nothing available", ncaId,
                         gpu.getGpuName());
            }
            return noInstancesLeft;
        });

    }

    /**
     * Removes destinations withheld from this NCA ID from an instance request. Unlike the read
     * paths, an empty result here is an error: the caller asked for capacity it may not use, so
     * this throws rather than silently launching nothing.
     */
    public void removeGatedDestinations(@NotNull Set<RequestInstanceDestination> destinations,
                                        @NotNull SpotInstanceRequestSchema instanceRequest) {
        String ncaId = instanceRequest.getNcaId();
        if (!capacityGatingProperties.hasCapacityGating(ncaId)) {
            return;
        }

        if (capacityGatingProperties.isOrgDenied(ncaId)) {
            log.info("InstanceRequest: {}: NcaId {}: account is denied all capacity, removing all "
                             + "{} destinations", instanceRequest.getLoggingId(), ncaId,
                     destinations.size());
            destinations.clear();
            throwGatingError(NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS, instanceRequest);
            return;
        }

        int destinationCountBeforeGating = destinations.size();
        Set<RequestInstanceDestination> gatedOut = new HashSet<>();
        boolean anyGpuAllowed = false;
        boolean anyInstanceTypeAllowed = false;

        for (RequestInstanceDestination destination : destinations) {
            if (!capacityGatingProperties.isGpuAllowedForNca(ncaId, destination.getGpuName())) {
                gatedOut.add(destination);
                continue;
            }
            anyGpuAllowed = true;

            String instanceTypeName = destination.getInstanceType() != null
                    ? destination.getInstanceType().getName() : null;
            if (!capacityGatingProperties.isInstanceTypeAllowedForNca(ncaId,
                                                                         destination.getGpuName(),
                                                                         instanceTypeName)) {
                gatedOut.add(destination);
                continue;
            }
            anyInstanceTypeAllowed = true;

            // Destinations carry the owning NCA ID, so ownership is settled inline here too.
            if (!Objects.equals(destination.getNcaId(), ncaId)
                    && !capacityGatingProperties.isClusterAllowedForNca(
                            ncaId, destination.getClusterId())) {
                gatedOut.add(destination);
            }
        }

        destinations.removeAll(gatedOut);

        log.info("InstanceRequest: {}: NcaId {}: capacity gating kept {} of {} destinations for "
                         + "GPU {} and instance type {}, removed clusters {}",
                 instanceRequest.getLoggingId(), ncaId, destinations.size(),
                 destinationCountBeforeGating, instanceRequest.getGpu(),
                 instanceRequest.getInstanceType(),
                 gatedOut.stream().map(RequestInstanceDestination::getClusterId).distinct()
                         .toList());

        if (!destinations.isEmpty()) {
            return;
        }

        // Report the narrowest rule that actually withheld everything: no GPU survived means the
        // org has no access to the GPU at all, an instance type surviving none means the GPU was
        // allowed and the type was not, and anything beyond that was withheld by cluster.
        if (!anyGpuAllowed) {
            throwGatingError(NVCF_CUSTOMER_NO_ACCESS_TO_GPU, instanceRequest);
        } else if (!anyInstanceTypeAllowed) {
            throwGatingError(NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE, instanceRequest);
        } else {
            throwGatingError(NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS, instanceRequest);
        }
    }

    /**
     * Rebuilds the GPU set keeping only the allowed GPUs and, within them, the allowed instance
     * types.
     */
    private Set<GpuResponse> retainAllowedGpus(Set<GpuResponse> gpus, String ncaId) {
        if (isSetEmptyOrNull(gpus)) {
            return new HashSet<>();
        }

        Set<GpuResponse> allowedGpus = new HashSet<>();
        for (GpuResponse gpu : gpus) {
            if (!capacityGatingProperties.isGpuAllowedForNca(ncaId, gpu.getName())) {
                continue;
            }

            Set<InstanceTypeResponse> allowedInstanceTypes = Optional
                    .ofNullable(gpu.getInstanceTypes())
                    .orElseGet(HashSet::new)
                    .stream()
                    .filter(instanceType -> capacityGatingProperties.isInstanceTypeAllowedForNca(
                            ncaId, gpu.getName(), instanceType.getName()))
                    .collect(Collectors.toSet());

            if (allowedInstanceTypes.isEmpty()) {
                continue;
            }

            allowedGpus.add(GpuResponse.builder()
                                    .name(gpu.getName())
                                    .instanceTypes(allowedInstanceTypes)
                                    .build());
        }
        return allowedGpus;
    }

    /**
     * Rebuilds the GPU set of an authorized cluster keeping only the allowed GPUs and, within
     * them, the allowed instance types.
     */
    private Set<GpuResponseSchema> retainAllowedClusterGpus(Set<GpuResponseSchema> gpus,
                                                            String ncaId) {
        if (isSetEmptyOrNull(gpus)) {
            return new HashSet<>();
        }

        Set<GpuResponseSchema> allowedGpus = new HashSet<>();
        for (GpuResponseSchema gpu : gpus) {
            if (!capacityGatingProperties.isGpuAllowedForNca(ncaId, gpu.getName())) {
                continue;
            }

            Set<InstanceTypeResponseSchema> allowedInstanceTypes = Optional
                    .ofNullable(gpu.getInstanceTypes())
                    .orElseGet(HashSet::new)
                    .stream()
                    .filter(instanceType -> capacityGatingProperties.isInstanceTypeAllowedForNca(
                            ncaId, gpu.getName(), instanceType.getName()))
                    .collect(Collectors.toSet());

            if (allowedInstanceTypes.isEmpty()) {
                continue;
            }

            allowedGpus.add(GpuResponseSchema.builder()
                                    .name(gpu.getName())
                                    .capacity(gpu.getCapacity())
                                    .instanceTypes(allowedInstanceTypes)
                                    .build());
        }
        return allowedGpus;
    }

    /**
     * Drops per-GPU capacity entries for GPUs that did not survive gating, so a withheld GPU name
     * is not disclosed through the cluster capacity map. Keyed off the already filtered GPU set
     * rather than off the allow list, because a GPU the NCA ID is allowed in general is still
     * dropped from this cluster when none of the instance types it offers here are permitted.
     *
     * <p>Must be called after the cluster GPU set has been rebuilt.
     */
    private void removeGatedGpuUsage(GetClusterResponse cluster) {
        if (cluster.getGpuUsage() == null) {
            return;
        }

        Set<String> survivingGpuNames = Optional.ofNullable(cluster.getGpus())
                .orElseGet(HashSet::new)
                .stream()
                .map(GpuResponseSchema::getName)
                .collect(Collectors.toSet());

        cluster.getGpuUsage().keySet().removeIf(gpuName -> !survivingGpuNames.contains(gpuName));
    }

    /**
     * Prunes the regions of an availability instance type and reports whether nothing is left.
     */
    private boolean isInstanceTypeEmptyAfterGating(
            InstanceTypeAvailabilityResponse.InstanceType instanceType, String ncaId,
            Set<String> ownedClusterIds) {
        if (instanceType.getRegions() != null) {
            instanceType.getRegions().removeIf(
                    region -> isAvailabilityRegionEmptyAfterGating(region, ncaId,
                                                                   ownedClusterIds));
        }
        return isSetEmptyOrNull(instanceType.getRegions());
    }

    /**
     * Prunes the clusters of an availability region and reports whether nothing is left.
     */
    private boolean isAvailabilityRegionEmptyAfterGating(
            InstanceTypeAvailabilityResponse.Region region, String ncaId,
            Set<String> ownedClusterIds) {
        if (region.getClusters() != null) {
            region.getClusters().removeIf(
                    cluster -> !isClusterAllowed(ncaId, cluster.getClusterId(), ownedClusterIds));
        }
        return isSetEmptyOrNull(region.getClusters());
    }

    /**
     * Prunes the regions of a usage instance and reports whether nothing is left.
     */
    private boolean isUsageInstanceEmptyAfterGating(GpuUsageResponse.Instance instance,
                                                    String ncaId, Set<String> ownedClusterIds) {
        if (instance.getRegions() != null) {
            instance.getRegions().removeIf(
                    region -> isUsageRegionEmptyAfterGating(region, ncaId, ownedClusterIds));
        }
        return isListEmptyOrNull(instance.getRegions());
    }

    /**
     * Prunes the clusters of a usage region and reports whether nothing is left.
     */
    private boolean isUsageRegionEmptyAfterGating(GpuUsageResponse.Region region, String ncaId,
                                                  Set<String> ownedClusterIds) {
        if (region.getClusters() != null) {
            region.getClusters().removeIf(
                    cluster -> !isClusterAllowed(ncaId, cluster.getClusterId(), ownedClusterIds));
        }
        return isListEmptyOrNull(region.getClusters());
    }

    /**
     * A cluster is allowed when the policy names it or when the NCA ID owns it. A denied account
     * gets neither: {@code clusters: []} is a deliberate kill switch, so it has to outrank the
     * ownership exemption or the switch would not hold for an org that registered its own
     * clusters.
     */
    private boolean isClusterAllowed(String ncaId, String clusterId, Set<String> ownedClusterIds) {
        if (capacityGatingProperties.isOrgDenied(ncaId)) {
            return false;
        }
        return capacityGatingProperties.isClusterAllowedForNca(ncaId, clusterId)
                || ownedClusterIds.contains(clusterId);
    }

    /**
     * Whether the cluster dimension can withhold anything from this NCA ID. A GPU-only policy
     * admits every cluster, so the nested region walk is skipped: it would only ever strip
     * regions and instance types that carried no clusters to begin with, changing the shape of a
     * response the policy never meant to touch.
     */
    private boolean restrictsClusters(String ncaId) {
        return capacityGatingProperties.isOrgDenied(ncaId)
                || capacityGatingProperties.hasClusterRestriction(ncaId);
    }

    /**
     * The clusters this NCA ID registered itself, used to exempt them from gating on the
     * responses that carry no per-cluster owner. Only read for an account whose policy actually
     * restricts clusters, so neither ungated nor GPU-only accounts pay for this query.
     */
    private Set<String> ownedClusterIds(String ncaId) {
        if (!capacityGatingProperties.hasClusterRestriction(ncaId)) {
            return Set.of();
        }
        return clusterRepository.getAllClustersInAnAccount(ncaId).stream()
                .map(ClusterEntity::getClusterId)
                .filter(Objects::nonNull)
                .collect(Collectors.toCollection(HashSet::new));
    }

    private void throwGatingError(@NotNull IcmsUnifiedError icmsUnifiedError,
                                  @NotNull SpotInstanceRequestSchema instanceRequest) {
        String message;
        if (icmsUnifiedError == NVCF_CUSTOMER_NO_ACCESS_TO_GPU) {
            message = String.format(icmsUnifiedError.defaultMessageFormat(),
                                    instanceRequest.getGpu());
        } else if (icmsUnifiedError == NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE) {
            message = String.format(icmsUnifiedError.defaultMessageFormat(),
                                    instanceRequest.getInstanceType(), instanceRequest.getGpu());
        } else {
            message = icmsUnifiedError.defaultMessageFormat();
        }

        unifiedErrorReporter.reportAndThrow(new IcmsHttpUnifiedErrorException(
                icmsUnifiedError, HttpStatus.CONFLICT, message,
                toUnifiedErrorData(instanceRequest)));
    }
}
