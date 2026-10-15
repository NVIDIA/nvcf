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
package com.nvidia.icms.service.account;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.InstanceTypeDetails;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse.NodeType;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.extensions.api.ClusterAuthorizationService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.byoc.ClustersService.ReadyClusterInfo;
import com.nvidia.icms.service.extensions.api.ReservationProcessor;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static com.nvidia.icms.service.byoc.ClustersService.toReadyClusterInfo;

@Service
@AllArgsConstructor
@Slf4j
public class ClusterGpuInfoHelper {

    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final ClusterTargetingHelper clusterTargetingHelper;
    private final ReservationProcessor reservationProcessor;
    private final InstanceLifecycleHelper instanceLifecycleHelper;
    private final ComputePlatformService computePlatformService;
    private final ClusterAuthorizationService clusterAuthorizationService;


    public InstanceTypeDetails toInstanceTypeDetails(InstanceTypeAvailabilityResponse.InstanceType instanceType, InstanceTypeAvailabilityResponse.Gpu gpu) {
        return InstanceTypeDetails.builder()
                .name(instanceType.getInstanceName())
                .value(instanceType.getValue())
                .description(instanceType.getDescription())
                .cpuCores(instanceType.getCpuCores())
                .systemMemory(instanceType.getSystemMemory())
                .gpuMemory(instanceType.getGpuMemory())
                .gpuCount(instanceType.getGpuCount())
                .availableCapacity(0)
                .gpuName(gpu.getGpuName())
                .defaultable(instanceType.isDefaultable())
                .cpuArch(instanceType.getCpuArch())
                .os(instanceType.getOs())
                .driverVersion(instanceType.getDriverVersion())
                .storage(instanceType.getStorage())
                .nodeType(instanceType.getNodeType() == NodeType.SINGLE ? NodeTypeEnum.SINGLE : NodeTypeEnum.MULTI)
                .attributes(new HashSet<>())
                .regions(new HashSet<>())
                .clusters(new HashSet<>())
                .build();
    }


    public boolean includeClusterBasedOnAccessLevel(InstanceTypeAvailabilityResponse.Cluster cluster) {
        return icmsConfigurationProperties.isIncludeCustomPublicClustersInAccountInfoApis() &&
                !computePlatformService.isPlatformCluster(cluster.getClusterGroup());
    }


    /**
     * Determines the available GPU capacity for a specific GPU type in a given cluster.
     * Behavior depends on the resource provider, reservation state, and configuration flags.
     *
     * For BYOC providers:
     * - Returns spot capacity directly from the cluster for the requested GPU type.
     * - Reservation logic is not applied.
     *
     * For non-BYOC providers (reservations apply only to non-BYOC):
     * - If the GPU type has active reservations anywhere in non-BYOC clusters:
     *   - If this cluster has an active reservation:
     *     - Calculate reserved capacity based on cluster health:
     *       - If cluster is healthy: use reservation's available capacity reported by the zone.
     *       - If cluster is unhealthy and reservation backup is enabled: derive capacity from active instances.
     *       - Otherwise: treat reserved capacity as 0.
     *     - If spotPostReservedExhaustionForFunctionEnabled flag is DISABLED:
     *       - Return only reserved capacity (strict reservation enforcement).
     *     - If spotPostReservedExhaustionForFunctionEnabled flag is ENABLED:
     *       - Return reserved capacity + spot capacity (combined capacity from both sources).
     *   - If this cluster has no active reservation:
     *     - If spotPostReservedExhaustionForFunctionEnabled flag is DISABLED: return 0 (reservation enforcement).
     *     - If spotPostReservedExhaustionForFunctionEnabled flag is ENABLED: return spot capacity (non-reserved clusters can provide capacity).
     * - If the GPU type has no reservations anywhere in non-BYOC clusters:
     *   - Return the cluster's available spot capacity (only from healthy clusters).
     *
     * This enforces that reserved GPUs are consumed only from reserved clusters first,
     * and when the flag is enabled, both reserved and spot capacity are combined for increased availability.
     *
     * @param ncaId NVIDIA Cloud Account identifier used for logging and reservation lookup
     * @param readyClusterInfo Cluster metadata including provider, region, GPU, and clusterId
     * @param activeReservationByGpuByClusterId Map keyed by GPU name, then clusterId to that cluster's active reservations
     * @param cloudHealthByClusterId Map of clusterId to its current cloud health snapshot
     * @return Number of GPUs available for the requested instance type in this cluster
     */
    public int  getAvailableCapacity(@NotNull String ncaId,
                                     @NotNull ReadyClusterInfo readyClusterInfo,
                                     @NotNull Map<String, Map<String, List<ReservationEntity>>> activeReservationByGpuByClusterId, // Gpu:Cluster:Reservations
                                     @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId) {
        // reservation available only for non-BYOC
        Map<String, List<ReservationEntity>> activeGpuReservationsPerClusterId = computePlatformService.isComputePlatformProvider(readyClusterInfo.getClusterProvider()) ?
                activeReservationByGpuByClusterId.getOrDefault(readyClusterInfo.getGpu(), null) : null;

        if (activeGpuReservationsPerClusterId != null) {
            // If active reservation for the same GPU exists
            List<ReservationEntity> reservations = activeGpuReservationsPerClusterId.getOrDefault(readyClusterInfo.getClusterId(), null);
            int reserved = getReservedCapacity(ncaId, readyClusterInfo, reservations, cloudHealthByClusterId);

            if (!instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(ncaId)) {
                // Flag DISABLED: return only reserved capacity (strict reservation enforcement)
                return reserved;
            } else {
                // Flag ENABLED: return reserved + spot capacity
                int spotCapacity = getNonReservedCapacity(ncaId, readyClusterInfo, cloudHealthByClusterId);
                int totalCapacity = reserved + spotCapacity;
                log.debug("NcaId {}: Using combined capacity: reserved {} + spot {} = total {} for cluster {} with GPU {}",
                         ncaId, reserved, spotCapacity, totalCapacity, readyClusterInfo.getClusterId(), readyClusterInfo.getGpu());
                return totalCapacity;
            }
        }
        else {
            // If ncaID does not have reservation for this Gpu, get current available capacity if cluster is healthy
            return getNonReservedCapacity(ncaId, readyClusterInfo, cloudHealthByClusterId);
        }
    }


