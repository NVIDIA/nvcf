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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageKey;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;


import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

@ExtendWith(MockitoExtension.class)
class ByocDescribeHelperTest {

    private static final String CLUSTER_ID = "cluster-id";
    private static final String CLUSTER_NAME = "cluster-name";
    private static final String DUMMY_ZONE = "us-east-1a";
    private static final int VALIDATION_DURATION_MIN = 30;

    @Mock private ClusterRepository clusterRepository;
    @Mock private TelemetryEventClient telemetryEventClient;
    @Mock private IcmsConfigurationProperties icmsConfigurationProperties;

    @Captor private ArgumentCaptor<List<GenericMetric>> eventCaptor;

    private ByocDescribeHelper helper;

    @BeforeEach
    void setUp() {
        helper = new ByocDescribeHelper(clusterRepository, telemetryEventClient, icmsConfigurationProperties);
    }

    // -------------------------------------------------------------------------
    // resolveOciZoneInfo
    // -------------------------------------------------------------------------

    @Test
    void resolveOciZoneInfo_returnsOciCloudProviderWithEntityZone() {
        InstanceV2Entity entity = InstanceV2Entity.getEmptyEntity();
        entity.setZone(DUMMY_ZONE);

        Optional<ZoneInfo> result = helper.resolveOciZoneInfo(entity);

        assertTrue(result.isPresent());
        assertEquals(CloudProvider.OCI, result.get().getCloudProvider());
        assertEquals(DUMMY_ZONE, result.get().getZoneName());
    }

    @Test
    void resolveOciZoneInfo_whenZoneIsNull_returnsZoneInfoWithNullZoneName() {
        InstanceV2Entity entity = InstanceV2Entity.getEmptyEntity();
        entity.setZone(null);

        Optional<ZoneInfo> result = helper.resolveOciZoneInfo(entity);

        assertTrue(result.isPresent());
        assertEquals(CloudProvider.OCI, result.get().getCloudProvider());
        assertNull(result.get().getZoneName());
    }

    @Test
    void resolveOciZoneInfo_whenZoneIsEmpty_returnsZoneInfoWithEmptyZoneName() {
        InstanceV2Entity entity = InstanceV2Entity.getEmptyEntity();
        entity.setZone("");

        Optional<ZoneInfo> result = helper.resolveOciZoneInfo(entity);

        assertTrue(result.isPresent());
        assertEquals(CloudProvider.OCI, result.get().getCloudProvider());
        assertEquals("", result.get().getZoneName());
    }

    // -------------------------------------------------------------------------
    // resolveByocZoneInfo — cluster found
    // -------------------------------------------------------------------------

    @Test
    void resolveByocZoneInfo_whenClusterFound_returnsZoneInfoWithClusterNameAndMappedProvider() {
        ClusterEntity cluster = ClusterEntity.builder()
                .clusterId(CLUSTER_ID)
                .clusterName(CLUSTER_NAME)
                .clusterProvider(ClusterProviderEnum.AWS)
                .clusterStatus(ClusterStatusEnum.READY)
                .build();
        when(clusterRepository.getClusterInfoByClusterId(CLUSTER_ID, true))
                .thenReturn(Optional.of(cluster));

        InstanceV2Entity entity = InstanceV2Entity.getEmptyEntity();
        entity.setZone(CLUSTER_ID);
        entity.setInstanceId("instance-id");

        Map<String, ClusterEntity> cache = new HashMap<>();
        Optional<ZoneInfo> result = helper.resolveByocZoneInfo(entity, cache);

        assertTrue(result.isPresent());
        assertEquals(CLUSTER_NAME, result.get().getZoneName());
        assertEquals(CloudProvider.AWS, result.get().getCloudProvider());
    }

    @Test
    void resolveByocZoneInfo_usesClustersCacheOnSecondCall() {
        ClusterEntity cluster = ClusterEntity.builder()
                .clusterId(CLUSTER_ID)
                .clusterName(CLUSTER_NAME)
                .clusterProvider(ClusterProviderEnum.AWS)
                .clusterStatus(ClusterStatusEnum.READY)
                .build();
        when(clusterRepository.getClusterInfoByClusterId(CLUSTER_ID, true))
                .thenReturn(Optional.of(cluster));

        InstanceV2Entity entity = InstanceV2Entity.getEmptyEntity();
        entity.setZone(CLUSTER_ID);
        entity.setInstanceId("instance-id");

        Map<String, ClusterEntity> cache = new HashMap<>();
        helper.resolveByocZoneInfo(entity, cache);

        // After the first call the cluster must be present in the cache
        assertTrue(cache.containsKey(CLUSTER_ID));
        assertEquals(cluster, cache.get(CLUSTER_ID));

        helper.resolveByocZoneInfo(entity, cache); // second call uses cache

        verify(clusterRepository).getClusterInfoByClusterId(CLUSTER_ID, true); // called only once
    }

