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

import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
class HeartbeatBasicServiceTest {

    @Mock
    private CloudHealthService cloudHealthService;
    @Mock
    private TelemetryEventClient telemetryEventClient;
    @Mock
    private ObjectMapper objectMapper;

    @Mock
    private ClusterRepository clusterRepository;
    @Mock
    private NvcaClusterRepository nvcaClusterRepository;

    @Captor
    private ArgumentCaptor<List<GenericMetric>> metricsCaptor;

    private TestHeartbeatService heartbeatService;

    // Concrete implementation for testing
    private static class TestHeartbeatService extends HeartbeatBasicService<TestHeartbeatRequest, Integer, String> {
        public TestHeartbeatService(CloudHealthService cloudHealthService,
                                    TelemetryEventClient telemetryEventClient,
                                    ObjectMapper objectMapper,
                                    ClusterRepository clusterRepository,
                                    NvcaClusterRepository nvcaClusterRepository,
                                    ComputePlatformService computePlatformService) {
            super(cloudHealthService, telemetryEventClient, objectMapper, clusterRepository, nvcaClusterRepository,
                    computePlatformService);
        }

        @Override
        public Map<String, Integer> getCapacityStats(TestHeartbeatRequest heartbeatRequest) {
            return heartbeatRequest.getGpuUsage();
        }

        @Override
        public Map<String, Object> createMetadataForEvent(TestHeartbeatRequest heartbeatRequest) {
            Map<String, Object> metadata = new HashMap<>();
            metadata.put("gpuUsage", heartbeatRequest.getGpuUsage());
            return metadata;
        }

        @Override
        public Map<String, GpuCapacity> toGpuCapacityMap(TestHeartbeatRequest heartbeatRequest) {
            Map<String, GpuCapacity> result = new HashMap<>();
            heartbeatRequest.getGpuUsage().forEach((key, value) -> 
                result.put(key, GpuCapacity.builder()
                    .capacity(value)
                    .allocated(0)
                    .available(value)
                    .build())
            );
            return result;
        }

        @Override
        public String recordClusterHeartbeat(String clusterId, TestHeartbeatRequest heartbeatRequest) {
            ClusterEntity clusterEntity = ClusterEntity.builder()
                    .clusterId(clusterId)
                    .clusterProvider(ClusterProviderEnum.AWS)
                    .build();
            recordHeartbeat(
                clusterId,
                heartbeatRequest,
                ResourceProvider.BYOC,
                CloudHealthStatus.HEALTHY,
                null,
                "cluster.heartbeat",
                300,
                clusterEntity
            );
            return "test-response"; // Return a test response
        }
    }

    // Test request class
    private static class TestHeartbeatRequest {
        private final Map<String, Integer> gpuUsage;

        public TestHeartbeatRequest(Map<String, Integer> gpuUsage) {
            this.gpuUsage = gpuUsage;
        }

        public Map<String, Integer> getGpuUsage() {
            return gpuUsage;
        }
    }

    @BeforeEach
    void setUp() {
        // computePlatformService is constructor-injected; pass a Non BYOC-configured instance
        // so classification preserves the pre-genericization behaviour.
        heartbeatService = new TestHeartbeatService(cloudHealthService, telemetryEventClient, objectMapper,
                clusterRepository, nvcaClusterRepository,
                ComputePlatformTestFixtures.nonByocComputePlatformService());
    }