    private int getReservedCapacity(@NotNull String ncaId,
                                   @NotNull ReadyClusterInfo readyClusterInfo,
                                   List<ReservationEntity> reservations,
                                   @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId) {
        /*
        1. Check the health of zone
        2. If zone is healthy: use reserved capacity reported by zone
        3. If zone is unhealthy and reservation backup enabled: find available capacity from active instances
         */

        int availableGpus = 0;
        if (reservations != null && !reservations.isEmpty()) {
            // if active reservation exists for this cluster
            for (ReservationEntity reservation : reservations) {

                // 1. Check the health of zone
                if (isCloudHealthy(cloudHealthByClusterId.get(reservation.getClusterId()))) {
                    // 2. If zone is healthy: use reserved capacity reported by zone
                    Double availableCapacityFromHealthyReservation = reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation);
                    availableGpus += availableCapacityFromHealthyReservation.intValue();
                    log.info("NcaId {}: gpuType: {} ClusterId: {} is HEALTHY, finding available capacity from active reserved_backup instances, reservationId: {} availableCapacity: {}",
                             ncaId, readyClusterInfo.getGpu(), readyClusterInfo.getClusterId(), reservation.getReservationId(), availableCapacityFromHealthyReservation);

                } else {
                    // 3. If zone is unhealthy and reservation backup enabled: find available capacity from active instances
                    if (icmsConfigurationProperties.isReservationBackupEnabled()) {
                        Double availableCapacityFromUnhealthyReservation = reservationProcessor.calculateAvailableCapacityForUnhealthyZone(reservation);
                        availableGpus += availableCapacityFromUnhealthyReservation.intValue();
                        log.info("NcaId {}: gpuType: {} ClusterId: {} is UNHEALTHY, finding available reserved capacity from active reserved_backup instances, reservationId: {} availableCapacity: {}",
                                ncaId, readyClusterInfo.getGpu(), readyClusterInfo.getClusterId(), reservation.getReservationId(), availableCapacityFromUnhealthyReservation);
                    }
                }
            }

            if (availableGpus <= 0) {
                // exclude cluster with reservation but without available reserved capacity
                log.info(
                        "NcaId {}: ClusterId {} does not have available capacity for reserved GPU {}",
                        ncaId, readyClusterInfo.getClusterId(), readyClusterInfo.getGpu());
            } else {
                // return reserved capacity
                log.info("NcaId {}: ClusterId {} has reserved capacity of {} GPUs for  GPU {}",
                         ncaId, readyClusterInfo.getClusterId(), availableGpus,
                         readyClusterInfo.getGpu());
            }
        } else {
            // skip cluster with the same GPU type but without active reservation
            log.info("NcaId {}: ClusterId {} does not have a reservation for reserved GPU {}",
                     ncaId, readyClusterInfo.getClusterId(), readyClusterInfo.getGpu());
        }
        return availableGpus;
    }


    public int getNonReservedCapacity(@NotNull String ncaId,
                                      @NotNull ReadyClusterInfo readyClusterInfo,
                                      @NotNull Map<String, CloudHealthEntity> cloudHealthByClusterId) {
        int availableGpus = 0;
        CloudHealthEntity cloudHealth = cloudHealthByClusterId.get(
                readyClusterInfo.getClusterId());
        GpuCapacity gpuCapacity = getHealthyClusterCapacity(
                cloudHealth, readyClusterInfo.getGpu());
        if (gpuCapacity == null) {
            log.info("NcaId {}: ClusterId {} is skipped because it is unhealthy or does not have capacity for GPU {}",
                     ncaId, readyClusterInfo.getClusterId(), readyClusterInfo.getGpu());
        } else {
            availableGpus = gpuCapacity.getAvailable();
            log.debug("NcaId {}: ClusterId {} has available capacity of {} GPUs for  GPU {}",
                     ncaId, readyClusterInfo.getClusterId(), availableGpus,
                     readyClusterInfo.getGpu());
        }
        return availableGpus;
    }

    /**
     * Delegates to {@link ReservationProcessor#getActiveReservationsForNcaId(String)}.
     * Kept as a thin pass-through so existing callers (e.g. {@code AccountInfoService}) do not
     * need to be rewritten; the actual lookup + filtering lives in the non-BYOC module impl.
     */
    public List<ReservationEntity> getActiveReservationsPerNcaId(@NotNull String ncaId) {
        return reservationProcessor.getActiveReservationsForNcaId(ncaId);
    }


    /**
     * Retrieves information about ready clusters available for a given NCA ID.
     * This method fetches cluster information from two sources:
     * 1. Wildcard allowed clusters (available to all accounts)
     * 2. NCA-specific clusters (if an NCA ID is provided)
     *
     * @param ncaId The NVIDIA Cloud Account ID for which to retrieve cluster information,
     *              or null to retrieve only wildcard clusters
     * @return A map where keys are cluster IDs and values are ReadyClusterInfo objects
     *         containing metadata about each ready cluster
     */

    public Set<ReadyClusterInfo> getReadyClusterInfo(@NotNull String ncaId) {

        Set<ClusterByGroupIdAndIdEntity> allClusters = new HashSet<>(
                clusterTargetingHelper.getWildCardAllowedClusterCachedInfo());

        if (!ncaId.equals(ClusterRepository.WILDCARD)) {
            allClusters.addAll(clusterTargetingHelper.getReadyClusterEntitiesForNcaId(ncaId));
        }

        // Delegated to ClusterAuthorizationService SPI so this method has no
        // Non-BYOC-specific logic. NoOp impl leaves the set unchanged.
        clusterAuthorizationService.filterAuthorizedClusters(ncaId, allClusters);

        Set<ReadyClusterInfo> readyClusterInfoSet = new HashSet<>(toReadyClusterInfo(allClusters));

        // Restrict limited GPUs to dedicated NGC orgs on compute-platform clusters.
        // No-op when icms.gpu-allowed-nca-ids is empty or the GPU is not listed (existing behavior);
        readyClusterInfoSet.removeIf(rci ->
                computePlatformService.isComputePlatformProvider(rci.getClusterProvider())
                        && !icmsConfigurationProperties.isNcaAllowedForGpu(rci.getGpu(), ncaId));

        return readyClusterInfoSet;
    }

    public Map<String, List<ReadyClusterInfo>> buildRegionToClusterInfoMap(@NotNull Set<ReadyClusterInfo> clusterInfoByClusterIdByGpu) {
        Map<String, List<ReadyClusterInfo>> regionToClusterInfos = new HashMap<>();
        for (ReadyClusterInfo readyClusterInfo : clusterInfoByClusterIdByGpu) {
            String region = readyClusterInfo.getRegion();
            regionToClusterInfos.computeIfAbsent(region, k -> new ArrayList<>())
                    .add(readyClusterInfo);
        }

        return regionToClusterInfos;
    }

    public void updateValuesForNewInstance(@NotNull InstanceTypeAvailabilityResponse.InstanceType responseInstanceType,
                                            @NotNull InstanceTypeV5Udt instanceType) {
        responseInstanceType.setValue(instanceType.getValue());
        responseInstanceType.setDescription(instanceType.getDescription());
        responseInstanceType.setCpuArch(instanceType.getCpuArch());
        responseInstanceType.setCpuCores(instanceType.getCpuCores());
        responseInstanceType.setSystemMemory(instanceType.getSystemMemory());
        responseInstanceType.setGpuMemory(instanceType.getGpuMemory());
        responseInstanceType.setGpuCount(instanceType.getGpuCount());
        responseInstanceType.setOs(instanceType.getOs());
        responseInstanceType.setDriverVersion(instanceType.getDriverVersion());
        responseInstanceType.setStorage(instanceType.getStorage());
        responseInstanceType.setNodeType(StringUtils.isBlank(instanceType.getNodeType()) ?
                                                 NodeType.SINGLE :
                                                 InstanceTypeAvailabilityResponse.NodeType.valueOf(instanceType.getNodeType()));
        responseInstanceType.setDefaultable(instanceType.getIsDefault() != null && instanceType.getIsDefault());

    }

    public void updateValuesForNewCluster(@NotNull InstanceTypeAvailabilityResponse.Cluster responseCluster,
                                           @NotNull ReadyClusterInfo readyClusterInfo,
                                           @NotNull InstanceTypeV5Udt instanceType,
                                           int gpuAvailable) {
        responseCluster.setClusterName(readyClusterInfo.getClusterName());
        responseCluster.setCloudProvider(
                readyClusterInfo.getClusterProvider() != null ? readyClusterInfo.getClusterProvider().name() : null);
        responseCluster.setClusterGroup(
                readyClusterInfo.getClusterGroupName());
        responseCluster.setIsDefaultInstanceType(
                instanceType.getIsDefault() != null && instanceType.getIsDefault());
        responseCluster.setMaxClusterAvailableCapacity(
                getMaxInstancesCreatedWithAvailableCapacity(
                        gpuAvailable, instanceType.getGpuCount()));
        responseCluster.setAttributes(readyClusterInfo.getAttributes() != null
                                              ? readyClusterInfo.getAttributes()
                                              : new HashSet<>());
    }


    public boolean isClusterAllowed(ReadyClusterInfo readyClusterInfo, @NotNull GpuUsageFilter filter) {
        if (readyClusterInfo == null) {
            return false;
        }

        if (!filter.isClusterNameAllowed(readyClusterInfo.getClusterName()) ||
                !filter.isGpuNameAllowed(readyClusterInfo.getGpu()) ||
                !filter.areAttributesAllowed(readyClusterInfo.getAttributes()) ||
                !filter.isClusterGroupNameAllowed(readyClusterInfo.getClusterGroupName()) ||
                !filter.isInstanceUsageAllowed(readyClusterInfo.getSupportedNodeTypes())) {
            return false;
        }
        else {
            return true;
        }
    }


    private boolean isCloudHealthy(@Nullable CloudHealthEntity cloudHealth) {
        return cloudHealth != null && cloudHealth.getStatus() != null && cloudHealth.getStatus().equals(CloudHealthStatus.HEALTHY);
    }

    public @Nullable GpuCapacity getHealthyClusterCapacity(@Nullable CloudHealthEntity cloudHealth, @NotNull String gpuName) {
        if (!isCloudHealthy(cloudHealth)) {
            return null; // skip unhealthy cluster
        }

        if (cloudHealth.getGpuUsage() == null ||
                cloudHealth.getGpuUsage().isEmpty() ||
                !cloudHealth.getGpuUsage().containsKey(gpuName)) {
            return null; // skip gpu without capacity
        }

        GpuCapacity gpuCapacity = cloudHealth.getGpuUsage().get(gpuName);
        if (gpuCapacity.getAvailable() <= 0) {
            return null; // skip gpu without available capacity
        }

        return gpuCapacity;
    }

    public static int getMaxInstancesCreatedWithAvailableCapacity(
            int available,
            int gpuCountForInstanceType) {
        if (gpuCountForInstanceType != 0) {
            return available / gpuCountForInstanceType;
        }
        return 0;
    }

}
