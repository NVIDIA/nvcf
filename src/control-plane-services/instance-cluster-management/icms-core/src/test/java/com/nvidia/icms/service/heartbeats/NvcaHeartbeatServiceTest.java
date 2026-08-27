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

import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.getDummyNvcaClusterHeartbeatRequest;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatResponse;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaHeartbeatActionResponse;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatRequest.NvcaClusterCapacityStats;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.service.ByocService;
import com.nvidia.icms.service.CloudHealthService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.TestUtil;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import java.util.stream.Stream;

@ExtendWith(MockitoExtension.class)
public class NvcaHeartbeatServiceTest {

    @Mock
    CloudHealthService cloudHealthService;
    @Mock
    ByocConfigurationProperties byocConfigurationProperties;
    @Mock
    ByocService byocService;
    @Mock
    TelemetryEventClient telemetryEventClient;
    @Mock
    ClusterRepository clusterRepository;
    @Mock
    NvcaClusterRepository nvcaClusterRepository;
    @Mock
    private NvcaConfigurationProperties nvcaConfigurationProperties;

    NvcaHeartbeatService nvcaHeartbeatService;

    @BeforeEach
    void setUp() {
        // computePlatformService is constructor-injected; pass a Non BYOC-configured instance
        // so classification preserves the pre-genericization behaviour.
        nvcaHeartbeatService = new NvcaHeartbeatService(cloudHealthService,
                byocConfigurationProperties,
                byocService,
                telemetryEventClient,
                TestUtil.getObjectMapperInstance(),
                clusterRepository,
                nvcaClusterRepository,
                nvcaConfigurationProperties,
                ComputePlatformTestFixtures.nonByocComputePlatformService());
    }

    @Test
    void recordNvcaClusterHeartbeat() {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_CLUSTER_ID);

        var ttl = 111;
        when(byocConfigurationProperties.getCloudHealthTtlForByocInSec()).thenReturn(ttl);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(Optional.of(clusterEntity));
        doNothing().when(nvcaClusterRepository).updateClusterEntity(Mockito.any());
        when(nvcaConfigurationProperties.isNvcaSelfDestructEnabled()).thenReturn(false);

