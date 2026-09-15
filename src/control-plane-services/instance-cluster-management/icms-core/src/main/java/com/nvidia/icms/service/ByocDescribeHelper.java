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
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static com.nvidia.icms.service.telemetry.model.Events.CLUSTER_INFO_NOT_FOUND_FOR_REQUEST_ID;

@Service
@Slf4j
@RequiredArgsConstructor
public class ByocDescribeHelper {

    private final ClusterRepository clusterRepository;
    private final TelemetryEventClient telemetryEventClient;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    public Optional<ZoneInfo> resolveOciZoneInfo(InstanceV2Entity entity) {
        return Optional.of(ZoneInfo.builder()
                .cloudProvider(CloudProvider.OCI)
                .zoneName(entity.getZone())
                .build());
    }

    public Optional<ZoneInfo> resolveByocZoneInfo(InstanceV2Entity entity,
                                                   Map<String, ClusterEntity> clustersCache) {
        ClusterEntity clusterEntity;
        if (clustersCache.containsKey(entity.getZone())) {
            clusterEntity = clustersCache.get(entity.getZone());
        } else {
            clusterEntity = clusterRepository.getClusterInfoByClusterId(entity.getZone(), true)
                    .orElse(null);
            clustersCache.put(entity.getZone(), clusterEntity);
        }

        if (clusterEntity != null) {
            return Optional.of(ZoneInfo.builder()
                    .cloudProvider(CloudProvider.getCloudProviderFromClusterProvider(
                            clusterEntity.getClusterProvider()))
                    .zoneName(clusterEntity.getClusterName())
                    .build());
        }

        String errMsg = String.format(
                "%s instance having BYOC resource provider but can not find cluster info for %s cluster-id",
                entity.getInstanceId(), entity.getZone());
        log.error(errMsg);
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withEventName(CLUSTER_INFO_NOT_FOUND_FOR_REQUEST_ID.toString())
                .withError(errMsg)
                .withResourceProvider(ResourceProvider.BYOC)
                .withZoneName(entity.getZone())
                .withRequestId(entity.getRequestId())
                .withInstanceId(entity.getInstanceId())));
        return Optional.empty();
    }

    public boolean isByocBatchExpired(ResourceProvider resourceProvider,
                                        SqsMessageEntity sqsMessageEntity,
                                        int requestCancelDurationInMin) {
        return icmsConfigurationProperties.isMessageBatchIdExpiryValidationInGet()
                && resourceProvider == ResourceProvider.BYOC
                && sqsMessageEntity.getCreationTime() != null
                && sqsMessageEntity.getCreationTime()
                .isBefore(Instant.now().minus(requestCancelDurationInMin, ChronoUnit.MINUTES));
    }


    public int getByocValidationDurationWithModel() {
        return icmsConfigurationProperties.getMessageBatchIdConfig()
                .getValidationDurationForByocWithModelInMin();
    }

    public int getByocValidationDurationWithoutModel() {
        return icmsConfigurationProperties.getMessageBatchIdConfig()
                .getValidationDurationForByocWithoutModelInMin();
    }
}
