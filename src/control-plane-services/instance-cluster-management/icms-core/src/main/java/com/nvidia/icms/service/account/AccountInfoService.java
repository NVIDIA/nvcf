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

import static com.nvidia.icms.service.account.ClusterGpuInfoHelper.getMaxInstancesCreatedWithAvailableCapacity;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.InstanceTypeDetails;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse.Cluster;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse.Gpu;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.byoc.ClustersService.ReadyClusterInfo;
import com.nvidia.icms.service.platform.ComputePlatformService;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.exception.ExceptionUtils;
import org.springframework.stereotype.Service;

@Service
@AllArgsConstructor
@Slf4j
public class AccountInfoService {

    public static final String GPUS_RESPONSE_FIELD_NAME = "gpus";
    public static final String REGIONS_RESPONSE_FIELD_NAME = "regions";
    public static final String CLUSTER_NAMES_RESPONSE_FIELD_NAME = "clusterNames";
    public static final String ATTRIBUTES_RESPONSE_FIELD_NAME = "attributes";

    private final ClusterTargetingHelper clusterTargetingHelper;

    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final ClusterGpuInfoHelper clusterGpuInfoHelper;

    private final ComputePlatformService computePlatformService;

    @Observed
    public Map<String, Set<String>> getAllGpusForAccount(
            @NotNull String ncaId,
            @NotNull GpuUsageFilter filter) {
        try {
            InstanceTypeAvailabilityResponse availability = getClusterCapacityData(ncaId, filter);

            TreeSet<String> gpuSet = availability.getGpus().stream().map(Gpu::getGpuName).
                    collect(Collectors.toCollection(TreeSet::new));

            return Map.of(GPUS_RESPONSE_FIELD_NAME, gpuSet);
        } catch (Exception e) {
            String errMsg =
                    String.format("Error while getting available GPUs, error: %s",
                                  e.getMessage());
            log.error("error: {}, Stacktrace: {}", errMsg, ExceptionUtils.getStackTrace(e));
            throw new IcmsInternalServerException(errMsg);
        }

    }


    @Observed
    public Map<String, Set<String>> getAvailableRegions(
            @NotNull String ncaId,
            @NonNull GpuUsageFilter filter) {

        try {
            InstanceTypeAvailabilityResponse availability = getClusterCapacityData(
                    ncaId, filter);

            TreeSet<String> regionSet = availability.getGpus().stream()
                    .flatMap(gpu -> gpu.getInstanceTypes().stream())
                    .flatMap(instanceType -> instanceType.getRegions().stream())
                    .map(region -> region.getRegionName().toLowerCase())
                    .collect(Collectors.toCollection(TreeSet::new));

            return Map.of(REGIONS_RESPONSE_FIELD_NAME, regionSet);
        } catch (Exception e) {
            String errMsg =
                    String.format("Error while getting available regions, error: %s",
                                  e.getMessage());
            log.error("error: {}, Stacktrace: {}", errMsg, ExceptionUtils.getStackTrace(e));
            throw new IcmsInternalServerException(errMsg);
        }
    }


    @Observed
    public Map<String, Set<String>> getAvailableClusterNames(
            @NotNull String ncaId,
            @NonNull GpuUsageFilter filter) {

        try {
            InstanceTypeAvailabilityResponse availability = getClusterCapacityData(
                    ncaId, filter);

            TreeSet<String> clusterNameSet = availability.getGpus().stream()
                    .flatMap(gpu -> gpu.getInstanceTypes().stream())
                    .flatMap(instanceType -> instanceType.getRegions().stream())
                    .flatMap(region -> region.getClusters().stream())
                    .filter(clusterGpuInfoHelper::includeClusterBasedOnAccessLevel)
                    .map(Cluster::getClusterName)
                    .collect(Collectors.toCollection(TreeSet::new));

            return Map.of(CLUSTER_NAMES_RESPONSE_FIELD_NAME, clusterNameSet);
        } catch (Exception e) {
            String errMsg =
                    String.format("Error while getting available clusters, error: %s",
                                  e.getMessage());
            log.error("error: {}, Stacktrace: {}", errMsg, ExceptionUtils.getStackTrace(e));
            throw new IcmsInternalServerException(errMsg);
        }
    }


