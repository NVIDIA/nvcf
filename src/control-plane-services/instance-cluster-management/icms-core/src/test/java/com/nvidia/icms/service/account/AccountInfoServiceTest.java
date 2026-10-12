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
import com.nvidia.icms.inbound.rest.model.InstanceTypeDetails;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.byoc.ClustersService.ReadyClusterInfo;
import com.nvidia.icms.service.gating.GpuGatingService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
class AccountInfoServiceTest {

    private static final String TEST_REGION = "us-west-1";
    private static final String TEST_CLUSTER = "test-cluster";
    private static final String GATED_NCA_ID = "GatedNcaId1aBcDeF";
    private static final String UNGATED_NCA_ID = "UngatedNcaId1aBcDeF";
    private static final String GPU_ALLOWED = "DUMMY_GPU_1";
    private static final String GPU_GATED = "DUMMY_GPU_2";
    private static final String INSTANCE_TYPE_ALLOWED = "dummy_gpu_1.large";
    private static final String INSTANCE_TYPE_GATED = "dummy_gpu_1.xlarge";
    private static final String INSTANCE_TYPE_OTHER_GPU = "dummy_gpu_2.large";

    @Mock
    private ClusterGpuInfoHelper clusterGpuInfoHelper;

    @Mock
    private ClusterTargetingHelper clusterTargetingHelper;

    private AccountInfoService accountInfoService;

    @BeforeEach
    void setUp() {
        accountInfoService = new AccountInfoService(null, null, clusterGpuInfoHelper,
                ComputePlatformTestFixtures.nonByocComputePlatformService(),
                new GpuGatingService(null, null));
    }

    @Test
    void updateInstanceTypeDetails_ShouldUpdateAllFields() {
        // Arrange
        InstanceTypeDetails instanceTypeDetails = createInstanceTypeDetails(10);

        Set<String> clusterAttributes = new HashSet<>();
        clusterAttributes.add("attr1");
        clusterAttributes.add("attr2");
        InstanceTypeAvailabilityResponse.Cluster cluster = createCluster(5, clusterAttributes);

        when(clusterGpuInfoHelper.includeClusterBasedOnAccessLevel(cluster)).thenReturn(true);

        // Act
        InstanceTypeDetails result = accountInfoService.updateInstanceTypeDetails(
            instanceTypeDetails, TEST_REGION, cluster, true);

        // Assert
        assertEquals(15, result.getAvailableCapacity());
        assertTrue(result.getRegions().contains(TEST_REGION));
        assertTrue(result.getClusters().contains(TEST_CLUSTER));
        assertTrue(result.getAttributes().containsAll(clusterAttributes));
        assertTrue(result.getDefaultable());
    }

    @Test
    void updateInstanceTypeDetails_WhenClusterNotIncluded_ShouldNotAddClusterName() {
        // Arrange
        InstanceTypeDetails instanceTypeDetails = createInstanceTypeDetails(10);
        InstanceTypeAvailabilityResponse.Cluster cluster = createCluster(5, new HashSet<>());

        when(clusterGpuInfoHelper.includeClusterBasedOnAccessLevel(cluster)).thenReturn(false);

        // Act
        InstanceTypeDetails result = accountInfoService.updateInstanceTypeDetails(
            instanceTypeDetails, TEST_REGION, cluster, false);

        // Assert
        assertEquals(15, result.getAvailableCapacity());
        assertTrue(result.getRegions().contains(TEST_REGION));
        assertFalse(result.getClusters().contains(TEST_CLUSTER));
        assertFalse(result.getDefaultable());
    }

    @Test
    void updateInstanceTypeDetails_WhenClusterHasNoAttributes_ShouldNotAddAttributes() {
        // Arrange
        InstanceTypeDetails instanceTypeDetails = createInstanceTypeDetails(10);
        InstanceTypeAvailabilityResponse.Cluster cluster = createCluster(5, null);

        when(clusterGpuInfoHelper.includeClusterBasedOnAccessLevel(cluster)).thenReturn(true);

        // Act
        InstanceTypeDetails result = accountInfoService.updateInstanceTypeDetails(
            instanceTypeDetails, TEST_REGION, cluster, false);

        // Assert
        assertEquals(15, result.getAvailableCapacity());
        assertTrue(result.getRegions().contains(TEST_REGION));
        assertTrue(result.getClusters().contains(TEST_CLUSTER));
        assertTrue(result.getAttributes().isEmpty());
        assertFalse(result.getDefaultable());
    }

    @Test
    void getAllGpusForAccount_WhenAccountIsGated_ShouldReturnOnlyAllowedGpus() {
        // Arrange
        stubReadyClusters();
        gate(GATED_NCA_ID, Map.of(GPU_ALLOWED, List.of(INSTANCE_TYPE_ALLOWED)));

        // Act
        Map<String, Set<String>> result =
                accountInfoService.getAllGpusForAccount(GATED_NCA_ID, new GpuUsageFilter());

        // Assert
        assertEquals(Set.of(GPU_ALLOWED),
                     result.get(AccountInfoService.GPUS_RESPONSE_FIELD_NAME));
    }

    @Test
    void getInstanceTypeAvailability_WhenAccountIsGated_ShouldReturnOnlyAllowedInstanceTypes() {
        // Arrange
        stubReadyClusters();
        gate(GATED_NCA_ID, Map.of(GPU_ALLOWED, List.of(INSTANCE_TYPE_ALLOWED)));

        // Act
        InstanceTypeAvailabilityResponse result =
                accountInfoService.getInstanceTypeAvailability(GATED_NCA_ID);

        // Assert
        assertEquals(1, result.getGpus().size());
        InstanceTypeAvailabilityResponse.Gpu gpu = result.getGpus().iterator().next();
        assertEquals(GPU_ALLOWED, gpu.getGpuName());
        assertEquals(Set.of(INSTANCE_TYPE_ALLOWED), instanceTypeNames(gpu));
    }