    // -------------------------------------------------------------------------
    // resolveByocZoneInfo — cluster not found
    // -------------------------------------------------------------------------

    @Test
    void resolveByocZoneInfo_whenClusterNotFound_returnsEmptyAndFiresTelemetry() {
        when(clusterRepository.getClusterInfoByClusterId(CLUSTER_ID, true))
                .thenReturn(Optional.empty());

        InstanceV2Entity entity = InstanceV2Entity.getEmptyEntity();
        entity.setZone(CLUSTER_ID);
        entity.setInstanceId("instance-id");
        entity.setRequestId("request-id");

        Map<String, ClusterEntity> cache = new HashMap<>();
        Optional<ZoneInfo> result = helper.resolveByocZoneInfo(entity, cache);

        assertTrue(result.isEmpty());
        verify(telemetryEventClient).triggerEvent(eventCaptor.capture());
        List<GenericMetric> capturedEvents = eventCaptor.getValue();
        assertEquals(1, capturedEvents.size());
        GenericMetric event = capturedEvents.get(0);
        assertEquals(Events.CLUSTER_INFO_NOT_FOUND_FOR_REQUEST_ID.toString(), event.getEventName());
        assertEquals(CLUSTER_ID, event.getZoneName());
        assertEquals("request-id", event.getRequestId());
        assertEquals("instance-id", event.getInstanceId());
    }

    // -------------------------------------------------------------------------
    // isByocBatchExpired
    // -------------------------------------------------------------------------

    @Test
    void isByocBatchExpired_whenValidationDisabled_returnsFalse() {
        when(icmsConfigurationProperties.isMessageBatchIdExpiryValidationInGet()).thenReturn(false);
        SqsMessageEntity msg = buildOldMessage();

        assertFalse(helper.isByocBatchExpired(ResourceProvider.BYOC, msg, VALIDATION_DURATION_MIN));
    }

    @Test
    void isByocBatchExpired_whenResourceProviderIsNotByoc_returnsFalse() {
        when(icmsConfigurationProperties.isMessageBatchIdExpiryValidationInGet()).thenReturn(true);
        SqsMessageEntity msg = buildOldMessage();

        assertFalse(helper.isByocBatchExpired(ResourceProvider.OCI, msg, VALIDATION_DURATION_MIN));
    }

    @Test
    void isByocBatchExpired_whenBatchIsRecent_returnsFalse() {
        when(icmsConfigurationProperties.isMessageBatchIdExpiryValidationInGet()).thenReturn(true);
        SqsMessageEntity msg = buildMessageWithAge(1); // 1 minute old — within 30-min window

        assertFalse(helper.isByocBatchExpired(ResourceProvider.BYOC, msg, VALIDATION_DURATION_MIN));
    }

    @Test
    void isByocBatchExpired_whenValidationEnabledAndBatchExpired_returnsTrue() {
        when(icmsConfigurationProperties.isMessageBatchIdExpiryValidationInGet()).thenReturn(true);
        SqsMessageEntity msg = buildOldMessage(); // 60 minutes old — beyond 30-min window

        assertTrue(helper.isByocBatchExpired(ResourceProvider.BYOC, msg, VALIDATION_DURATION_MIN));
    }

    // -------------------------------------------------------------------------
    // getByocValidationDuration*
    // -------------------------------------------------------------------------

    @Test
    void getByocValidationDurationWithModel_returnsConfigValue() {
        when(icmsConfigurationProperties.getMessageBatchIdConfig()).thenReturn(
                IcmsConfigurationProperties.MessageBatchIdConfig.builder()
                        .validationDurationForByocWithModelInMin(240)
                        .build());

        assertEquals(240, helper.getByocValidationDurationWithModel());
    }

    @Test
    void getByocValidationDurationWithoutModel_returnsConfigValue() {
        when(icmsConfigurationProperties.getMessageBatchIdConfig()).thenReturn(
                IcmsConfigurationProperties.MessageBatchIdConfig.builder()
                        .validationDurationForByocWithoutModelInMin(60)
                        .build());

        assertEquals(60, helper.getByocValidationDurationWithoutModel());
    }

    // -------------------------------------------------------------------------
    // helpers
    // -------------------------------------------------------------------------

    private SqsMessageEntity buildOldMessage() {
        return buildMessageWithAge(60);
    }

    private SqsMessageEntity buildMessageWithAge(int minutesOld) {
        return SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                        .requestId("req-id")
                        .messageBatchId("batch-id")
                        .build())
                .creationTime(Instant.now().minus(minutesOld, ChronoUnit.MINUTES))
                .build();
    }
}