    @Observed
    public Map<String, Set<String>> getAvailableAttributes(
            @NonNull String ncaId,
            @NonNull GpuUsageFilter filter) {
        try {
            InstanceTypeAvailabilityResponse availability = getClusterCapacityData(
                    ncaId, filter);

            TreeSet<String> attributeSet = availability.getGpus().stream()
                    .flatMap(gpu -> gpu.getInstanceTypes().stream())
                    .flatMap(instanceType -> instanceType.getRegions().stream())
                    .flatMap(region -> region.getClusters().stream())
                    .flatMap(cluster -> cluster.getAttributes().stream())
                    .collect(Collectors.toCollection(TreeSet::new));

            return Map.of(ATTRIBUTES_RESPONSE_FIELD_NAME, attributeSet);
        } catch (Exception e) {
            String errMsg =
                    String.format("Error while getting available attributes, error: %s",
                                  e.getMessage());
            log.error("error: {}, Stacktrace: {}", errMsg, ExceptionUtils.getStackTrace(e));
            throw new IcmsInternalServerException(errMsg);
        }
    }



    @Observed
    public Map<String, Set<InstanceTypeDetails>> getAvailableInstanceTypes(
            String ncaId,
            @NotNull GpuUsageFilter filter) {
        try {
            InstanceTypeAvailabilityResponse availabilityData = getClusterCapacityData(ncaId, filter);
            Map<String, Set<InstanceTypeDetails>> result = convertToInstanceTypeDetailsByGpu(availabilityData);

            return result;

        } catch (Exception e) {
            String errMsg =
                    String.format("Error while getting available instance types, error: %s",
                                  e.getMessage());
            log.error("error: {}, Stacktrace: {}", errMsg, ExceptionUtils.getStackTrace(e));
            throw new IcmsInternalServerException(errMsg);
        }
    }

    /**
     * Retrieves instance type availability information for a given NCA ID.
     * This method provides a comprehensive view of available GPU instances across different regions and clusters,
     * including their configurations and capacities.
     * <p>
     * The response includes:
     * - List of available GPUs
     * - For each GPU:
     * - Available instance types
     * - Instance configurations (CPU cores, memory, storage, etc.)
     * - Regions and clusters where the instance type is available
     * - Available capacity in each cluster
     * - Cluster attributes and provider information
     *
     * @param ncaId The NCA ID for which to retrieve instance type availability information
     * @return An InstanceTypeAvailabilityResponse containing the list of available GPUs with their
     * instance types, regions, and clusters. The response is structured hierarchically:
     * GPUs -> Instance Types -> Regions -> Clusters, with detailed configuration and
     * capacity information at each level.
     */

    @Observed
    public InstanceTypeAvailabilityResponse getInstanceTypeAvailability(@NotNull String ncaId) {
        try {
            InstanceTypeAvailabilityResponse result = getClusterCapacityData(ncaId,
                                                                             new GpuUsageFilter());
            return combineNonByocClusters(result);
        } catch (Exception e) {
            String errMsg =
                    String.format("Error while getting instance type availability, error: %s",
                                  e.getMessage());
            log.error("error: {}, Stacktrace: {}", errMsg, ExceptionUtils.getStackTrace(e));
            throw new IcmsInternalServerException(errMsg);
        }
    }


    private @NotNull InstanceTypeAvailabilityResponse getClusterCapacityData(@NotNull String ncaId, @NotNull GpuUsageFilter filter) {

        InstanceTypeAvailabilityResponse result = new InstanceTypeAvailabilityResponse();

        Map<String, CloudHealthEntity> cloudHealthByClusterId = clusterTargetingHelper.getAllClusterHealthInMap();

        Set<ReadyClusterInfo> clusterInfoByClusterIdByGpu = clusterGpuInfoHelper.getReadyClusterInfo(ncaId);

        // [Gpu: [Cluster: [Reservations]]]
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = getActiveReservationMap(ncaId);

        // Group clusters by region
        Map<String, List<ReadyClusterInfo>> regionToClusterInfos = clusterGpuInfoHelper.buildRegionToClusterInfoMap(clusterInfoByClusterIdByGpu);

        for(String regionName : regionToClusterInfos.keySet()) {
            if (regionToClusterInfos.get(regionName) == null || regionToClusterInfos.get(regionName).isEmpty()) {
                continue; // skip empty region
            }

            if (!filter.isRegionNameAllowed(regionName)) {
                continue;
            }

            for(ReadyClusterInfo readyClusterInfo : regionToClusterInfos.get(regionName)) {

                if (!clusterGpuInfoHelper.isClusterAllowed(readyClusterInfo, filter)) {
                    continue;
                }

                int availableGpus = clusterGpuInfoHelper.getAvailableCapacity(ncaId,
                                                                              readyClusterInfo,
                                                                              reservationByGpuByClusterId,
                                                                              cloudHealthByClusterId);

               if (availableGpus <= 0 && filter.isValidateCapacity()) {
                   continue; // skip cluster without available capacity when validation enabled
               }

                for (InstanceTypeV5Udt instanceType : readyClusterInfo.getInstanceTypes()) {
                    addInstanceTypeV5Udt(instanceType, readyClusterInfo, result,  availableGpus, regionName, filter);
                }
            }
        }
        return result;
    }


