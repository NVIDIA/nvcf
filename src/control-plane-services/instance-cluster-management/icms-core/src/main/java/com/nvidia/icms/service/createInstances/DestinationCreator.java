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
package com.nvidia.icms.service.createInstances;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.account.GpuUsageFilter;
import com.nvidia.icms.service.byoc.ByocValidationService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

import java.util.ArrayList;
import java.util.List;
import java.util.Set;

/**
 * Shared factory responsible for assembling {@link RequestInstanceDestination} objects.
 * Used by both the non-BYOC and BYOC destination providers.
 */
@Service
@Slf4j
@AllArgsConstructor
public class DestinationCreator {

    private final ByocValidationService byocValidationService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final ByocConfigurationProperties byocConfigurationProperties;
    private final ComputePlatformService computePlatformService;

    /**
     * Iterates over every GPU / instance-type combination in the given cluster data and
     * creates one {@link RequestInstanceDestination} per valid combination.
     */
    public @NotNull List<RequestInstanceDestination> addAvailableDestinations(
            @NotNull DestinationClusterData destinationClusterData,
            @NotNull GpuUsageFilter filter,
            @Nullable CloudProvider cloudProvider,
            @NotNull Integer instanceCount) {

        List<RequestInstanceDestination> result = new ArrayList<>();

        if (cloudProvider != null && filter.isClusterGroupNameAllowed(destinationClusterData.getClusterGroupName())) {
            Set<GpuV5Udt> gpuV5s = destinationClusterData.getGpusV5();
            for (GpuV5Udt gpuV5 : (gpuV5s != null ? gpuV5s : Set.<GpuV5Udt>of())) {
                if (filter.isGpuNameAllowed(gpuV5.getName())) {
                    String creationQueueUrl =
                            destinationClusterData.getQueueUrlByGpuNameMap().get(gpuV5.getName());
                    // Skip only when the map has no entry (filter rejected the GPU). An entry
                    // that is an empty string is a valid NATS-mode signal from
                    // ByocValidationService.getByocCreationQueueUrlForGpu (no SQS URL to route to,
                    // workloads dispatch via NATS streams instead). Treating blank as missing
                    // here causes every NATS-enabled (self-hosted NVCF) deployment to fail
                    // scheduling with "There are no available clusters".
                    if (creationQueueUrl == null) {
                        log.warn("DestinationCreator: No creation queue URL found for GPU {} in cluster {},"
                                         + " skipping destination",
                                 gpuV5.getName(), destinationClusterData.getClusterId());
                        continue;
                    }
                    for (InstanceTypeV5Udt instanceTypeV5 : gpuV5.getInstanceTypes()) {
                        if (filter.isInstanceTypeAllowed(instanceTypeV5.getName())) {
                            result.add(createDestination(
                                    destinationClusterData,
                                    instanceTypeV5,
                                    cloudProvider,
                                    gpuV5.getName(),
                                    creationQueueUrl,
                                    instanceCount));
                        }
                    }
                }
            }
        }

        return result;
    }

    /**
     * Builds a single {@link RequestInstanceDestination} from the supplied cluster and instance-type data.
     */
    public RequestInstanceDestination createDestination(
            @NotNull DestinationClusterData destinationClusterData,
            @NotNull InstanceTypeV5Udt instanceTypeV5,
            @NotNull CloudProvider cloudProvider,
            @NotNull String providedGpuName,
            @NotNull String creationQueueDefault,
            @NotNull Integer instanceCount) {

        InstanceTypeV5Udt instanceTypeV5Copy = new InstanceTypeV5Udt(instanceTypeV5);
        instanceTypeV5Copy.setGpuCount(byocValidationService.getRequiredGpuCountForInstance(
                instanceTypeV5Copy.getGpuCount()));

        // Determine the appropriate instance batch count based on destination type
        boolean isPlatformDestination = computePlatformService.isPlatformCluster(destinationClusterData.getClusterGroupName());
        Integer instanceBatchCount = isPlatformDestination
                ? icmsConfigurationProperties.getInstanceBatchCount()
                : byocConfigurationProperties.getInstanceBatchCount();

        return RequestInstanceDestination.builder()
                .clusterGroupId(destinationClusterData.getClusterGroupId())
                .clusterGroupName(destinationClusterData.getClusterGroupName())
                .creationQueueUrl(creationQueueDefault)
                .instanceType(instanceTypeV5Copy)
                .ncaId(destinationClusterData.getNcaId())
                .authorizedNcaIds(destinationClusterData.getAuthorizedNcaIds())
                .cloudProvider(cloudProvider)
                .clusterName(destinationClusterData.getClusterName())
                .clusterId(destinationClusterData.getClusterId())
                .region(destinationClusterData.getRegion())
                .gpuName(providedGpuName)
                .instanceBatchCount(instanceBatchCount)
                // For non-BYOC: capacityType will be overridden later based on reservation availability
                // For NVCA: capacityType will remain SPOT
                .capacityType(CapacityType.SPOT)
                // For non-BYOC destinations: initialize maxFulfillableInstances for reservation flow
                // For BYOC destinations: not used
                .maxFulfillableInstances(instanceCount)
                .build();
    }
}
