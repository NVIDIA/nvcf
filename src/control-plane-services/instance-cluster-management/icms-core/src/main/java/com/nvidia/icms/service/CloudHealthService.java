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
package com.nvidia.icms.service;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.service.extensions.api.CloudHealthEventService;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudHealthUpdateRequest;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthKey;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.metrics.ClusterGpuUsageMetricsService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.Optional;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@AllArgsConstructor
@Slf4j
public class CloudHealthService {

    private final CloudHealthRepository cloudHealthRepository;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final CloudHealthEventService cloudHealthEventService;
    private final ClusterGpuUsageMetricsService clusterGpuUsageMetricsService;
    private final ClusterRepository clusterRepository;
    private final ComputePlatformService computePlatformService;

    public static void logIfStatusIsNull(
            CloudHealthEntity cloudHealth, String functionName, String clusterId) {
        if (cloudHealth == null) {
            log.info(
                    "CLOUD_HEALTH_STATUS_LOGGING: function: {}, clusterId: {}, Received CloudHealthEntity as NULL from DB",
                    functionName, clusterId);
            return;
        }

        if (cloudHealth.getStatus() == null) {
            log.info(
                    "CLOUD_HEALTH_STATUS_LOGGING: function: {}, clusterId: {}, cloudHealthEntity {}, Received status as NULL from DB",
                    functionName, clusterId, cloudHealth);
        }
    }

    @Observed
    public void updateCloudHealthStatus(
            ResourceProvider resourceProvider, String zone,
            @NotNull CloudHealthStatus cloudHealthStatus,
            @Nullable String clusterUpgradeStatus,
            Map<String, GpuCapacity> gpuUsage, int ttl) {

        CloudHealthEntity entity = CloudHealthEntity.builder()
                .key(CloudHealthKey.builder()
                             .cloudProvider(resourceProvider)
                             .zone(zone)
                             .build())
                .status(cloudHealthStatus)
                .clusterUpgradeStatus(clusterUpgradeStatus)
                .gpuUsage(gpuUsage)
                .build();

        if (cloudHealthStatus == CloudHealthStatus.UNHEALTHY) {
            log.info("Cloud {} for resource provider {} is received as unhealthy in the request",
                     zone, resourceProvider);

            // Send event for unhealthy non-BYOC cloud event
            if (computePlatformService.isComputePlatformProvider(entity.getKey().getCloudProvider())) {
                cloudHealthEventService.handleUnhealthyCloud(entity);
            }

            Optional<CloudHealthEntity> existingEntityOptional =
                    getCloudHealth(resourceProvider, zone);
            if (existingEntityOptional.isPresent()) {
                CloudHealthEntity existingEntity = existingEntityOptional.get();
                // if cloud is healthy then update it to unhealthy but if it is
                // already updated as unhealthy, then don't update it
                logIfStatusIsNull(existingEntity, "updateCloudHealthStatus", zone);

                if (existingEntity.getStatus() != null
                        && existingEntity.getStatus() == CloudHealthStatus.HEALTHY) {
                    cloudHealthRepository.insert(entity,
                                                 icmsConfigurationProperties.getTtlToMarkCloudUnhealthy());
                }
            }
        } else {
            cloudHealthRepository.insert(entity, ttl);
        }

        // Send gpu usage metrics
        sendGpuUsageMetrics(zone, gpuUsage);

    }

    public void updateCloudHealthStatus(
            ResourceProvider resourceProvider, String zone,
            CloudHealthUpdateRequest updateRequest, int ttl) {

        updateCloudHealthStatus(resourceProvider, zone, updateRequest.getStatus(), null, null, ttl);
    }

    public Optional<CloudHealthEntity> getCloudHealth(
            ResourceProvider resourceProvider,
            String zone) {
        return cloudHealthRepository.findByCloudAndZone(resourceProvider, zone);
    }

    private void sendGpuUsageMetrics(String clusterId, Map<String, GpuCapacity> gpuUsage) {
        // Fetch cluster info
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, true);
        if (optionalClusterEntity.isPresent()) {
            ClusterEntity clusterEntity = optionalClusterEntity.get();

            // Send gpu capacity metrics
            for (Map.Entry<String, GpuCapacity> entry : gpuUsage.entrySet()) {
                clusterGpuUsageMetricsService.recordTotalGpus(clusterEntity.getClusterName(), entry.getKey(),
                                                              entry.getValue().getCapacity(), clusterEntity);
                clusterGpuUsageMetricsService.recordAvailableGpus(clusterEntity.getClusterName(), entry.getKey(),
                                                                  entry.getValue().getAvailable(), clusterEntity);
                clusterGpuUsageMetricsService.recordOccupiedGpus(clusterEntity.getClusterName(), entry.getKey(),
                                                                 entry.getValue().getAllocated(), clusterEntity);
            }
        } else {
            log.debug("Couldn't find clusterInfo for {} clusterId", clusterId);
        }
    }
}