    /**
     * A GPU is allowed but every one of its instance types is gated out, so the GPU must
     * disappear from the response rather than show up with an empty instance type list.
     */
    @Test
    void getInstanceTypeAvailability_WhenAllInstanceTypesOfGpuGatedOut_ShouldDropGpu() {
        // Arrange
        stubReadyClusters();
        gate(GATED_NCA_ID, Map.of(GPU_ALLOWED, List.of("some-other-instance-type")));

        // Act
        InstanceTypeAvailabilityResponse result =
                accountInfoService.getInstanceTypeAvailability(GATED_NCA_ID);

        // Assert
        assertTrue(result.getGpus().isEmpty());
    }

    @Test
    void getInstanceTypeAvailability_WhenAccountIsNotGated_ShouldReturnEverything() {
        // Arrange
        stubReadyClusters();
        gate(GATED_NCA_ID, Map.of(GPU_ALLOWED, List.of(INSTANCE_TYPE_ALLOWED)));

        // Act
        InstanceTypeAvailabilityResponse result =
                accountInfoService.getInstanceTypeAvailability(UNGATED_NCA_ID);

        // Assert
        assertEquals(2, result.getGpus().size());
        Set<String> allInstanceTypes = result.getGpus().stream()
                .flatMap(gpu -> instanceTypeNames(gpu).stream())
                .collect(Collectors.toSet());
        assertEquals(Set.of(INSTANCE_TYPE_ALLOWED, INSTANCE_TYPE_GATED, INSTANCE_TYPE_OTHER_GPU),
                     allInstanceTypes);
    }

    /** Two GPUs on one cluster, each carrying the instance types the gating tests key off. */
    private void stubReadyClusters() {
        ReadyClusterInfo allowedGpuCluster = ReadyClusterInfo.builder()
                .clusterId(TEST_CLUSTER)
                .clusterName(TEST_CLUSTER)
                .gpu(GPU_ALLOWED)
                .region(TEST_REGION)
                .instanceTypes(new HashSet<>(Set.of(instanceType(INSTANCE_TYPE_ALLOWED),
                                                    instanceType(INSTANCE_TYPE_GATED))))
                .build();

        ReadyClusterInfo gatedGpuCluster = ReadyClusterInfo.builder()
                .clusterId(TEST_CLUSTER)
                .clusterName(TEST_CLUSTER)
                .gpu(GPU_GATED)
                .region(TEST_REGION)
                .instanceTypes(new HashSet<>(Set.of(instanceType(INSTANCE_TYPE_OTHER_GPU))))
                .build();

        Set<ReadyClusterInfo> readyClusters = Set.of(allowedGpuCluster, gatedGpuCluster);

        when(clusterTargetingHelper.getAllClusterHealthInMap()).thenReturn(Map.of());
        when(clusterGpuInfoHelper.getReadyClusterInfo(anyString())).thenReturn(readyClusters);
        when(clusterGpuInfoHelper.getActiveReservationsPerNcaId(anyString()))
                .thenReturn(List.of());
        when(clusterGpuInfoHelper.buildRegionToClusterInfoMap(readyClusters))
                .thenReturn(Map.of(TEST_REGION, new ArrayList<>(readyClusters)));
        when(clusterGpuInfoHelper.isClusterAllowed(any(), any())).thenReturn(true);
        when(clusterGpuInfoHelper.getAvailableCapacity(anyString(), any(), any(), any()))
                .thenReturn(8);
    }

    private void gate(String ncaId, Map<String, List<String>> allowedInstanceTypesByGpu) {
        IcmsConfigurationProperties properties = new IcmsConfigurationProperties();
        properties.setGpuGating(Map.of(ncaId, allowedInstanceTypesByGpu));
        accountInfoService = new AccountInfoService(clusterTargetingHelper, properties,
                clusterGpuInfoHelper, ComputePlatformTestFixtures.nonByocComputePlatformService(),
                new GpuGatingService(properties, null));
    }

    private static InstanceTypeV5Udt instanceType(String name) {
        return InstanceTypeV5Udt.builder().name(name).gpuCount(1).build();
    }

    private static Set<String> instanceTypeNames(InstanceTypeAvailabilityResponse.Gpu gpu) {
        return gpu.getInstanceTypes().stream()
                .map(InstanceTypeAvailabilityResponse.InstanceType::getInstanceName)
                .collect(Collectors.toSet());
    }

    private InstanceTypeDetails createInstanceTypeDetails(int initialCapacity) {
        InstanceTypeDetails instanceTypeDetails = new InstanceTypeDetails();
        instanceTypeDetails.setAvailableCapacity(initialCapacity);
        instanceTypeDetails.setRegions(new HashSet<>());
        instanceTypeDetails.setClusters(new HashSet<>());
        instanceTypeDetails.setAttributes(new HashSet<>());
        return instanceTypeDetails;
    }

    private InstanceTypeAvailabilityResponse.Cluster createCluster(int maxCapacity, Set<String> attributes) {
        InstanceTypeAvailabilityResponse.Cluster cluster = new InstanceTypeAvailabilityResponse.Cluster();
        cluster.setMaxClusterAvailableCapacity(maxCapacity);
        cluster.setClusterName(TEST_CLUSTER);
        cluster.setAttributes(attributes);
        return cluster;
    }
} 