    private void addInstanceTypeV5Udt(@NotNull InstanceTypeV5Udt instanceType,
                                      @NotNull ReadyClusterInfo readyClusterInfo,
                                      @NotNull InstanceTypeAvailabilityResponse result,
                                      int availableGpus,
                                      @NotNull String regionName,
                                      @NotNull GpuUsageFilter filter) {

        // Checking if instanceType allowed in filter
        if (!filter.isInstanceTypeAllowed(instanceType.getName())) {
            return;
        }

        // Checking if instanceTypeUsage allowed in filter
        if (!filter.isInstanceUsageAllowed(Set.of(NvcaConverter.getNodeTypeEnum(instanceType)))) {
            return;
        }

        // Checking if instanceType can be created with available GPU
        if (filter.isValidateCapacity() &&
                getMaxInstancesCreatedWithAvailableCapacity(availableGpus, instanceType.getGpuCount()) <= 0) {
            return;
        }

        InstanceTypeAvailabilityResponse.Gpu responseGpu = result.findOrCreateGpu(
                readyClusterInfo.getGpu());

        InstanceTypeAvailabilityResponse.InstanceType responseInstanceType =
                responseGpu.findOrCreateInstanceType(instanceType.getName());

        if (StringUtils.isEmpty(responseInstanceType.getDescription())) {
            clusterGpuInfoHelper.updateValuesForNewInstance(responseInstanceType, instanceType);
        }

        InstanceTypeAvailabilityResponse.Region responseRegion =
                responseInstanceType.findOrCreateRegion(regionName);

        InstanceTypeAvailabilityResponse.Cluster responseCluster =
                responseRegion.findOrCreateCluster(readyClusterInfo.getClusterId());

        if (StringUtils.isEmpty(responseCluster.getClusterName())) {
            clusterGpuInfoHelper.updateValuesForNewCluster(responseCluster,
                                                           readyClusterInfo,
                                                           instanceType,
                                                           availableGpus);
        } else {
            log.warn(
                    "Duplicated cluster found: GPU {} InstanceName {} region {} cluster {}",
                    readyClusterInfo.getGpu(), instanceType.getName(), regionName,
                    readyClusterInfo.getClusterId());
        }
    }


    // Returns map [ GpuName: [ClusterId: [Reservations...]]]
    private  @NotNull Map<String, Map<String, List<ReservationEntity>>> getActiveReservationMap(@NotNull String ncaId) {
        List<ReservationEntity> activeReservations = clusterGpuInfoHelper.getActiveReservationsPerNcaId(ncaId);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = new HashMap<>();
        activeReservations.forEach(r -> {
            Map<String, List<ReservationEntity>> reservationsByClusterId;
            if (reservationByGpuByClusterId.containsKey(r.getGpuType())) {
                reservationsByClusterId = reservationByGpuByClusterId.get(r.getGpuType());
            }
            else {
                reservationsByClusterId = new HashMap<>();
                reservationByGpuByClusterId.put(r.getGpuType(), reservationsByClusterId);
            }

            List<ReservationEntity> reservations;
            if (reservationsByClusterId.containsKey(r.getClusterId())) {
                reservations = reservationsByClusterId.get(r.getClusterId());
            }
            else {
                reservations = new ArrayList<>();
                reservationsByClusterId.put(r.getClusterId(), reservations);
            }

            reservations.add(r);
        });
        return reservationByGpuByClusterId;
    }


