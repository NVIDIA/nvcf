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

import static com.nvidia.icms.service.telemetry.model.Events.NVCA_HEARTBEAT_EVENT;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatRequest.NvcaClusterCapacityStats;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatResponse;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaHeartbeatActionResponse;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.ByocService;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.vdurmont.semver4j.Semver;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Map.Entry;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class NvcaHeartbeatService extends HeartbeatBasicService<NvcaClusterHeartbeatRequest, NvcaClusterCapacityStats, NvcaClusterHeartbeatResponse> {

    private final ByocConfigurationProperties byocConfigurationProperties;
    private final NvcaConfigurationProperties nvcaConfigurationProperties;

    public NvcaHeartbeatService(
            CloudHealthService cloudHealthService,
            ByocConfigurationProperties byocConfigurationProperties,
            ByocService byocService,
            TelemetryEventClient telemetryEventClient,
            ObjectMapper objectMapper,
            ClusterRepository clusterRepository,
            NvcaClusterRepository nvcaClusterRepository,
            NvcaConfigurationProperties nvcaConfigurationProperties,
            ComputePlatformService computePlatformService) {
        super(cloudHealthService, telemetryEventClient, objectMapper, clusterRepository, nvcaClusterRepository,
                computePlatformService);
        this.byocConfigurationProperties = byocConfigurationProperties;
        this.nvcaConfigurationProperties = nvcaConfigurationProperties;
    }

    /**
     * NVCA-specific heartbeat method that returns a response
     * This method is used by the NVCA controller
     */
    @Override
    @Observed
    public NvcaClusterHeartbeatResponse recordClusterHeartbeat(
            @NotNull String clusterId, @NotNull NvcaClusterHeartbeatRequest heartbeatRequest) {

        ClusterEntity clusterEntity = getClusterInfo(clusterId);
        recordHeartbeat(clusterId, heartbeatRequest, ResourceProvider.BYOC,
                        heartbeatRequest.getStatus(), heartbeatRequest.getUpgradeStatus(),
                        NVCA_HEARTBEAT_EVENT.toString(),
                        byocConfigurationProperties.getCloudHealthTtlForByocInSec(), clusterEntity);

        recordLastHealthyHeartbeatReportTime(clusterEntity, heartbeatRequest.getStatus());
        
        return validateNvcaVersionAndReturnResponse(clusterId, heartbeatRequest);
    }

    private NvcaClusterHeartbeatResponse validateNvcaVersionAndReturnResponse(String clusterId, NvcaClusterHeartbeatRequest heartbeatRequest) {

        // If feature is not enabled then we will return "ACCEPTED" for all heartbeat request
        if (!nvcaConfigurationProperties.isNvcaSelfDestructEnabled()) {
            return new NvcaClusterHeartbeatResponse(NvcaHeartbeatActionResponse.ACCEPTED);
        }

        try {
            Semver minSupportedVersion = new Semver(
                    nvcaConfigurationProperties.getNvcaSelfDestructMinVersion(), Semver.SemverType.NPM);
            String nvcaAgentVersion = heartbeatRequest.getNvcaAgentVersion();

            // If no agent version is provided, return ACCEPTED
            if (StringUtils.isBlank(nvcaAgentVersion)) {
                log.warn("validateNvcaVersionAndReturnResponse: NVCA agent version is not provided for clusterId: {}, returning ACCEPTED", clusterId);

                sendNvcaSelfDestructEvent(clusterId, heartbeatRequest, NvcaHeartbeatActionResponse.ACCEPTED, "NVCA agent version is not provided");
                return new NvcaClusterHeartbeatResponse(NvcaHeartbeatActionResponse.ACCEPTED);
            }

            // Parse the version using semver - it automatically handles dev build suffixes
            // For example: "2.47.3-v4a667381" will be parsed as "2.47.3"
            // For example: "2" will be parsed as "2.0.0"
            Semver agentVersion = new Semver(nvcaAgentVersion, Semver.SemverType.NPM);
            // Compare versions
            if (agentVersion.isLowerThan(minSupportedVersion)) {
                log.info("validateNvcaVersionAndReturnResponse: NVCA agent version '{}' " +
                                "is lower than minimum supported version '{}' for clusterId: {}, returning SELF_DESTRUCT",
                        nvcaAgentVersion, minSupportedVersion.getValue(), clusterId);

                sendNvcaSelfDestructEvent(clusterId, heartbeatRequest, NvcaHeartbeatActionResponse.SELF_DESTRUCT, null);
                return new NvcaClusterHeartbeatResponse(NvcaHeartbeatActionResponse.SELF_DESTRUCT);
            }

        } catch (Exception e) {
            // Suppressing the error and returning ACCEPTED response
            log.error("validateNvcaVersionAndReturnResponse: Failed to parse NVCA agent version '{}' " +
                            "for clusterId: {}, returning ACCEPTED. Error: {}, Exception: ",
                    heartbeatRequest.getNvcaAgentVersion(), clusterId, e.getMessage(), e);

            sendNvcaSelfDestructEvent(clusterId, heartbeatRequest, NvcaHeartbeatActionResponse.ACCEPTED, e.getMessage());
        }

        /* Returning accepted as default
        1. When error occurred while parsing the version
        2. When version is supported
         */
        return new NvcaClusterHeartbeatResponse(NvcaHeartbeatActionResponse.ACCEPTED);
    }


    public @NotNull Map<String, GpuCapacity> toGpuCapacityMap(
            @NotNull NvcaClusterHeartbeatRequest heartbeatRequest) {
        Map<String, NvcaClusterCapacityStats> gpuUsage = heartbeatRequest.getGpuUsage();
        return gpuUsage.entrySet().stream()
                .collect(Collectors.toMap(Entry::getKey, entry -> GpuCapacity.builder()
                        .capacity(entry.getValue().getCapacity())
                        .allocated(entry.getValue().getAllocated())
                        .available(entry.getValue().getAvailable()).build()));
    }

    @Override
    public @NotNull Map<String, NvcaClusterCapacityStats> getCapacityStats(
            @NotNull NvcaClusterHeartbeatRequest heartbeatRequest) {
        return heartbeatRequest.getGpuUsage();
    }


    public @NotNull Map<String, Object> createMetadataForEvent(
            @NotNull NvcaClusterHeartbeatRequest heartbeatRequest) {
        Map<String, Object> metadata = new HashMap<>();

        metadata.put(EventMetaData.CLUSTER_STATUS.getName(),
                     heartbeatRequest.getStatus().toString());
        metadata.put(EventMetaData.CLUSTER_UPGRADE_STATUS.getName(),
                     heartbeatRequest.getUpgradeStatus());

        String gpuUsage = getGpuUsageAsString(heartbeatRequest);
        if (gpuUsage != null) {
            metadata.put(EventMetaData.GPU_USAGE.getName(), gpuUsage);
        }

        if (StringUtils.isNotBlank(heartbeatRequest.getClusterOwnerNcaId())) {
            metadata.put(EventMetaData.CLUSTER_OWNER_NCA_ID.getName(),
                         heartbeatRequest.getClusterOwnerNcaId());
        }

        if (StringUtils.isNotBlank(heartbeatRequest.getNvcaAgentVersion())) {
            metadata.put(EventMetaData.NVCA_AGENT_VERSION.getName(), heartbeatRequest.getNvcaAgentVersion());
        }

        if (StringUtils.isNotBlank(heartbeatRequest.getNvcaOperatorVersion())) {
            metadata.put(EventMetaData.NVCA_OPERATOR_VERSION.getName(),
                         heartbeatRequest.getNvcaOperatorVersion());
        }

        if (StringUtils.isNotBlank(heartbeatRequest.getClusterName())) {
            metadata.put(EventMetaData.CLUSTER_NAME.getName(), heartbeatRequest.getClusterName());
        }

        return metadata;
    }

    private void sendNvcaSelfDestructEvent(
            @NotNull String clusterId,
            @NotNull NvcaClusterHeartbeatRequest heartbeatRequest,
            NvcaHeartbeatActionResponse nvcaHeartbeatActionResponse,
            @Nullable String error) {
        Map<String, Object> metadata = new HashMap<>();
        metadata.put(EventMetaData.NVCA_AGENT_VERSION.getName(), heartbeatRequest.getNvcaAgentVersion());
        metadata.put(EventMetaData.HEARTBEAT_REQUEST_BODY.getName(), getHeartbeatRequestAsString(heartbeatRequest));
        metadata.put(EventMetaData.NVCA_HEARTBEAT_ACTION_RESPONSE.getName(), nvcaHeartbeatActionResponse.name());

        String eventName = StringUtils.isEmpty(error) ? Events.NVCA_VERSION_SELF_DESTRUCTED.toString()
                : Events.NVCA_VERSION_SELF_DESTRUCTION_VALIDATION_FAILED.toString();

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withMetadata(metadata)
                .withClusterId(clusterId)
                .withEventName(eventName)));
    }
}