        // Act
        NvcaClusterHeartbeatResponse response = nvcaHeartbeatService.recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);

        // Assert
        assertNotNull(response);
        assertEquals(NvcaHeartbeatActionResponse.ACCEPTED, response.getAction());
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true);
        verify(nvcaClusterRepository).updateClusterEntity(Mockito.any());
        verify(telemetryEventClient, times(1)).triggerEvent(Mockito.anyList());
        verify(cloudHealthService).updateCloudHealthStatus(ResourceProvider.BYOC,
                                                           DUMMY_CLUSTER_ID,
                                                           request.getStatus(),
                                                           request.getUpgradeStatus(),
                                                           Map.of("gpu1",
                                                                  GpuCapacity.builder().capacity(10)
                                                                          .allocated(4)
                                                                          .available(6).build(),
                                                                  "gpu2",
                                                                  GpuCapacity.builder().capacity(20)
                                                                          .allocated(11)
                                                                          .available(9).build()),
                                                           ttl);
    }

    @Test
    void recordNvcaClusterHeartbeat_failure() {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(Optional.empty());

        // Act and Assert
        assertThrows(IcmsNotFoundException.class,
                     () -> nvcaHeartbeatService.recordClusterHeartbeat(DUMMY_CLUSTER_ID, request));
        verify(clusterRepository).getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true);
    }

    @Test
    void determineHeartbeatResponse_featureDisabled_returnsAccepted() {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_CLUSTER_ID);
        request.setNvcaAgentVersion("1.0.0");
        when(nvcaConfigurationProperties.isNvcaSelfDestructEnabled()).thenReturn(false);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(Optional.of(clusterEntity));

        // Act
        NvcaClusterHeartbeatResponse response = nvcaHeartbeatService.recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);

        // Assert
        assertEquals(NvcaHeartbeatActionResponse.ACCEPTED, response.getAction());
    }

    @Test
    void determineHeartbeatResponse_noAgentVersion_returnsAccepted() {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        request.setNvcaAgentVersion(null);
        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_CLUSTER_ID);
        when(nvcaConfigurationProperties.isNvcaSelfDestructEnabled()).thenReturn(true);
        when(nvcaConfigurationProperties.getNvcaSelfDestructMinVersion()).thenReturn("2.47.3");
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(Optional.of(clusterEntity));

        // Act
        NvcaClusterHeartbeatResponse response = nvcaHeartbeatService.recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);

        // Assert
        assertEquals(NvcaHeartbeatActionResponse.ACCEPTED, response.getAction());
    }

    @Test
    void determineHeartbeatResponse_emptyAgentVersion_returnsAccepted() {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        request.setNvcaAgentVersion("");
        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_CLUSTER_ID);
        when(nvcaConfigurationProperties.isNvcaSelfDestructEnabled()).thenReturn(true);
        when(nvcaConfigurationProperties.getNvcaSelfDestructMinVersion()).thenReturn("2.47.3");
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(Optional.of(clusterEntity));

        // Act
        NvcaClusterHeartbeatResponse response = nvcaHeartbeatService.recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);

        // Assert
        assertEquals(NvcaHeartbeatActionResponse.ACCEPTED, response.getAction());
    }

    @Test
    void determineHeartbeatResponse_blankAgentVersion_returnsAccepted() {
        // Prepare
        var request = getDummyNvcaClusterHeartbeatRequest();
        request.setNvcaAgentVersion("   ");
        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_CLUSTER_ID);
        when(nvcaConfigurationProperties.isNvcaSelfDestructEnabled()).thenReturn(true);
        when(nvcaConfigurationProperties.getNvcaSelfDestructMinVersion()).thenReturn("2.47.3");
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(Optional.of(clusterEntity));

        // Act
        NvcaClusterHeartbeatResponse response = nvcaHeartbeatService.recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);

        // Assert
        assertEquals(NvcaHeartbeatActionResponse.ACCEPTED, response.getAction());
    }

    @Test
    void toGpuCapacityMap_ShouldConvertGpuUsageToGpuCapacity() {
        // Prepare
        NvcaClusterHeartbeatRequest request = new NvcaClusterHeartbeatRequest();
        Map<String, NvcaClusterCapacityStats> gpuUsage = new HashMap<>();
        NvcaClusterCapacityStats stats = new NvcaClusterCapacityStats();
        stats.setCapacity(10);
        stats.setAllocated(5);
        stats.setAvailable(5);
        gpuUsage.put("gpu1", stats);
        request.setGpuUsage(gpuUsage);

        // Act
        Map<String, GpuCapacity> result = nvcaHeartbeatService.toGpuCapacityMap(request);

        // Assert
        assertEquals(1, result.size());
        GpuCapacity capacity = result.get("gpu1");
        assertNotNull(capacity);
        assertEquals(10, capacity.getCapacity());
        assertEquals(5, capacity.getAllocated());
        assertEquals(5, capacity.getAvailable());
    }

    @Test
    void toGpuCapacityMap_WithEmptyGpuUsage() {
        // Prepare
        NvcaClusterHeartbeatRequest request = new NvcaClusterHeartbeatRequest();
        request.setGpuUsage(new HashMap<>());

        // Act
        Map<String, GpuCapacity> result = nvcaHeartbeatService.toGpuCapacityMap(request);

        // Assert
        assertEquals(0, result.size());
    }

    @Test
    void getCapacityStats_ShouldReturnGpuUsage() {
        // Prepare
        NvcaClusterHeartbeatRequest request = new NvcaClusterHeartbeatRequest();
        Map<String, NvcaClusterCapacityStats> expectedGpuUsage = new HashMap<>();
        request.setGpuUsage(expectedGpuUsage);

        // Act
        Map<String, NvcaClusterCapacityStats> result = nvcaHeartbeatService.getCapacityStats(request);

        // Assert
        assertEquals(expectedGpuUsage, result);
    }

    @Test
    void createMetadataForEvent_ShouldCreateCompleteMetadata() {
        // Prepare
        NvcaClusterHeartbeatRequest request = new NvcaClusterHeartbeatRequest();
        request.setStatus(CloudHealthStatus.HEALTHY);
        request.setUpgradeStatus("UP_TO_DATE");
        request.setClusterOwnerNcaId("owner123");
        request.setNvcaAgentVersion("1.0.0");
        request.setNvcaOperatorVersion("2.0.0");
        request.setClusterName("test-cluster");

        Map<String, NvcaClusterCapacityStats> gpuUsage = new HashMap<>();
        NvcaClusterCapacityStats stats = new NvcaClusterCapacityStats();
        stats.setCapacity(10);
        stats.setAllocated(5);
        stats.setAvailable(5);
        gpuUsage.put("gpu1", stats);
        request.setGpuUsage(gpuUsage);

        // Act
        Map<String, Object> metadata = nvcaHeartbeatService.createMetadataForEvent(request);

        // Assert
        assertEquals(CloudHealthStatus.HEALTHY.toString(), metadata.get("clusterStatus"));
        assertEquals("UP_TO_DATE", metadata.get("clusterUpgradeStatus"));
        assertNotNull(metadata.get("GPU_USAGE"));
        assertEquals("owner123", metadata.get("ClusterOwnerNcaId"));
        assertEquals("1.0.0", metadata.get("NvcaAgentVersion"));
        assertEquals("2.0.0", metadata.get("NvcaOperatorVersion"));
        assertEquals("test-cluster", metadata.get("clusterName"));
    }

    @Test
    void createMetadataForEvent_WithMinimalData() {
        // Prepare
        NvcaClusterHeartbeatRequest request = new NvcaClusterHeartbeatRequest();
        request.setStatus(CloudHealthStatus.HEALTHY);
        request.setUpgradeStatus("UP_TO_DATE");

        // Act
        Map<String, Object> metadata = nvcaHeartbeatService.createMetadataForEvent(request);

        // Assert
        assertEquals(CloudHealthStatus.HEALTHY.toString(), metadata.get("clusterStatus"));
        assertEquals("UP_TO_DATE", metadata.get("clusterUpgradeStatus"));
        assertEquals(2, metadata.size());
    }

    // Comprehensive parameterized test for version comparison
    @ParameterizedTest
    @MethodSource("versionComparisonTestCases")
    void determineHeartbeatResponse_versionComparison_returnsExpectedResponse(
            String agentVersion, 
            String minVersion, 
            NvcaHeartbeatActionResponse expectedResponse,
            String testDescription) {
        // Prepare (mocking setup)
        var request = getDummyNvcaClusterHeartbeatRequest();
        request.setNvcaAgentVersion(agentVersion);
        when(nvcaConfigurationProperties.isNvcaSelfDestructEnabled()).thenReturn(true);
        when(nvcaConfigurationProperties.getNvcaSelfDestructMinVersion()).thenReturn(minVersion);
        when(byocConfigurationProperties.getCloudHealthTtlForByocInSec()).thenReturn(300);
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_CLUSTER_ID, true)).thenReturn(Optional.of(TestUtil.getDummyClusterEntity()));
        doNothing().when(nvcaClusterRepository).updateClusterEntity(Mockito.any());

        // Act
        NvcaClusterHeartbeatResponse response = nvcaHeartbeatService.recordClusterHeartbeat(DUMMY_CLUSTER_ID, request);

        // Assert
        assertEquals(expectedResponse, response.getAction(), 
                String.format("Test failed for: %s. Agent version: %s, Min version: %s", 
                        testDescription, agentVersion, minVersion));
    }

    static Stream<Arguments> versionComparisonTestCases() {
        return Stream.of(
                // Format: (agentVersion, minVersion, expectedResponse, testDescription)

                // Basic version comparisons
                Arguments.of("1.47.3", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Major version lower"),
                Arguments.of("2.46.3", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Minor version lower"),
                Arguments.of("2.47.2", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Patch version lower"),
                Arguments.of("2.47.3", "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Exact version match"),
                Arguments.of("2.47.4", "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Patch version higher"),
                Arguments.of("2.48.0", "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Minor version higher"),
                Arguments.of("3.0.0", "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Major version higher"),

                // Prerelease versions (with - suffix) - should return SELF_DESTRUCT (prerelease < base)
                Arguments.of("2.47.3-20230828", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Date suffix"),
                Arguments.of("2.47.3-dev-feature-branch", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Feature branch suffix"),
                Arguments.of("2.47.3-beta.2", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Beta prerelease"),
                Arguments.of("2.47.3-rc.1", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Release candidate"),

                // Build metadata versions (with + suffix) - should return ACCEPTED (build metadata doesn't affect precedence)
                Arguments.of("2.47.3+20230828", "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Build metadata with date"),
                Arguments.of("2.47.3+build.123", "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Build metadata with build number"),

                // Only MAJOR provided, remaining fields will be considered as 0
                Arguments.of("2", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Missing minor and patch versions"),

                // Only MAJOR and MINOR provided, remaining fields will be considered as 0
                Arguments.of("2.47", "2.47.3", NvcaHeartbeatActionResponse.SELF_DESTRUCT, "Missing patch version"),

                // Edge cases - invalid versions should return ACCEPTED (fallback behavior)
                Arguments.of("invalid-version", "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Invalid version format"),
                Arguments.of("", "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Empty version string"),
                Arguments.of(null, "2.47.3", NvcaHeartbeatActionResponse.ACCEPTED, "Null version string")
        );
    }
}