    // For /instanceType/Availability API we need to combine and show all non-BYOC zones under the same fake cluster
    // with fake name, so the underlying compute-platform structure is not visible for NGC users since they cannot
    // target to select specific non-BYOC zones.
    // All non-BYOC zones in the same region will be combined under the name "regionName-<computePlatform>"
    // where the suffix is the compute-platform backend that matched the configured registry.
    private InstanceTypeAvailabilityResponse combineNonByocClusters(@NotNull InstanceTypeAvailabilityResponse result) {
        if (result.getGpus() == null) {
            return result;
        }
        result.getGpus().forEach(gpu -> {
            if (gpu.getInstanceTypes() != null) {
                gpu.getInstanceTypes().forEach(instanceType -> {
                    if (instanceType.getRegions() != null) {
                        instanceType.getRegions().forEach(region -> {
                            if (region.getClusters() != null) {
                                InstanceTypeAvailabilityResponse.Cluster nonByocCluster = null;
                                for(InstanceTypeAvailabilityResponse.Cluster cluster: region.getClusters()) {
                                    if (computePlatformService.isComputePlatformBackend(cluster.getCloudProvider())) {
                                        if (nonByocCluster == null) { // if this is first non-BYOC cluster in region
                                            nonByocCluster = cluster;
                                            String combinedClusterName = region.getRegionName() + "-" + cluster.getCloudProvider();
                                            nonByocCluster.setClusterId(combinedClusterName);
                                            nonByocCluster.setClusterName(combinedClusterName);
                                        }
                                        else {
                                            // combine cluster with non-byoc cluster
                                            nonByocCluster.setMaxClusterAvailableCapacity(nonByocCluster.getMaxClusterAvailableCapacity() + cluster.getMaxClusterAvailableCapacity());
                                            // mark cluster for deleting
                                            cluster.setClusterId(null);
                                        }
                                    }
                                }
                                region.getClusters().removeIf(r -> StringUtils.isEmpty(r.getClusterId())); // remove combined clusters
                            }
                        });
                    }
                });
            }
        });

        return result;
    }


    private Map<String, Set<InstanceTypeDetails>> convertToInstanceTypeDetailsByGpu(@NotNull InstanceTypeAvailabilityResponse availabilityData) {
        Map<String, Set<InstanceTypeDetails>> result = new TreeMap<>();

        availabilityData.getGpus().forEach(gpu -> {
            if (gpu.getInstanceTypes() != null) {
                gpu.getInstanceTypes().forEach(instanceType -> {
                    if (instanceType.getRegions() != null) {
                        instanceType.getRegions().forEach(region -> {
                            if (region.getClusters() != null) {
                                for(InstanceTypeAvailabilityResponse.Cluster cluster: region.getClusters()) {

                                    if (!result.containsKey(gpu.getGpuName())) {
                                        result.put(gpu.getGpuName(), new TreeSet<>(Comparator.comparing(InstanceTypeDetails::getName)));
                                    }

                                    InstanceTypeDetails instanceTypeDetails = result.get(
                                                    gpu.getGpuName()).stream().filter(
                                                    r -> r.getName().equals(instanceType.getInstanceName())).findFirst()
                                            .orElse(null);

                                    if (instanceTypeDetails == null) {
                                        instanceTypeDetails = clusterGpuInfoHelper.toInstanceTypeDetails(instanceType, gpu);
                                        result.get(gpu.getGpuName()).add(instanceTypeDetails);
                                    }

                                    updateInstanceTypeDetails(instanceTypeDetails, region.getRegionName(), cluster, instanceType.isDefaultable());
                                }
                            }
                        });
                    }
                });
            }
        });
        return result;
    }


    InstanceTypeDetails updateInstanceTypeDetails(
            @NotNull InstanceTypeDetails instanceTypeDetails,
            @NotNull String regionName,
            @NotNull InstanceTypeAvailabilityResponse.Cluster cluster,
            boolean isDefaultable) {
        instanceTypeDetails.setAvailableCapacity(
                instanceTypeDetails.getAvailableCapacity() + cluster.getMaxClusterAvailableCapacity());

        instanceTypeDetails.getRegions().add(regionName);

        if (clusterGpuInfoHelper.includeClusterBasedOnAccessLevel(cluster)) {
            // non-BYOC zone names should not be reported
            instanceTypeDetails.getClusters().add(cluster.getClusterName());
        }

        if (cluster.getAttributes() != null && !cluster.getAttributes().isEmpty()) {
            instanceTypeDetails.getAttributes().addAll(cluster.getAttributes());
        }

        if (instanceTypeDetails.getDefaultable() == null || isDefaultable) {
            instanceTypeDetails.setDefaultable(isDefaultable);
        }

        return instanceTypeDetails;
    }

}

