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
import static com.nvidia.icms.util.InstanceServiceUtil.isListEmptyOrNull;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.createInstances.RequestInstanceDestination;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.HashSet;
import java.util.List;
import java.util.Objects;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Service;

/**
 * Applies {@code icms.cluster-gating} to everything a gated NCA ID can get or allocate. 
 */
@Service
@AllArgsConstructor
@Slf4j
public class ClusterGatingService {

    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final UnifiedErrorReporter unifiedErrorReporter;
    private final ClusterRepository clusterRepository;

    /**
     * Removes clusters withheld from this NCA ID from an account-info response. A region left
     * with no clusters is dropped, as is an instance type left with no regions and a GPU left
     * with no instance types.
     */
    public void removeGatedClustersFromAccountInfo(@NotNull InstanceTypeAvailabilityResponse result,
                                                   @NotNull String ncaId) {
        if (!icmsConfigurationProperties.hasClusterGating(ncaId) || result.getGpus() == null) {
            return;
        }

        Set<String> ownedClusterIds = ownedClusterIds(ncaId);

        result.getGpus().removeIf(gpu -> {
            if (gpu.getInstanceTypes() != null) {
                gpu.getInstanceTypes().removeIf(
                        instanceType -> isInstanceTypeEmptyAfterGating(instanceType, ncaId,
                                                                       ownedClusterIds));
            }

            boolean noInstanceTypesLeft = isSetEmptyOrNull(gpu.getInstanceTypes());
            if (noInstanceTypesLeft) {
                log.info("NcaId {}: GPU {} removed from account info response because cluster "
                                 + "gating left it with no clusters", ncaId, gpu.getGpuName());
            }
            return noInstanceTypesLeft;
        });
    }

    /**
     * Removes clusters withheld from this NCA ID from the cluster-group response, dropping a
     * group left with no clusters. A group that never listed clusters is left alone, so this
     * only ever removes a group whose entire cluster list was gated out.
     */
    public void removeGatedClusterGroups(@NotNull Set<ClusterGroups> clusterGroupsSet,
                                         @NotNull String ncaId) {
        if (!icmsConfigurationProperties.hasClusterGating(ncaId)) {
            return;
        }

        Set<String> ownedClusterIds = ownedClusterIds(ncaId);

        clusterGroupsSet.removeIf(clusterGroup -> {
            if (isSetEmptyOrNull(clusterGroup.getClusters())) {
                return false;
            }

            clusterGroup.getClusters().removeIf(
                    cluster -> !isClusterAllowed(ncaId, cluster.getId(), ownedClusterIds));

            boolean noClustersLeft = isSetEmptyOrNull(clusterGroup.getClusters());
            if (noClustersLeft) {
                log.info("NcaId {}: clusterGroup {} removed from response by cluster gating",
                         ncaId, clusterGroup.getName());
            }
            return noClustersLeft;
        });
    }

    /**
     * Removes clusters withheld from this NCA ID from the authorized cluster listing. Clusters
     * the NCA ID owns are left untouched: gating controls what an org may borrow from others,
     * not whether it can still see and manage the clusters it registered itself.
     */
    public void removeGatedAuthorizedClusters(@NotNull List<GetClusterResponse> clusters,
                                              @NotNull String ncaId) {
        if (!icmsConfigurationProperties.hasClusterGating(ncaId)) {
            return;
        }

        // The response carries the owning NCA ID per cluster, so ownership is settled inline and
        // this path never needs the owned-cluster lookup.
        clusters.removeIf(cluster -> {
            if (Objects.equals(cluster.getNcaId(), ncaId)) {
                return false;
            }

            boolean gatedOut = !icmsConfigurationProperties.isClusterAllowedForNca(
                    ncaId, cluster.getClusterId());
            if (gatedOut) {
                log.trace("NcaId {}: cluster {} removed from authorized cluster listing by "
                                  + "cluster gating", ncaId, cluster.getClusterId());
            }
            return gatedOut;
        });
    }

    /**
     * Removes clusters withheld from this NCA ID from a GPU usage response. A region left with no
     * clusters is dropped, as is an instance left with no regions and a GPU left with no
     * instances.
     */
    public void removeGatedClustersFromGpuUsage(@Nullable List<GpuUsageResponse.Gpu> gpus,
                                                @NotNull String ncaId) {
        if (!icmsConfigurationProperties.hasClusterGating(ncaId) || gpus == null) {
            return;
        }

        Set<String> ownedClusterIds = ownedClusterIds(ncaId);

        gpus.removeIf(gpu -> {
            if (gpu.getInstances() != null) {
                gpu.getInstances().removeIf(
                        instance -> isUsageInstanceEmptyAfterGating(instance, ncaId,
                                                                    ownedClusterIds));
            }

            boolean noInstancesLeft = isListEmptyOrNull(gpu.getInstances());
            if (noInstancesLeft) {
                log.info("NcaId {}: GPU {} removed from gpu usage response because cluster gating "
                                 + "left it with no clusters", ncaId, gpu.getGpuName());
            }
            return noInstancesLeft;
        });
    }

    /**
     * Removes destinations withheld from this NCA ID from an instance request. Unlike the read
     * paths, an empty result here is an error: the caller asked for capacity in clusters it may
     * not use, so this throws rather than silently launching nothing.
     */
    public void removeGatedDestinations(@NotNull Set<RequestInstanceDestination> destinations,
                                        @NotNull SpotInstanceRequestSchema instanceRequest) {
        String ncaId = instanceRequest.getNcaId();
        if (!icmsConfigurationProperties.hasClusterGating(ncaId)) {
            return;
        }

        int destinationCountBeforeGating = destinations.size();
        // Destinations carry the owning NCA ID, so ownership is settled inline here too.
        Set<RequestInstanceDestination> gatedOut = destinations.stream()
                .filter(destination -> !Objects.equals(destination.getNcaId(), ncaId))
                .filter(destination -> !icmsConfigurationProperties.isClusterAllowedForNca(
                        ncaId, destination.getClusterId()))
                .collect(Collectors.toSet());

        destinations.removeAll(gatedOut);

        log.info("InstanceRequest: {}: NcaId {}: cluster gating kept {} of {} destinations, "
                         + "removed clusters {}",
                 instanceRequest.getLoggingId(), ncaId, destinations.size(),
                 destinationCountBeforeGating,
                 gatedOut.stream().map(RequestInstanceDestination::getClusterId).distinct()
                         .toList());

        if (destinations.isEmpty()) {
            unifiedErrorReporter.reportAndThrow(new IcmsHttpUnifiedErrorException(
                    NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS, HttpStatus.CONFLICT,
                    NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS.defaultMessageFormat(),
                    toUnifiedErrorData(instanceRequest)));
        }
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
     * A cluster is allowed when the allow list names it or when the NCA ID owns it.
     */
    private boolean isClusterAllowed(String ncaId, String clusterId, Set<String> ownedClusterIds) {
        return icmsConfigurationProperties.isClusterAllowedForNca(ncaId, clusterId)
                || ownedClusterIds.contains(clusterId);
    }

    /**
     * The clusters this NCA ID registered itself, used to exempt them from gating on the
     * responses that carry no per-cluster owner. Only reached for a gated NCA ID, so ungated
     * accounts never pay for this read.
     */
    private Set<String> ownedClusterIds(String ncaId) {
        return clusterRepository.getAllClustersInAnAccount(ncaId).stream()
                .map(ClusterEntity::getClusterId)
                .filter(Objects::nonNull)
                .collect(Collectors.toCollection(HashSet::new));
    }
}
