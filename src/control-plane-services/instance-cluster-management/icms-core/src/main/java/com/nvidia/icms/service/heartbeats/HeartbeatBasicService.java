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
package com.nvidia.icms.service.heartbeats;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;

import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import jakarta.annotation.Nullable;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
public abstract class HeartbeatBasicService<T, C, R> implements HeartbeatService<T, C, R> {

    private final CloudHealthService cloudHealthService;
    protected final TelemetryEventClient telemetryEventClient;
    private final ObjectMapper objectMapper;
    private final ClusterRepository clusterRepository;
    private final NvcaClusterRepository nvcaClusterRepository;

    private final ComputePlatformService computePlatformService;

    protected HeartbeatBasicService(CloudHealthService cloudHealthService,
                                    TelemetryEventClient telemetryEventClient,
                                    ObjectMapper objectMapper,
                                    ClusterRepository clusterRepository,
                                    NvcaClusterRepository nvcaClusterRepository,
                                    ComputePlatformService computePlatformService) {
        this.cloudHealthService = cloudHealthService;
        this.telemetryEventClient = telemetryEventClient;
        this.objectMapper = objectMapper;
        this.clusterRepository = clusterRepository;
        this.nvcaClusterRepository = nvcaClusterRepository;
        this.computePlatformService = computePlatformService;
    }

    public void recordHeartbeat(
            @NotNull String clusterId,
            @NotNull T heartbeatRequest,
            @NotNull ResourceProvider resourceProvider,
            @NotNull CloudHealthStatus cloudHealthStatus,
            @Nullable String upgradeStatus,
            @NotNull String heartbeatEvent,
            int ttl,
            @NotNull ClusterEntity clusterEntity) {

        logHeartbeatReceived(heartbeatRequest, clusterId, resourceProvider);
        CloudProvider cloudProvider = CloudProvider.getCloudProviderFromClusterProvider(clusterEntity.getClusterProvider());
        cloudHealthService.updateCloudHealthStatus(resourceProvider,
                                                   clusterId,
                                                   cloudHealthStatus,
                                                   upgradeStatus,
                                                   toGpuCapacityMap(heartbeatRequest),
                                                   ttl);

        sendHeartbeatEvent(clusterId, cloudProvider, heartbeatEvent,
                           createMetadataForEvent(heartbeatRequest));
    }

    public void sendHeartbeatEvent(
            @NotNull String clusterId,
            @NotNull CloudProvider cloudProvider,
            String eventName,
            @NotNull Map<String, Object> metadata) {

        ResourceProvider resourceProvider = computePlatformService.resourceProviderFor(cloudProvider)
                .orElse(ResourceProvider.BYOC);

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withEventName(eventName)
                                                           .withCloudProvider(cloudProvider)
                                                           .withResourceProvider(resourceProvider)
                                                           .withClusterId(clusterId)
                                                           .withMetadata(metadata)));
    }

    public void logHeartbeatReceived(
            @NotNull T heartbeatRequest, @NotNull String cluster,
            @NotNull ResourceProvider resourceProvider) {
        try {
            log.info(
                    "HEART_BEAT_LOGGING: Received {} heartbeat from {} cluster, heartBeatRequest {}",
                    cluster,
                    resourceProvider, objectMapper.writeValueAsString(heartbeatRequest));
        } catch (Exception exception) {
            log.error(
                    "HEART_BEAT_LOGGING: Failed to log heartbeat received from {}, error: {}, exception: ",
                    resourceProvider, exception.getMessage(), exception);
        }
    }

    @Nullable
    public String getGpuUsageAsString(@NotNull T heartbeatRequest) {
        try {
            Map<String, C> gpuUsage = getCapacityStats(heartbeatRequest);
            if (gpuUsage != null && !gpuUsage.isEmpty()) {
                return objectMapper.writeValueAsString(gpuUsage);
            }
        } catch (Exception exception) {
            log.error(
                    "Failed to convert gpuUsage to string, error: {}, exception: ",
                    exception.getMessage(), exception);
        }

        return null;
    }

    @Nullable
    public String getHeartbeatRequestAsString(@NotNull T heartbeatRequest) {
        try {
            return objectMapper.writeValueAsString(heartbeatRequest);
        } catch (Exception exception) {
            log.error(
                    "Failed to convert heartbeat request to string, error: {}, exception: ",
                    exception.getMessage(), exception);
        }
        return null;
    }

    @Observed
    public void recordLastHealthyHeartbeatReportTime(@NotNull ClusterEntity clusterEntity,
                                                     @NotNull CloudHealthStatus cloudHealthStatus) {

        try {
            // If health status is healthy then only updating in DB
            if (cloudHealthStatus == CloudHealthStatus.HEALTHY) {
                clusterEntity.setHealthyHeartbeatReportTime(Instant.now());

                // Updating clusterEntity in DB
                nvcaClusterRepository.updateClusterEntity(clusterEntity);
                log.info("Class: HeartbeatBasicService, Function: recordLastHealthyHeartbeatReportTime, Updated healthyHeartbeatReportTime in DB for clusterId {}", clusterEntity.getClusterId());
            }
        } catch (Exception exception) {
            log.error("Class: HeartbeatBasicService, Function: recordLastHealthyHeartbeatReportTime, failed to update healthyHeartbeatTime in clusterEntity, clusterId: {}, error: {}, exception: ",
                    clusterEntity.getClusterId(), exception.getMessage(), exception);

            throw exception;
        }
    }

    public ClusterEntity getClusterInfo(String clusterId) {
        Optional<ClusterEntity> optionalClusterInfo = clusterRepository.getClusterInfoByClusterId(clusterId, true);

        if (optionalClusterInfo.isEmpty()) {
            String errMsg = String.format("Cluster with clusterId %s does not exist", clusterId);
            log.error("HeartbeatBasicService: Failed to get cluster info from DB for heartbeat update, error: {}",
                      errMsg);
            throw new IcmsNotFoundException(errMsg);
        }
        return optionalClusterInfo.get();
    }
}
