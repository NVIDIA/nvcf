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
package com.nvidia.icms.service.byoc;

import static com.nvidia.icms.service.telemetry.model.Events.BART_HEARTBEAT_EVENT;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudHealthUpdateRequest;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterHeartbeatRequest;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterHeartbeatStatus;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;


@Service
@Slf4j
@AllArgsConstructor
public class ClusterHealthService {

    private final CloudHealthService cloudHealthService;

    private final ByocConfigurationProperties byocConfigurationProperties;

    private final TelemetryEventClient telemetryEventClient;

    private final ClusterRepository clusterRepository;

    @Observed
    public void registerClusterHeartbeat(
            ClusterHeartbeatRequest heartbeatRequest,
            String clusterId) {

        CloudHealthUpdateRequest cloudHealthUpdateRequest =
                convertToCloudHealthUpdateRequest(heartbeatRequest);

        cloudHealthService.updateCloudHealthStatus(ResourceProvider.BYOC, clusterId,
                                                   cloudHealthUpdateRequest,
                                                   byocConfigurationProperties.getCloudHealthTtlForByocInSec());

        sendHeartBeatEvent(clusterId, cloudHealthUpdateRequest.getStatus());
    }


    private CloudHealthUpdateRequest convertToCloudHealthUpdateRequest(
            ClusterHeartbeatRequest clusterHeartbeatRequest) {

        CloudHealthStatus cloudHealthStatus = CloudHealthStatus.UNHEALTHY;
        if (clusterHeartbeatRequest.getStatus().equals(ClusterHeartbeatStatus.HEALTHY)) {
            cloudHealthStatus = CloudHealthStatus.HEALTHY;
        }
        return new CloudHealthUpdateRequest(cloudHealthStatus);
    }

    private void sendHeartBeatEvent(String clientId, CloudHealthStatus status) {
        Map<String, Object> metadata = new HashMap<>();
        metadata.put(TelemetryEventClient.EventMetaData.CLUSTER_STATUS.getName(),
                     status.toString());

        telemetryEventClient.triggerEvent(
                List.of(new GenericMetric().withEventName(BART_HEARTBEAT_EVENT.toString())
                                .withClusterId(clientId)
                        .withCloudProvider(getCloudProviderProviderForClientId(clientId))
                        .withResourceProvider(ResourceProvider.BYOC).withMetadata(metadata)));
    }

    private CloudProvider getCloudProviderProviderForClientId(String clusterId) {
        Optional<ClusterEntity> clusterEntityOptional =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        if (clusterEntityOptional.isPresent()) {
            return CloudProvider.getCloudProviderFromClusterProvider(clusterEntityOptional.get()
                                                                             .getClusterProvider());
        }
        log.info("Received heartbeat from client with id {} but cluster info is not present for it",
                 clusterId);
        return null;
    }
}
