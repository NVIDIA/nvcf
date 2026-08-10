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
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.account.DeploymentGpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.byoc.ClustersService.ReadyClusterInfo;
import com.nvidia.icms.service.platform.ComputePlatformService;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class GpuUsageService {

    private final InstanceRequestV2Repository instanceRequestV2Repository;
    private final InstanceV2Repository instanceV2Repository;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final ClusterGpuInfoHelper clusterGpuInfoHelper;
    private final ComputePlatformService computePlatformService;

    /**
     * Retrieves GPU usage information for a specific NCA ID.
     * This method fetches all instances associated with the NCA ID and groups them by GPU type,
     * region, and cluster to provide a comprehensive view of GPU usage.
     *
     * @param ncaId The NCA ID for which to retrieve GPU usage information
     * @return A GpuUsageResponse containing the GPU usage information, organized by GPU type,
     * instance type, region, and cluster, including counts of active and pending instances
     */
    public GpuUsageResponse getGpuUsage(@NotNull String ncaId) {
        try {
            List<InstanceRequestV2Entity> requests = instanceRequestV2Repository.findRequestsPerNcaId(ncaId);

            Set<String> requestIds = requests.stream().map(
                    InstanceRequestV2Entity::getRequestId).collect(Collectors.toSet());

            Map<String, List<InstanceV2Entity>> requestIdToInstances = getInstancesForRequestIds(
                    requestIds);

            List<GpuUsageResponse.Gpu> gpus = processInstances(requestIdToInstances, clusterGpuInfoHelper.getReadyClusterInfo(ncaId));

            return GpuUsageResponse.builder()
                    .gpus(gpus)
                    .build();
        } catch (Exception e) {
            log.error("Error encountered while trying to fetch gpu usage {}", e.getMessage(), e);
            throw e;
        }
    }

    /**
     * Retrieves GPU usage information for a specific deployment within an NCA.
     * This method fetches all instances associated with both the NCA ID and deployment ID,
     * and groups them by GPU type, region, and cluster to provide deployment-specific GPU usage.
     *
     * @param ncaId        The NCA ID for which to retrieve GPU usage information
     * @param deploymentId The deployment ID for which to retrieve GPU usage information
     * @return A DeploymentGpuUsageResponse containing the GPU usage information for the specified
     * deployment, organized by GPU type, instance type, region, and cluster, including
     * counts of active and pending instances
     */
    public DeploymentGpuUsageResponse getDeploymentGpuUsage(@NotNull String ncaId, @NotNull String deploymentId) {
        try {
            List<InstanceRequestV2Entity> requests = instanceRequestV2Repository.findRequestsPerNcaIdAndDeploymentId(
                    ncaId, UUID.fromString(deploymentId));

            Set<String> requestIds = requests.stream().map(
                    InstanceRequestV2Entity::getRequestId).collect(Collectors.toSet());

            Map<String, List<InstanceV2Entity>> requestIdToInstances = getInstancesForRequestIds(
                    requestIds);

            List<GpuUsageResponse.Gpu> gpus = processInstances(requestIdToInstances, clusterGpuInfoHelper.getReadyClusterInfo(ncaId));

            return DeploymentGpuUsageResponse.builder()
                    .deployments(List.of(DeploymentGpuUsageResponse.Deployment.builder()
                                                 .deploymentId(UUID.fromString(deploymentId))
                                                 .gpus(gpus)
                                                 .build()))
                    .build();
        } catch (Exception e) {
            log.error("Error encountered while trying to fetch gpu usage using deployment Id {}",
                      e.getMessage(), e);
            throw e;
        }
    }

    /**
     * Retrieves all instances associated with the provided request IDs.
     * This method queries the instance repository to fetch instances that match
     * the given set of request IDs, organizing them in a map for efficient processing.
     *
     * @param requestIds A set of request IDs for which to retrieve associated instances
     * @return A map where keys are request IDs and values are lists of InstanceV2Entity objects
     *         associated with each request ID
     */

    private Map<String, List<InstanceV2Entity>> getInstancesForRequestIds(
            @Nullable Set<String> requestIds) {
        return instanceV2Repository.findAllInstancesByCustomerAndRequestIds(null,
                                                                                requestIds,
                                                                                icmsConfigurationProperties.isFindInstancesByRequestIdForGpuUsageInParallel());
    }


    private GpuUsageResponse.Gpu findGpuInResponse(@NotNull String gpuName, @NotNull List<GpuUsageResponse.Gpu> response) {
        GpuUsageResponse.Gpu gpuInResponse = response.stream().filter(g -> g.getGpuName().equals(gpuName)).findFirst().orElse(null);
        if (gpuInResponse == null) {
            gpuInResponse = GpuUsageResponse.Gpu.builder()
                    .instances(new ArrayList<>())
                    .gpuName(gpuName)
                    .build();
            response.add(gpuInResponse);
        }

        return gpuInResponse;
    }

    private GpuUsageResponse.Instance findInstanceInResponse(@NotNull String instanceName, @NotNull List<GpuUsageResponse.Instance> response) {
        GpuUsageResponse.Instance instanceInResponse = response.stream().filter(g -> g.getInstanceName().equals(instanceName)).findFirst().orElse(null);
        if (instanceInResponse == null) {
            instanceInResponse = GpuUsageResponse.Instance.builder()
                    .regions(new ArrayList<>())
                    .instanceName(instanceName)
                    .build();
            response.add(instanceInResponse);
        }

        return instanceInResponse;
    }

    private GpuUsageResponse.Region findRegionInResponse(@NotNull String regionName, @NotNull List<GpuUsageResponse.Region> response) {
        GpuUsageResponse.Region regionInResponse = response.stream().filter(g -> g.getRegionName().equals(regionName)).findFirst().orElse(null);
        if (regionInResponse == null) {
            regionInResponse = GpuUsageResponse.Region.builder()
                    .clusters(new ArrayList<>())
                    .regionName(regionName)
                    .build();
            response.add(regionInResponse);
        }

        return regionInResponse;
    }

    private GpuUsageResponse.Cluster findClusterInResponse(@NotNull String clusterId, @NotNull String clusterGroupName, @NotNull List<GpuUsageResponse.Cluster> response) {
        GpuUsageResponse.Cluster clusterInResponse = response.stream().filter(g -> g.getClusterId().equals(clusterId)).findFirst().orElse(null);
        if (clusterInResponse == null) {
            clusterInResponse = GpuUsageResponse.Cluster.builder()
                    .status(GpuUsageResponse.Status.builder()
                                    .activeInstances(0)
                                    .pendingInstances(0)
                                    .build())
                    .clusterId(clusterId)
                    .clusterGroupName(clusterGroupName)
                    .build();
            response.add(clusterInResponse);
        }

        return clusterInResponse;
    }

    /**
     * Processes instance data and organizes it into a hierarchical structure by GPU type, instance type,
     * region, and cluster.
     * This method takes a map of request IDs to instances and transforms it into a structured list of GPU
     * objects, each containing information about instance types, regions, and clusters where the GPU is used.
     * The resulting structure provides a comprehensive view of GPU usage across the infrastructure.
     *
     * @param requestIdToInstances A map where keys are request IDs and values are lists of InstanceV2Entity
     *                            objects associated with each request
     * @param clusterInfoMap A map of cluster IDs to ReadyClusterInfo objects containing cluster metadata
     * @return A list of GpuUsageResponse.Gpu objects representing the hierarchical organization of GPU usage,
     *         with each GPU containing instance types, regions, and clusters with their usage statistics
     */
    private List<GpuUsageResponse.Gpu> processInstances(
            @NotNull Map<String, List<InstanceV2Entity>> requestIdToInstances,
            @NotNull Set<ReadyClusterInfo> clusterInfoMap) {

        List<GpuUsageResponse.Gpu> result = new ArrayList<>();

        for(List<InstanceV2Entity> instances : requestIdToInstances.values()) {
            if (instances == null || instances.isEmpty()) {
                continue;
            }

            for(InstanceV2Entity instanceV2Entity: instances) {
                if (instanceV2Entity == null) {
                    continue;
                }

                GpuUsageResponse.Gpu gpuInResponse = findGpuInResponse(instanceV2Entity.getGpu(),result);
                GpuUsageResponse.Instance instanceInResponse = findInstanceInResponse(instanceV2Entity.getInstanceType(), gpuInResponse.getInstances());

                ReadyClusterInfo readyClusterInfo = clusterInfoMap.stream()
                        .filter(r -> r.getClusterId().equals(instanceV2Entity.getZone())).findFirst()
                        .orElse(null);

                if (instanceV2Entity.getRegion() == null) {
                    instanceV2Entity.setRegion(readyClusterInfo != null ? readyClusterInfo.getRegion() : "UNKNOWN");
                }

                GpuUsageResponse.Region regionInResponse = findRegionInResponse(instanceV2Entity.getRegion(), instanceInResponse.getRegions());

                String nonByocClusterId;
                // For all non-BYOC clusters in that region has to be combined under artificial name
                // <regionName>-<computePlatform>, where the suffix is the matched compute-platform
                // provider, until we find a way to report them separately
                if (computePlatformService.isComputePlatformProvider(instanceV2Entity.getResourceProvider())) {
                    nonByocClusterId = instanceV2Entity.getRegion() + "-" + instanceV2Entity.getResourceProvider();
                } else {
                    nonByocClusterId = instanceV2Entity.getZone();
                }

                GpuUsageResponse.Cluster clusterInResponse = findClusterInResponse(nonByocClusterId,
                                                                                    readyClusterInfo != null ? readyClusterInfo.getClusterGroupName() : "UNKNOWN",
                                                                                    regionInResponse.getClusters());

                if (instanceV2Entity.getInstanceStateName() == SpotInstanceInternalState.RUNNING) {
                    clusterInResponse.getStatus().setActiveInstances(clusterInResponse.getStatus().getActiveInstances() + 1);
                }
                else if (instanceV2Entity.getInstanceStateName() == SpotInstanceInternalState.STARTING) {
                    clusterInResponse.getStatus().setPendingInstances(clusterInResponse.getStatus().getPendingInstances() + 1);
                }
            }
        }

        return result;
    }
}