    @Test
    void recordHeartbeat_ShouldProcessHeartbeatCorrectly()
            throws JacksonException {
        // Arrange
        String clusterId = "test-cluster";
        Map<String, Integer> gpuUsage = Map.of("gpu1", 80, "gpu2", 60);
        TestHeartbeatRequest request = new TestHeartbeatRequest(gpuUsage);
        String heartbeatEvent = "test.heartbeat";
        int ttl = 300;

        when(objectMapper.writeValueAsString(any())).thenReturn("{}");
        ClusterEntity clusterEntity = ClusterEntity.builder()
                .clusterId(clusterId)
                .clusterProvider(ClusterProviderEnum.AWS)
                .build();

        // Act
        heartbeatService.recordHeartbeat(
            clusterId,
            request,
            ResourceProvider.BYOC,
            CloudHealthStatus.HEALTHY,
            "UPGRADE_COMPLETE",
            heartbeatEvent,
            ttl,
            clusterEntity
        );

        // Assert
        verify(cloudHealthService).updateCloudHealthStatus(
            eq(ResourceProvider.BYOC),
            eq(clusterId),
            eq(CloudHealthStatus.HEALTHY),
            eq("UPGRADE_COMPLETE"),
            anyMap(),
            eq(ttl)
        );

        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());
        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        assertEquals(heartbeatEvent, metrics.get(0).getEventName());
        assertEquals(CloudProvider.AWS.toString(), metrics.get(0).getCloudProvider());
        assertEquals(ResourceProvider.BYOC.toString(), metrics.get(0).getResourceProvider());
    }

    @Test
    void sendHeartbeatEvent_WithNonByocProvider_ShouldSetCorrectResourceProvider() {
        // Arrange
        String clusterId = "test-cluster";
        String eventName = "test.event";
        Map<String, Object> metadata = new HashMap<>();
        metadata.put("key", "value");

        // Act
        heartbeatService.sendHeartbeatEvent(clusterId, CloudProvider.OCI, eventName, metadata);

        // Assert
        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());
        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertEquals(1, metrics.size());
        assertEquals(ResourceProvider.OCI.toString(), metrics.get(0).getResourceProvider());
        assertEquals(CloudProvider.OCI.toString(), metrics.get(0).getCloudProvider());
        assertEquals(eventName, metrics.get(0).getEventName());
    }

    @Test
    void getGpuUsageAsString_WithValidUsage_ShouldReturnJsonString()
            throws JacksonException {
        // Arrange
        Map<String, Integer> gpuUsage = Map.of("gpu1", 80);
        TestHeartbeatRequest request = new TestHeartbeatRequest(gpuUsage);
        String expectedJson = "{\"gpu1\":80}";
        when(objectMapper.writeValueAsString(gpuUsage)).thenReturn(expectedJson);

        // Act
        String result = heartbeatService.getGpuUsageAsString(request);

        // Assert
        assertEquals(expectedJson, result);
    }

    @Test
    void getGpuUsageAsString_WithEmptyUsage_ShouldReturnNull() {
        // Arrange
        TestHeartbeatRequest request = new TestHeartbeatRequest(Map.of());

        // Act
        String result = heartbeatService.getGpuUsageAsString(request);

        // Assert
        assertNull(result);
    }

    @Test
    void getClusterInfo_WhenPresent_ShouldReturnClusterEntity() {
        // Arrange
        String clusterId = "test-cluster";
        ClusterEntity clusterEntity = ClusterEntity.builder()
                .clusterId(clusterId)
                .clusterProvider(ClusterProviderEnum.AWS)
                .build();
        when(clusterRepository.getClusterInfoByClusterId(clusterId, true)).thenReturn(Optional.of(clusterEntity));

        // Act
        ClusterEntity result = heartbeatService.getClusterInfo(clusterId);

        // Assert
        assertEquals(clusterEntity, result);
        verify(clusterRepository).getClusterInfoByClusterId(clusterId, true);
    }

    @Test
    void getClusterInfo_WhenMissing_ShouldThrowSisNotFoundException() {
        // Arrange
        String clusterId = "missing-cluster";
        when(clusterRepository.getClusterInfoByClusterId(clusterId, true)).thenReturn(Optional.empty());

        // Act + Assert
        assertThrows(IcmsNotFoundException.class, () -> heartbeatService.getClusterInfo(clusterId));
        verify(clusterRepository).getClusterInfoByClusterId(clusterId, true);
    }

    @Test
    void recordLastHealthyHeartbeatReportTime_WhenHealthy_ShouldUpdateAndPersist() {
        // Arrange
        ClusterEntity clusterEntity = ClusterEntity.builder()
                .clusterId("test-cluster")
                .clusterProvider(ClusterProviderEnum.AWS)
                .build();
        assertNull(clusterEntity.getHealthyHeartbeatReportTime());

        // Act
        heartbeatService.recordLastHealthyHeartbeatReportTime(clusterEntity, CloudHealthStatus.HEALTHY);

        // Assert
        assertNotNull(clusterEntity.getHealthyHeartbeatReportTime());
        verify(nvcaClusterRepository).updateClusterEntity(clusterEntity);
    }

    @Test
    void recordLastHealthyHeartbeatReportTime_WhenUnhealthy_ShouldNotPersist() {
        // Arrange
        ClusterEntity clusterEntity = ClusterEntity.builder()
                .clusterId("test-cluster")
                .clusterProvider(ClusterProviderEnum.AWS)
                .build();

        // Act
        heartbeatService.recordLastHealthyHeartbeatReportTime(clusterEntity, CloudHealthStatus.UNHEALTHY);

        // Assert
        verifyNoInteractions(nvcaClusterRepository);
    }
}
