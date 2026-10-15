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
import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.InstanceTypeDetails;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeV5Udt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.service.extensions.api.ClusterAuthorizationService;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;
import com.nvidia.icms.service.extensions.impl.NoOpClusterAuthorizationService;
import com.nvidia.icms.service.byoc.ClusterTargetingHelper;
import com.nvidia.icms.service.byoc.ClustersService.ReadyClusterInfo;
import com.nvidia.icms.service.extensions.api.ReservationProcessor;
import jakarta.validation.constraints.NotNull;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.mockito.junit.jupiter.MockitoExtension;

import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class ClusterGpuInfoHelperTest {

    private static final int ZERO_GPUS = 0;
    private static final int ONE_GPU = 1;
    private static final int TWO_GPUS = 2;
    private static final int FOUR_GPUS = 4;
    private static final int EIGHT_GPUS = 8;
    private static final int SIXTEEN_GPUS = 16;
    private static final String TEST_GPU_NAME = "test-gpu";
    private static final String TEST_GPU_NAME_2 = "test-gpu-2";
    private static final int AVAILABLE_CAPACITY = 10;
    private static final int ZERO_CAPACITY = 0;
    private static final String TEST_CLUSTER_NAME = "test-cluster";
    private static final String TEST_CLUSTER_GROUP = "test-group";
    private static final String TEST_ATTRIBUTE = "test-attribute";
    private static final String TEST_REGION_1 = "test-region-1";
    private static final String TEST_REGION_2 = "test-region-2";
    private static final String TEST_CLUSTER_1 = "test-cluster-1";
    private static final String TEST_CLUSTER_2 = "test-cluster-2";
    private static final String TEST_CLUSTER_3 = "test-cluster-3";
    private static final String TEST_NCA_ID = "test-nca-id";
    private static final String TEST_CLUSTER_ID = "test-cluster";
    private static final String TEST_GPU_TYPE = "test-gpu";
    private static final int TEST_RESERVED_GPU_COUNT = 10;
    private static final int TEST_RESERVED_CAPACITY = 5;
    private static final int TEST_CAPACITY_1 = 3;
    private static final int TEST_CAPACITY_2 = 4;

    private static final ComputePlatformService COMPUTE_PLATFORM_SERVICE_NON_BYOC =
            ComputePlatformTestFixtures.nonByocComputePlatformService();
    /**
     * No-op SPI used for tests that do not exercise Non BYOC authorization filtering.
     */
    private static final ClusterAuthorizationService NON_BYOC_AUTHZ_NOOP =
            new NoOpClusterAuthorizationService();

    private ClusterGpuInfoHelper clusterGpuInfoHelper;
    private CloudHealthEntity cloudHealth;
    private Map<String, GpuCapacity> gpuUsage;
    private ReadyClusterInfo readyClusterInfo;
    private GpuUsageFilter filter;

    @BeforeEach
    void setUp() {
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(null, null, null, null, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        gpuUsage = new HashMap<>();
        cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        cloudHealth.setGpuUsage(gpuUsage);

        // Create ReadyClusterInfo with test data
        Set<String> attributes = new HashSet<>();
        attributes.add(TEST_ATTRIBUTE);
        Set<NodeTypeEnum> nodeTypes = new HashSet<>();
        nodeTypes.add(NodeTypeEnum.SINGLE);

        readyClusterInfo = ReadyClusterInfo.builder()
                .clusterName(TEST_CLUSTER_NAME)
                .gpu(TEST_GPU_NAME)
                .attributes(attributes)
                .clusterGroupName(TEST_CLUSTER_GROUP)
                .supportedNodeTypes(nodeTypes)
                .build();

        // Create GpuUsageFilter with test data from readyClusterInfo
        filter = GpuUsageFilter.builder()
                .clusterNames(new HashSet<>(Set.of(readyClusterInfo.getClusterName())))
                .gpuNames(new HashSet<>(Set.of(readyClusterInfo.getGpu())))
                .attributes(new HashSet<>(readyClusterInfo.getAttributes()))
                .clusterGroupNames(new HashSet<>(Set.of(readyClusterInfo.getClusterGroupName())))
                .instanceTypeUsageFilter(null) // This will allow all node types
                .build();
    }

    @Test
    void getMaxInstancesCreatedWithAvailableCapacity_WhenGpuCountIsZero_ReturnsZero() {
        // Given
        int availableGpus = 10;
        int gpuCountPerInstance = ZERO_GPUS;

        // When
        int result = ClusterGpuInfoHelper.getMaxInstancesCreatedWithAvailableCapacity(availableGpus, gpuCountPerInstance);

        // Then
        assertEquals(ZERO_GPUS, result);
    }

    @Test
    void getMaxInstancesCreatedWithAvailableCapacity_WhenAvailableGpusIsZero_ReturnsZero() {
        // Given
        int availableGpus = ZERO_GPUS;
        int gpuCountPerInstance = ONE_GPU;

        // When
        int result = ClusterGpuInfoHelper.getMaxInstancesCreatedWithAvailableCapacity(availableGpus, gpuCountPerInstance);

        // Then
        assertEquals(ZERO_GPUS, result);
    }

    @ParameterizedTest
    @CsvSource({
        "10, 1, 10",    // 10 GPUs available, 1 GPU per instance = 10 instances
        "10, 2, 5",     // 10 GPUs available, 2 GPUs per instance = 5 instances
        "10, 4, 2",     // 10 GPUs available, 4 GPUs per instance = 2 instances
        "10, 8, 1",     // 10 GPUs available, 8 GPUs per instance = 1 instance
        "10, 16, 0",    // 10 GPUs available, 16 GPUs per instance = 0 instances
        "16, 4, 4",     // 16 GPUs available, 4 GPUs per instance = 4 instances
        "15, 4, 3",     // 15 GPUs available, 4 GPUs per instance = 3 instances (rounds down)
        "1, 1, 1",      // 1 GPU available, 1 GPU per instance = 1 instance
        "1, 2, 0"       // 1 GPU available, 2 GPUs per instance = 0 instances
    })
    void getMaxInstancesCreatedWithAvailableCapacity_WithVariousInputs_ReturnsCorrectResult(
            int availableGpus, int gpuCountPerInstance, int expectedResult) {
        // When
        int result = ClusterGpuInfoHelper.getMaxInstancesCreatedWithAvailableCapacity(availableGpus, gpuCountPerInstance);

        // Then
        assertEquals(expectedResult, result);
    }

    @Test
    void getHealthyClusterCapacity_WhenCloudHealthIsNull_ReturnsNull() {
        assertNull(clusterGpuInfoHelper.getHealthyClusterCapacity(null, TEST_GPU_NAME));
    }

    @Test
    void getHealthyClusterCapacity_WhenStatusIsNotHealthy_ReturnsNull() {
        cloudHealth.setStatus(CloudHealthStatus.UNHEALTHY);
        assertNull(clusterGpuInfoHelper.getHealthyClusterCapacity(cloudHealth, TEST_GPU_NAME));
    }

    @Test
    void getHealthyClusterCapacity_WhenGpuUsageIsNull_ReturnsNull() {
        cloudHealth.setGpuUsage(null);
        assertNull(clusterGpuInfoHelper.getHealthyClusterCapacity(cloudHealth, TEST_GPU_NAME));
    }

    @Test
    void getHealthyClusterCapacity_WhenGpuUsageIsEmpty_ReturnsNull() {
        assertNull(clusterGpuInfoHelper.getHealthyClusterCapacity(cloudHealth, TEST_GPU_NAME));
    }

    @Test
    void getHealthyClusterCapacity_WhenGpuNameNotInUsage_ReturnsNull() {
        gpuUsage.put(TEST_GPU_NAME_2, createGpuCapacity(AVAILABLE_CAPACITY));
        assertNull(clusterGpuInfoHelper.getHealthyClusterCapacity(cloudHealth, TEST_GPU_NAME));
    }

    @Test
    void getHealthyClusterCapacity_WhenGpuHasZeroCapacity_ReturnsNull() {
        gpuUsage.put(TEST_GPU_NAME, createGpuCapacity(ZERO_CAPACITY));
        assertNull(clusterGpuInfoHelper.getHealthyClusterCapacity(cloudHealth, TEST_GPU_NAME));
    }

    @Test
    void getHealthyClusterCapacity_WhenGpuHasNegativeCapacity_ReturnsNull() {
        gpuUsage.put(TEST_GPU_NAME, createGpuCapacity(-1));
        assertNull(clusterGpuInfoHelper.getHealthyClusterCapacity(cloudHealth, TEST_GPU_NAME));
    }

    @Test
    void getHealthyClusterCapacity_WhenGpuHasValidCapacity_ReturnsGpuCapacity() {
        GpuCapacity expectedCapacity = createGpuCapacity(AVAILABLE_CAPACITY);
        gpuUsage.put(TEST_GPU_NAME, expectedCapacity);
        
        GpuCapacity result = clusterGpuInfoHelper.getHealthyClusterCapacity(cloudHealth, TEST_GPU_NAME);
        
        assertNotNull(result);
        assertEquals(expectedCapacity, result);
    }

    @Test
    void isClusterAllowed_WhenAllConditionsMet_ReturnsTrue() {
        boolean result = clusterGpuInfoHelper.isClusterAllowed(readyClusterInfo, filter);
        assertTrue(result);
    }

    @Test
    void isClusterAllowed_WhenAllConditionsMet_DefaultUsage_ReturnsTrue() {
        filter.setInstanceTypeUsageFilter(InstanceTypeUsageEnum.DEFAULT);
        boolean result = clusterGpuInfoHelper.isClusterAllowed(readyClusterInfo, filter);
        assertTrue(result);
    }

    @Test
    void isClusterAllowed_WhenClusterNameNotAllowed_ReturnsFalse() {

        filter = GpuUsageFilter.builder()
                .clusterNames(Set.of("wrongClusterName"))
                .gpuNames(new HashSet<>(Set.of(readyClusterInfo.getGpu())))
                .attributes(new HashSet<>(readyClusterInfo.getAttributes()))
                .clusterGroupNames(new HashSet<>(Set.of(readyClusterInfo.getClusterGroupName())))
                .instanceTypeUsageFilter(null)
                .build();

        boolean result = clusterGpuInfoHelper.isClusterAllowed(readyClusterInfo, filter);
        assertFalse(result);
    }

    @Test
    void isClusterAllowed_WhenGpuNameNotAllowed_ReturnsFalse() {
        filter = GpuUsageFilter.builder()
                .clusterNames(new HashSet<>(Set.of(readyClusterInfo.getClusterName())))
                .gpuNames(Set.of("wrongGpu"))
                .attributes(new HashSet<>(readyClusterInfo.getAttributes()))
                .clusterGroupNames(new HashSet<>(Set.of(readyClusterInfo.getClusterGroupName())))
                .instanceTypeUsageFilter(null)
                .build();

        boolean result = clusterGpuInfoHelper.isClusterAllowed(readyClusterInfo, filter);
        assertFalse(result);
    }

    @Test
    void isClusterAllowed_WhenAttributesNotAllowed_ReturnsFalse() {
        filter = GpuUsageFilter.builder()
                .clusterNames(new HashSet<>(Set.of(readyClusterInfo.getClusterName())))
                .gpuNames(new HashSet<>(Set.of(readyClusterInfo.getGpu())))
                .attributes(Set.of("wrongAttribute"))
                .clusterGroupNames(new HashSet<>(Set.of(readyClusterInfo.getClusterGroupName())))
                .instanceTypeUsageFilter(null)
                .build();

        boolean result = clusterGpuInfoHelper.isClusterAllowed(readyClusterInfo, filter);
        assertFalse(result);
    }

    @Test
    void isClusterAllowed_WhenClusterGroupNotAllowed_ReturnsFalse() {
        filter = GpuUsageFilter.builder()
                .clusterNames(new HashSet<>(Set.of(readyClusterInfo.getClusterName())))
                .gpuNames(new HashSet<>(Set.of(readyClusterInfo.getGpu())))
                .attributes(new HashSet<>(readyClusterInfo.getAttributes()))
                .clusterGroupNames(Set.of("wrongClusterGroupName"))
                .instanceTypeUsageFilter(null)
                .build();

        boolean result = clusterGpuInfoHelper.isClusterAllowed(readyClusterInfo, filter);
        assertFalse(result);
    }

    @Test
    void isClusterAllowed_WhenInstanceUsageNotAllowed_ReturnsFalse() {
        filter = GpuUsageFilter.builder()
                .clusterNames(new HashSet<>(Set.of(readyClusterInfo.getClusterName())))
                .gpuNames(new HashSet<>(Set.of(readyClusterInfo.getGpu())))
                .attributes(new HashSet<>(readyClusterInfo.getAttributes()))
                .clusterGroupNames(new HashSet<>(Set.of(readyClusterInfo.getClusterGroupName())))
                .instanceTypeUsageFilter(InstanceTypeUsageEnum.CONTAINER) // This will only allow SINGLE node type
                .build();

        // Update readyClusterInfo to have MULTI node type
        Set<NodeTypeEnum> multiNodeTypes = new HashSet<>();
        multiNodeTypes.add(NodeTypeEnum.MULTI);
        readyClusterInfo = ReadyClusterInfo.builder()
                .clusterName(readyClusterInfo.getClusterName())
                .gpu(readyClusterInfo.getGpu())
                .attributes(readyClusterInfo.getAttributes())
                .clusterGroupName(readyClusterInfo.getClusterGroupName())
                .supportedNodeTypes(multiNodeTypes)
                .build();

        boolean result = clusterGpuInfoHelper.isClusterAllowed(readyClusterInfo, filter);
        assertFalse(result);
    }

    @Test
    void isClusterAllowed_WhenReadyClusterInfoIsNull_ReturnsFalse() {
        boolean result = clusterGpuInfoHelper.isClusterAllowed(null, filter);
        assertFalse(result);
    }

    /**
     * Tests that all fields are correctly updated when creating a new cluster.
     * Verifies that:
     * 1. All fields from ReadyClusterInfo are correctly copied to the response cluster
     * 2. Default instance type flag is correctly set based on instance type
     * 3. Max capacity is correctly calculated based on available GPUs
     * 4. Attributes are correctly copied from ReadyClusterInfo
     */
    @Test
    void updateValuesForNewCluster_UpdatesAllFieldsCorrectly() {
        // Given
        InstanceTypeAvailabilityResponse.Cluster responseCluster = new InstanceTypeAvailabilityResponse.Cluster();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setIsDefault(true);
        int gpuAvailable = 10;

        // When
        clusterGpuInfoHelper.updateValuesForNewCluster(responseCluster, readyClusterInfo, instanceType, gpuAvailable);

        // Then
        assertEquals(readyClusterInfo.getClusterName(), responseCluster.getClusterName());
        assertEquals(readyClusterInfo.getClusterProvider() != null ? readyClusterInfo.getClusterProvider().name() : null, responseCluster.getCloudProvider());
        assertEquals(readyClusterInfo.getClusterGroupName(), responseCluster.getClusterGroup());
        assertTrue(responseCluster.getIsDefaultInstanceType());
        assertEquals(10, responseCluster.getMaxClusterAvailableCapacity());
        assertEquals(readyClusterInfo.getAttributes(), responseCluster.getAttributes());
    }

    /**
     * Tests that null attributes are handled correctly when creating a new cluster.
     * Verifies that:
     * 1. When ReadyClusterInfo has null attributes, an empty set is created
     * 2. Other fields are correctly copied from ReadyClusterInfo
     * 3. The response cluster's attributes are initialized but empty
     */
    @Test
    void updateValuesForNewCluster_WithNullAttributes_SetsEmptySet() {
        // Given
        InstanceTypeAvailabilityResponse.Cluster responseCluster = new InstanceTypeAvailabilityResponse.Cluster();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setIsDefault(true);
        int gpuAvailable = 10;

        // Create ReadyClusterInfo with null attributes
        ReadyClusterInfo clusterInfoWithNullAttributes = ReadyClusterInfo.builder()
                .clusterName(readyClusterInfo.getClusterName())
                .gpu(readyClusterInfo.getGpu())
                .attributes(null)
                .clusterGroupName(readyClusterInfo.getClusterGroupName())
                .supportedNodeTypes(readyClusterInfo.getSupportedNodeTypes())
                .build();

        // When
        clusterGpuInfoHelper.updateValuesForNewCluster(responseCluster, clusterInfoWithNullAttributes, instanceType, gpuAvailable);

        // Then
        assertNotNull(responseCluster.getAttributes());
        assertTrue(responseCluster.getAttributes().isEmpty());
    }

    /**
     * Tests that non-default instance types are handled correctly.
     * Verifies that:
     * 1. When instance type is not default, isDefaultInstanceType is set to false
     * 2. Other fields are correctly copied from ReadyClusterInfo
     */
    @Test
    void updateValuesForNewCluster_WithNonDefaultInstanceType_SetsIsDefaultToFalse() {
        // Given
        InstanceTypeAvailabilityResponse.Cluster responseCluster = new InstanceTypeAvailabilityResponse.Cluster();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setIsDefault(false);
        int gpuAvailable = 10;

        // When
        clusterGpuInfoHelper.updateValuesForNewCluster(responseCluster, readyClusterInfo, instanceType, gpuAvailable);

        // Then
        assertFalse(responseCluster.getIsDefaultInstanceType());
    }

    /**
     * Tests that zero GPU availability is handled correctly.
     * Verifies that:
     * 1. When no GPUs are available, max capacity is set to zero
     * 2. Other fields are correctly copied from ReadyClusterInfo
     */
    @Test
    void updateValuesForNewCluster_WithZeroGpuAvailable_SetsMaxCapacityToZero() {
        // Given
        InstanceTypeAvailabilityResponse.Cluster responseCluster = new InstanceTypeAvailabilityResponse.Cluster();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setIsDefault(true);
        int gpuAvailable = 0;

        // When
        clusterGpuInfoHelper.updateValuesForNewCluster(responseCluster, readyClusterInfo, instanceType, gpuAvailable);

        // Then
        assertEquals(0, responseCluster.getMaxClusterAvailableCapacity());
    }

    /**
     * Tests that multi-GPU instance types are handled correctly.
     * Verifies that:
     * 1. Max capacity is correctly calculated based on GPUs per instance
     * 2. The calculation divides available GPUs by GPUs per instance
     * 3. Other fields are correctly copied from ReadyClusterInfo
     */
    @Test
    void updateValuesForNewCluster_WithMultiGpuInstanceType_CalculatesCorrectMaxCapacity() {
        // Given
        InstanceTypeAvailabilityResponse.Cluster responseCluster = new InstanceTypeAvailabilityResponse.Cluster();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setIsDefault(true);
        instanceType.setGpuCount(2); // 2 GPUs per instance
        int gpuAvailable = 10;

        // When
        clusterGpuInfoHelper.updateValuesForNewCluster(responseCluster, readyClusterInfo, instanceType, gpuAvailable);

        // Then
        assertEquals(5, responseCluster.getMaxClusterAvailableCapacity()); // 10 GPUs / 2 GPUs per instance = 5 instances
    }

    /**
     * Tests that all fields are correctly updated when creating a new instance.
     * Verifies that:
     * 1. All fields from InstanceTypeV5Udt are correctly copied to the response instance type
     * 2. Node type is correctly set to SINGLE
     * 3. Defaultable flag is correctly set based on isDefault value
     */
    @Test
    void updateValuesForNewInstance_UpdatesAllFieldsCorrectly() {
        // Given
        InstanceTypeAvailabilityResponse.InstanceType responseInstanceType = new InstanceTypeAvailabilityResponse.InstanceType();
        InstanceTypeV5Udt instanceType =createInstanceTypeV5Udt(2);

        // When
        clusterGpuInfoHelper.updateValuesForNewInstance(responseInstanceType, instanceType);

        // Then
        assertResponseInstanceType(responseInstanceType, instanceType, 2);
    }

    /**
     * Tests that MULTI node type is correctly set in the response.
     * Verifies that:
     * 1. Node type is correctly set to MULTI when specified
     * 2. Other fields remain unchanged
     */
    @Test
    void updateValuesForNewInstance_WithMultiNodeType_SetsCorrectNodeType() {
        // Given
        InstanceTypeAvailabilityResponse.InstanceType responseInstanceType = new InstanceTypeAvailabilityResponse.InstanceType();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setNodeType("MULTI");
        instanceType.setIsDefault(true);

        // When
        clusterGpuInfoHelper.updateValuesForNewInstance(responseInstanceType, instanceType);

        // Then
        assertEquals(InstanceTypeAvailabilityResponse.NodeType.MULTI, responseInstanceType.getNodeType());
    }

    /**
     * Tests that blank node type defaults to SINGLE.
     * Verifies that:
     * 1. When node type is blank, it defaults to SINGLE
     * 2. Other fields remain unchanged
     */
    @Test
    void updateValuesForNewInstance_WithBlankNodeType_SetsSingleNodeType() {
        // Given
        InstanceTypeAvailabilityResponse.InstanceType responseInstanceType = new InstanceTypeAvailabilityResponse.InstanceType();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setNodeType("");
        instanceType.setIsDefault(true);

        // When
        clusterGpuInfoHelper.updateValuesForNewInstance(responseInstanceType, instanceType);

        // Then
        assertEquals(InstanceTypeAvailabilityResponse.NodeType.SINGLE, responseInstanceType.getNodeType());
    }

    /**
     * Tests that null isDefault value results in false defaultable flag.
     * Verifies that:
     * 1. When isDefault is null, defaultable is set to false
     * 2. Other fields remain unchanged
     */
    @Test
    void updateValuesForNewInstance_WithNullIsDefault_SetsDefaultableToFalse() {
        // Given
        InstanceTypeAvailabilityResponse.InstanceType responseInstanceType = new InstanceTypeAvailabilityResponse.InstanceType();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setNodeType("SINGLE");
        instanceType.setIsDefault(null);

        // When
        clusterGpuInfoHelper.updateValuesForNewInstance(responseInstanceType, instanceType);

        // Then
        assertFalse(responseInstanceType.isDefaultable());
    }

    /**
     * Tests that false isDefault value results in false defaultable flag.
     * Verifies that:
     * 1. When isDefault is false, defaultable is set to false
     * 2. Other fields remain unchanged
     */
    @Test
    void updateValuesForNewInstance_WithFalseIsDefault_SetsDefaultableToFalse() {
        // Given
        InstanceTypeAvailabilityResponse.InstanceType responseInstanceType = new InstanceTypeAvailabilityResponse.InstanceType();
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setNodeType("SINGLE");
        instanceType.setIsDefault(false);

        // When
        clusterGpuInfoHelper.updateValuesForNewInstance(responseInstanceType, instanceType);

        // Then
        assertFalse(responseInstanceType.isDefaultable());
    }

    /**
     * Tests that clusters are correctly grouped by their regions.
     * Creates three clusters:
     * - Two clusters in TEST_REGION_1 (TEST_CLUSTER_1 and TEST_CLUSTER_2)
     * - One cluster in TEST_REGION_2 (TEST_CLUSTER_3)
     * 
     * Verifies that:
     * 1. The result map contains exactly two entries (one per region)
     * 2. TEST_REGION_1 contains both of its clusters
     * 3. TEST_REGION_2 contains its single cluster
     * 4. Each cluster is correctly associated with its region
     */
    @Test
    void buildRegionToClusterInfoMap_GroupsClustersByRegion() {
        // Given
        Set<ReadyClusterInfo> clusterInfos = new HashSet<>();
        clusterInfos.add(createReadyClusterInfo(TEST_CLUSTER_1, TEST_REGION_1));
        clusterInfos.add(createReadyClusterInfo(TEST_CLUSTER_2, TEST_REGION_1));
        clusterInfos.add(createReadyClusterInfo(TEST_CLUSTER_3, TEST_REGION_2));

        // When
        Map<String, List<ReadyClusterInfo>> result = clusterGpuInfoHelper.buildRegionToClusterInfoMap(clusterInfos);

        // Then
        assertEquals(2, result.size());
        assertTrue(result.containsKey(TEST_REGION_1));
        assertTrue(result.containsKey(TEST_REGION_2));
        
        List<ReadyClusterInfo> region1Clusters = result.get(TEST_REGION_1);
        assertEquals(2, region1Clusters.size());
        assertTrue(region1Clusters.stream().anyMatch(c -> c.getClusterName().equals(TEST_CLUSTER_1)));
        assertTrue(region1Clusters.stream().anyMatch(c -> c.getClusterName().equals(TEST_CLUSTER_2)));
        
        List<ReadyClusterInfo> region2Clusters = result.get(TEST_REGION_2);
        assertEquals(1, region2Clusters.size());
        assertEquals(TEST_CLUSTER_3, region2Clusters.get(0).getClusterName());
    }

    /**
     * Tests the behavior when an empty set of clusters is provided.
     * Verifies that:
     * 1. The method returns an empty map
     * 2. No regions are created
     * 
     * This test ensures the method handles empty input gracefully.
     */
    @Test
    void buildRegionToClusterInfoMap_WithEmptySet_ReturnsEmptyMap() {
        // Given
        Set<ReadyClusterInfo> emptySet = new HashSet<>();

        // When
        Map<String, List<ReadyClusterInfo>> result = clusterGpuInfoHelper.buildRegionToClusterInfoMap(emptySet);

        // Then
        assertTrue(result.isEmpty());
    }

    /**
     * Tests the behavior when all clusters are in the same region.
     * Creates two clusters in TEST_REGION_1:
     * - TEST_CLUSTER_1
     * - TEST_CLUSTER_2
     * 
     * Verifies that:
     * 1. The result map contains exactly one entry for TEST_REGION_1
     * 2. The region's list contains both clusters
     * 3. Each cluster is correctly associated with the region
     * 
     * This test ensures the method correctly handles multiple clusters in a single region.
     */
    @Test
    void buildRegionToClusterInfoMap_WithSingleRegion_ReturnsSingleEntry() {
        // Given
        Set<ReadyClusterInfo> clusterInfos = new HashSet<>();
        clusterInfos.add(createReadyClusterInfo(TEST_CLUSTER_1, TEST_REGION_1));
        clusterInfos.add(createReadyClusterInfo(TEST_CLUSTER_2, TEST_REGION_1));

        // When
        Map<String, List<ReadyClusterInfo>> result = clusterGpuInfoHelper.buildRegionToClusterInfoMap(clusterInfos);

        // Then
        assertEquals(1, result.size());
        assertTrue(result.containsKey(TEST_REGION_1));
        
        List<ReadyClusterInfo> regionClusters = result.get(TEST_REGION_1);
        assertEquals(2, regionClusters.size());
        assertTrue(regionClusters.stream().anyMatch(c -> c.getClusterName().equals(TEST_CLUSTER_1)));
        assertTrue(regionClusters.stream().anyMatch(c -> c.getClusterName().equals(TEST_CLUSTER_2)));
    }

    /**
     * Creates a ReadyClusterInfo object with the specified properties.
     * Uses default values for common fields if not specified.
     * 
     * @param clusterId The cluster ID
     * @param gpu The GPU type
     * @param clusterProvider The cluster provider
     * @return A ReadyClusterInfo object with the specified properties
     */
    private ReadyClusterInfo createReadyClusterInfo(String clusterId, String gpu, ClusterProviderEnum clusterProvider) {
        return ReadyClusterInfo.builder()
            .clusterId(clusterId)
            .gpu(gpu)
            .clusterProvider(clusterProvider)
            .clusterName(TEST_CLUSTER_NAME)
            .clusterGroupName(TEST_CLUSTER_GROUP)
            .attributes(new HashSet<>(Set.of(TEST_ATTRIBUTE)))
            .supportedNodeTypes(new HashSet<>(Set.of(NodeTypeEnum.SINGLE)))
            .build();
    }

    /**
     * Creates a ReadyClusterInfo object with the specified cluster name and region.
     * Uses default values for other fields.
     * 
     * @param clusterName The name of the cluster
     * @param region The region where the cluster is located
     * @return A ReadyClusterInfo object with the specified cluster name and region
     */
    private ReadyClusterInfo createReadyClusterInfo(String clusterName, String region) {
        return ReadyClusterInfo.builder()
            .clusterName(clusterName)
            .region(region)
            .gpu(TEST_GPU_NAME)
            .clusterProvider(ClusterProviderEnum.OCI)
            .clusterGroupName(TEST_CLUSTER_GROUP)
            .attributes(new HashSet<>(Set.of(TEST_ATTRIBUTE)))
            .supportedNodeTypes(new HashSet<>(Set.of(NodeTypeEnum.SINGLE)))
            .build();
    }

    /**
     * Creates a GpuCapacity object with the specified available capacity.
     * 
     * @param available The number of available GPUs
     * @return A GpuCapacity object with the specified available capacity
     */
    private GpuCapacity createGpuCapacity(int available) {
        GpuCapacity capacity = new GpuCapacity();
        capacity.setAvailable(available);
        return capacity;
    }

    /**
     * Creates an InstanceTypeV5Udt object with test data.
     * 
     * @param gpuCount The number of GPUs for this instance type
     * @return An InstanceTypeV5Udt object with the specified GPU count and common test values
     */
    private InstanceTypeV5Udt createInstanceTypeV5Udt(int gpuCount) {
        InstanceTypeV5Udt instanceType = new InstanceTypeV5Udt();
        instanceType.setValue("test-value");
        instanceType.setDescription("test description");
        instanceType.setCpuCores(4);
        instanceType.setSystemMemory("16G");
        instanceType.setGpuMemory("8G");
        instanceType.setGpuCount(gpuCount);
        instanceType.setOs("test-os");
        instanceType.setDriverVersion("test-driver");
        instanceType.setStorage("test-storage");
        instanceType.setNodeType("SINGLE");
        instanceType.setIsDefault(true);
        return instanceType;
    }

    /**
     * Asserts that all fields in the response instance type match the expected values.
     * 
     * @param responseInstanceType The response instance type to check
     * @param instanceType The source instance type containing expected values
     * @param gpuCount The expected GPU count
     */
    private void assertResponseInstanceType(@NotNull InstanceTypeAvailabilityResponse.InstanceType responseInstanceType,
                                            @NotNull InstanceTypeV5Udt instanceType,
                                            int gpuCount) {

        assertEquals(instanceType.getValue(), responseInstanceType.getValue());
        assertEquals(instanceType.getDescription(), responseInstanceType.getDescription());
        assertEquals(instanceType.getCpuCores(), responseInstanceType.getCpuCores());
        assertEquals(instanceType.getSystemMemory(), responseInstanceType.getSystemMemory());
        assertEquals(instanceType.getGpuMemory(), responseInstanceType.getGpuMemory());
        assertEquals(gpuCount, responseInstanceType.getGpuCount());
        assertEquals(instanceType.getOs(), responseInstanceType.getOs());
        assertEquals(instanceType.getDriverVersion(), responseInstanceType.getDriverVersion());
        assertEquals(instanceType.getStorage(), responseInstanceType.getStorage());
        assertEquals(InstanceTypeAvailabilityResponse.NodeType.SINGLE, responseInstanceType.getNodeType());
        assertEquals(instanceType.getIsDefault(), responseInstanceType.isDefaultable());
    }

    /**
     * Tests that getReadyClusterInfo correctly combines wildcard and NCA-specific clusters.
     * Creates two clusters:
     * - One wildcard cluster (available to all accounts)
     * - One NCA-specific cluster (available only to the specified NCA)
     * 
     * Verifies that:
     * 1. When NCA ID is WILDCARD, only wildcard clusters are returned
     * 2. When NCA ID is specific, both wildcard and NCA-specific clusters are returned
     * 3. Clusters are correctly converted to ReadyClusterInfo objects
     * 4. All cluster properties are preserved in the conversion
     */
    @Test
    void getReadyClusterInfo_CombinesWildcardAndNcaSpecificClusters() {
        // Given
        String ncaId = "test-nca-id";
        ClusterByGroupIdAndIdEntity wildcardCluster = createClusterEntity("wildcard-cluster", ClusterRepository.WILDCARD);
        ClusterByGroupIdAndIdEntity ncaSpecificCluster = createClusterEntity("nca-specific-cluster", ncaId);

        // Mock clusterTargetingHelper to return test clusters
        ClusterTargetingHelper mockClusterTargetingHelper = mock(ClusterTargetingHelper.class);
        when(mockClusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
            .thenReturn(Set.of(wildcardCluster));
        when(mockClusterTargetingHelper.getReadyClusterEntitiesForNcaId(ncaId))
            .thenReturn(Set.of(ncaSpecificCluster));

        IcmsConfigurationProperties mockNonByocConfig = mock(IcmsConfigurationProperties.class);
        // Default (unrestricted) GPU-to-NCA behavior so the new compute-platform filter is a no-op here.
        when(mockNonByocConfig.isNcaAllowedForGpu(anyString(), anyString())).thenReturn(true);

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(mockNonByocConfig, mockClusterTargetingHelper, null, null, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        // When
        Set<ReadyClusterInfo> result = clusterGpuInfoHelper.getReadyClusterInfo(ncaId);

        // Then
        assertEquals(2, result.size());
        assertTrue(result.stream().anyMatch(c -> c.getClusterName().equals("wildcard-cluster")));
        assertTrue(result.stream().anyMatch(c -> c.getClusterName().equals("nca-specific-cluster")));

        // Verify wildcard cluster properties
        ReadyClusterInfo wildcardInfo = result.stream()
            .filter(c -> c.getClusterName().equals("wildcard-cluster"))
            .findFirst()
            .orElseThrow();
        assertEquals(wildcardCluster.getClusterName(), wildcardInfo.getClusterName());
        assertEquals(wildcardCluster.getGpusV5().iterator().next().getName(), wildcardInfo.getGpu());
        assertEquals(wildcardCluster.getClusterGroupName(), wildcardInfo.getClusterGroupName());
        assertEquals(wildcardCluster.getRegion(), wildcardInfo.getRegion());
        assertEquals(wildcardCluster.getClusterProvider(), wildcardInfo.getClusterProvider());

        // Verify NCA-specific cluster properties
        ReadyClusterInfo ncaSpecificInfo = result.stream()
            .filter(c -> c.getClusterName().equals("nca-specific-cluster"))
            .findFirst()
            .orElseThrow();
        assertEquals(ncaSpecificCluster.getClusterName(), ncaSpecificInfo.getClusterName());
        assertEquals(ncaSpecificCluster.getGpusV5().iterator().next().getName(), ncaSpecificInfo.getGpu());
        assertEquals(ncaSpecificCluster.getClusterGroupName(), ncaSpecificInfo.getClusterGroupName());
        assertEquals(ncaSpecificCluster.getRegion(), ncaSpecificInfo.getRegion());
        assertEquals(ncaSpecificCluster.getClusterProvider(), ncaSpecificInfo.getClusterProvider());
    }

    /**
     * Tests that getReadyClusterInfo returns only wildcard clusters when NCA ID is WILDCARD.
     * Creates one wildcard cluster and verifies that:
     * 1. Only wildcard clusters are returned
     * 2. NCA-specific clusters are not included
     * 3. Cluster properties are correctly preserved
     */
    @Test
    void getReadyClusterInfo_WithWildcardNcaId_ReturnsOnlyWildcardClusters() {
        // Given
        ClusterByGroupIdAndIdEntity wildcardCluster = createClusterEntity("wildcard-cluster", ClusterRepository.WILDCARD);

        // Mock clusterTargetingHelper to return test cluster
        ClusterTargetingHelper mockClusterTargetingHelper = mock(ClusterTargetingHelper.class);
        when(mockClusterTargetingHelper.getWildCardAllowedClusterCachedInfo())
            .thenReturn(Set.of(wildcardCluster));

        IcmsConfigurationProperties mockNonByocConfig = mock(IcmsConfigurationProperties.class);
        // Default (unrestricted) GPU-to-NCA behavior so the new compute-platform filter is a no-op here.
        when(mockNonByocConfig.isNcaAllowedForGpu(anyString(), anyString())).thenReturn(true);

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(mockNonByocConfig, mockClusterTargetingHelper, null, null, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        // When
        Set<ReadyClusterInfo> result = clusterGpuInfoHelper.getReadyClusterInfo(ClusterRepository.WILDCARD);

        // Then
        assertEquals(1, result.size());
        ReadyClusterInfo clusterInfo = result.iterator().next();
        assertEquals(wildcardCluster.getClusterName(), clusterInfo.getClusterName());
        assertEquals(wildcardCluster.getGpusV5().iterator().next().getName(), clusterInfo.getGpu());
        assertEquals(wildcardCluster.getClusterGroupName(), clusterInfo.getClusterGroupName());
    }

    /**
     * A compute-platform cluster carrying a restricted GPU must be hidden from an NCA that
     * is not in the GPU's allowlist.
     */
    @Test
    void getReadyClusterInfo_hidesRestrictedGpuOnComputePlatform_forDisallowedNca() {
        String ncaId = "disallowed-nca";
        // createClusterEntity uses ClusterProviderEnum.OCI (a compute platform in the fixture)
        // and gpu == TEST_GPU_NAME.
        ClusterByGroupIdAndIdEntity platformCluster = createClusterEntity("platform-cluster", ncaId);

        ClusterTargetingHelper mockClusterTargetingHelper = mock(ClusterTargetingHelper.class);
        when(mockClusterTargetingHelper.getWildCardAllowedClusterCachedInfo()).thenReturn(Set.of());
        when(mockClusterTargetingHelper.getReadyClusterEntitiesForNcaId(ncaId))
                .thenReturn(Set.of(platformCluster));

        IcmsConfigurationProperties props = new IcmsConfigurationProperties();
        props.setGpuAllowedNcaIds(Map.of(TEST_GPU_NAME, List.of("allowed-nca")));

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(props, mockClusterTargetingHelper, null, null,
                COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        Set<ReadyClusterInfo> result = clusterGpuInfoHelper.getReadyClusterInfo(ncaId);

        assertTrue(result.isEmpty());
    }

    /**
     * A compute-platform cluster carrying a restricted GPU must be visible to an NCA that is
     * in the GPU's allowlist.
     */
    @Test
    void getReadyClusterInfo_showsRestrictedGpuOnComputePlatform_forAllowedNca() {
        String ncaId = "allowed-nca";
        ClusterByGroupIdAndIdEntity platformCluster = createClusterEntity("platform-cluster", ncaId);

        ClusterTargetingHelper mockClusterTargetingHelper = mock(ClusterTargetingHelper.class);
        when(mockClusterTargetingHelper.getWildCardAllowedClusterCachedInfo()).thenReturn(Set.of());
        when(mockClusterTargetingHelper.getReadyClusterEntitiesForNcaId(ncaId))
                .thenReturn(Set.of(platformCluster));

        IcmsConfigurationProperties props = new IcmsConfigurationProperties();
        props.setGpuAllowedNcaIds(Map.of(TEST_GPU_NAME, List.of("allowed-nca")));

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(props, mockClusterTargetingHelper, null, null,
                COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        Set<ReadyClusterInfo> result = clusterGpuInfoHelper.getReadyClusterInfo(ncaId);

        assertEquals(1, result.size());
    }

    /**
     * A BYOC cluster (non-compute-platform provider) carrying a restricted GPU must NOT be gated,
     * even for an NCA outside the allowlist.
     */
    @Test
    void getReadyClusterInfo_keepsRestrictedGpuOnByocCluster_forDisallowedNca() {
        String ncaId = "disallowed-nca";
        ClusterByGroupIdAndIdEntity byocCluster = createClusterEntity(
                "byoc-cluster", ncaId, TEST_CLUSTER_GROUP, ClusterProviderEnum.AWS, null);

        ClusterTargetingHelper mockClusterTargetingHelper = mock(ClusterTargetingHelper.class);
        when(mockClusterTargetingHelper.getWildCardAllowedClusterCachedInfo()).thenReturn(Set.of());
        when(mockClusterTargetingHelper.getReadyClusterEntitiesForNcaId(ncaId))
                .thenReturn(Set.of(byocCluster));

        IcmsConfigurationProperties props = new IcmsConfigurationProperties();
        props.setGpuAllowedNcaIds(Map.of(TEST_GPU_NAME, List.of("allowed-nca")));

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(props, mockClusterTargetingHelper, null, null,
                COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        Set<ReadyClusterInfo> result = clusterGpuInfoHelper.getReadyClusterInfo(ncaId);

        assertEquals(1, result.size());
    }

    /**
     * Creates a ClusterByGroupIdAndIdEntity object with test data.
     * 
     * @param clusterName The name of the cluster
     * @param ncaId The NVIDIA Cloud Account ID this cluster belongs to
     * @return A ClusterByGroupIdAndIdEntity object with the specified properties
     */
    private ClusterByGroupIdAndIdEntity createClusterEntity(String clusterName, String ncaId) {
        return createClusterEntity(clusterName, ncaId, TEST_CLUSTER_GROUP, ClusterProviderEnum.OCI, null);
    }

    private ClusterByGroupIdAndIdEntity createClusterEntity(
            String clusterId, String ncaId, String clusterGroupName,
            ClusterProviderEnum provider, Set<String> authorizedNcaIds) {
        ClusterByGroupIdAndIdEntity entity = new ClusterByGroupIdAndIdEntity();
        entity.setClusterName(clusterId);
        entity.setNcaId(ncaId);
        entity.setClusterGroupName(clusterGroupName);
        entity.setClusterStatus(ClusterStatusEnum.READY);
        entity.setRegion(TEST_REGION_1);
        entity.setClusterProvider(provider);
        if (authorizedNcaIds != null) {
            entity.setAuthorizedNcaIds(authorizedNcaIds);
        }

        ClusterByGroupIdAndIdKey key = ClusterByGroupIdAndIdKey.builder()
            .clusterId(clusterId)
            .clusterGroupId(clusterGroupName)
            .build();
        entity.setKey(key);

        GpuV5Udt gpu = GpuV5Udt.builder()
            .name(TEST_GPU_NAME)
            .instanceTypes(new HashSet<>(Set.of(createInstanceTypeV5Udt(ONE_GPU))))
            .build();
        entity.getGpusV5().add(gpu);

        return entity;
    }

    @Test
    void getActiveReservationsPerNcaId_DelegatesToNonByocReservationProcessor() {
        // Given
        String ncaId = "test-nca-id";
        ReservationProcessor mockProcessor = mock(ReservationProcessor.class);
        ReservationEntity activeReservation = ReservationEntity.builder()
                .reservationId(UUID.randomUUID())
                .ncaId(ncaId)
                .clusterId("test-cluster")
                .gpuType("test-gpu")
                .reservedGpuCount(1)
                .availableGpuCount(1.0)
                .startTime(Instant.now().minusSeconds(3600))
                .endTime(Instant.now().plusSeconds(3600))
                .build();
        List<ReservationEntity> expected = new ArrayList<>(List.of(activeReservation));
        when(mockProcessor.getActiveReservationsForNcaId(ncaId)).thenReturn(expected);

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(null, null, mockProcessor, null, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        // When
        List<ReservationEntity> result = clusterGpuInfoHelper.getActiveReservationsPerNcaId(ncaId);

        // Then
        assertEquals(expected, result);
        org.mockito.Mockito.verify(mockProcessor).getActiveReservationsForNcaId(ncaId);
    }

    /**
     * Creates a map of reservations by GPU and cluster ID.
     * 
     * @param gpuType The GPU type
     * @param clusterId The cluster ID
     * @param reservations The list of reservations for this GPU and cluster
     * @return A map structured as GPU -> Cluster -> List of Reservations
     */
    private Map<String, Map<String, List<ReservationEntity>>> createReservationMap(
            String gpuType,
            String clusterId,
            List<ReservationEntity> reservations) {
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = new HashMap<>();
        Map<String, List<ReservationEntity>> clusterReservations = new HashMap<>();
        clusterReservations.put(clusterId, reservations);
        reservationByGpuByClusterId.put(gpuType, clusterReservations);
        return reservationByGpuByClusterId;
    }

    /**
     * Creates a reservation entity with the specified properties.
     * 
     * @param ncaId The NVIDIA Cloud Account ID
     * @param clusterId The cluster ID
     * @param gpuType The GPU type
     * @param reservedGpuCount The total number of reserved GPUs
     * @param availableGpuCount The number of available GPUs
     * @param startTime The reservation start time
     * @param endTime The reservation end time
     * @return A ReservationEntity with the specified properties
     */
    private ReservationEntity createReservation(
            String ncaId,
            String clusterId,
            String gpuType,
            int reservedGpuCount,
            double availableGpuCount,
            Instant startTime,
            Instant endTime) {
        return ReservationEntity.builder()
            .reservationId(UUID.randomUUID())
            .ncaId(ncaId)
            .clusterId(clusterId)
            .gpuType(gpuType)
            .reservedGpuCount(reservedGpuCount)
            .availableGpuCount(availableGpuCount)
            .startTime(startTime)
            .endTime(endTime)
            .build();
    }

    /**
     * Creates a reservation entity with default values for common parameters.
     * Uses TEST_NCA_ID, TEST_CLUSTER_ID, and TEST_GPU_TYPE for the common fields.
     * 
     * @param availableGpuCount The number of available GPUs
     * @param isActive Whether the reservation should be active (true) or inactive (false)
     * @return A ReservationEntity with the specified available GPU count and default values for other fields
     */
    private ReservationEntity createReservation(double availableGpuCount, boolean isActive, String clusterId) {
        Instant now = Instant.now();
        Instant startTime = isActive ? now.minusSeconds(3600) : now.plusSeconds(3600);
        Instant endTime = isActive ? now.plusSeconds(3600) : now.plusSeconds(7200);
        return createReservation(
            TEST_NCA_ID, clusterId,
            TEST_GPU_TYPE,
            TEST_RESERVED_GPU_COUNT,
            availableGpuCount,
            startTime,
            endTime
        );
    }

    /**
     * Creates an active reservation entity with default values for common parameters.
     * Uses TEST_NCA_ID, TEST_CLUSTER_ID, and TEST_GPU_TYPE for the common fields.
     * 
     * @param availableGpuCount The number of available GPUs
     * @return A ReservationEntity with the specified available GPU count and default values for other fields
     */
    private ReservationEntity createReservation(double availableGpuCount, String clusterId) {
        return createReservation(availableGpuCount, true, clusterId);
    }

    /**
     * Tests that getAvailableCapacity returns reserved capacity for Non BYOC provider with active reservation.
     * Creates a Non BYOC cluster with an active reservation and verifies that:
     * 1. The reserved capacity is returned when the cluster has an active reservation
     * 2. The capacity is correctly calculated from the reservation
     */
    @Test
    void getAvailableCapacity_WithNonByocProviderAndActiveReservation_ReturnsReservedCapacity() {
        // Given
        // Create ReadyClusterInfo for Non BYOC provider
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);

        // Create active reservation
        ReservationEntity reservation = createReservation(TEST_RESERVED_CAPACITY, TEST_CLUSTER_ID);

        // Create reservation map
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation));

        // Create healthy cloud health
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);

        // Mock configuration (flag disabled for backward compatibility)
        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(false);

        // Mock healthy reservation capacity computation
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation))
            .thenReturn((double) TEST_RESERVED_CAPACITY);
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(null, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        // When
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);

        // Then
        assertEquals(TEST_RESERVED_CAPACITY, result);
    }

    /**
     * Tests that getAvailableCapacity returns SPOT capacity for Non BYOC provider with no reservation.
     * Creates a Non BYOC cluster with no reservation and verifies that:
     * 1. SPOT capacity is returned when the cluster has no reservation
     */
    @Test
    void getAvailableCapacity_WithNonByocProviderAndNoReservation_ReturnsAvailableFallbackCapacity() {
        // Given
        // Create ReadyClusterInfo for Non BYOC provider
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);

        // Create empty reservation map
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = new HashMap<>();

        // Create healthy cloud health with available capacity
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(AVAILABLE_CAPACITY));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);

        // Mock configuration - no need to check flag since there's no reservation
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, null, null, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        // When
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);

        // Then
        assertEquals(AVAILABLE_CAPACITY, result);
    }

    /**
     * Tests that getAvailableCapacity returns available capacity for BYOC provider.
     * Creates a BYOC cluster with available capacity and verifies that:
     * 1. The available capacity is returned directly from the cluster
     * 2. No reservation logic is applied for BYOC providers
     */
    @Test
    void getAvailableCapacity_WithByocProvider_ReturnsAvailableFallbackCapacity() {
        // Given
        // Create ReadyClusterInfo for BYOC provider
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.AWS);

        // Create empty reservation map
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = new HashMap<>();

        // Create healthy cloud health with available capacity
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(AVAILABLE_CAPACITY));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);

        // When
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);

        // Then
        assertEquals(AVAILABLE_CAPACITY, result);
    }


    /**
     * Tests that getAvailableCapacity returns zero for cluster with no available capacity.
     * Creates a Non BYOC cluster with an active reservation but zero available capacity and verifies that:
     * 1. Zero capacity is returned when the reservation has no available capacity
     * 2. The cluster is skipped even if it has an active reservation
     */
    @Test
    void getAvailableCapacity_WithNoAvailableCapacity_ReturnsZero() {
        // Given
        // Create ReadyClusterInfo for Non BYOC provider
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);

        // Create active reservation with zero available capacity
        ReservationEntity reservation = createReservation(ZERO_CAPACITY, TEST_CLUSTER_ID);

        // Create reservation map
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation));

        // Create healthy cloud health
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);

        // Mock configuration (flag disabled for backward compatibility)
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(false);

        // Mock healthy reservation capacity computation to 0
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation))
            .thenReturn(0.0);
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        // When
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);

        // Then
        assertEquals(ZERO_CAPACITY, result);
    }

    /**
     * Tests that getAvailableCapacity sums up capacity from multiple reservations.
     * Creates a Non BYOC cluster with multiple active reservations and verifies that:
     * 1. The total capacity is the sum of all active reservations
     * 2. All reservations are considered when calculating capacity
     */
    @Test
    void getAvailableCapacity_WithMultipleActiveAndHealthyReservations_SumsUpCapacity() {
        // Given
        // Create ReadyClusterInfo for Non BYOC provider
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);

        // Create multiple active reservations
        ReservationEntity reservation1 = createReservation(TEST_CAPACITY_1, TEST_CLUSTER_ID);
        ReservationEntity reservation2 = createReservation(TEST_CAPACITY_2, TEST_CLUSTER_ID);

        // Create reservation map
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation1, reservation2));

        // Create healthy cloud health
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);

        // Mock configuration (flag disabled for backward compatibility)
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(false);

        // Mock healthy reservation capacity computation per reservation
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation1))
            .thenReturn((double) TEST_CAPACITY_1);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation2))
            .thenReturn((double) TEST_CAPACITY_2);
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        // When
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);

        // Then
        assertEquals(TEST_CAPACITY_1 + TEST_CAPACITY_2, result);
    }

    /**
     * Tests that getAvailableCapacity from active instances for reservation from unhealthy zone.
     * Creates a unhealthy Non BYOC cluster with active reservations and verifies that:
     * 1. total capacity is equal to capacity calculated from active instances
     * 2. Reservation is considered while capacity calculation even if cluster is unhealthy
     */
    @Test
    void getAvailableCapacity_WithActiveAndUnHealthyReservationsAndReservationBackupEnabled_considerCapacityFromActiveInstances() {
        // Given
        // Create ReadyClusterInfo for Non BYOC provider
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);

        // Create multiple active reservations
        ReservationEntity reservation1 = createReservation(TEST_CAPACITY_1, TEST_CLUSTER_ID);

        // Create reservation map
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId =
                createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation1));

        // Create healthy cloud health
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.UNHEALTHY);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);

        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForUnhealthyZone(reservation1)).thenReturn(3.0);

        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);
        when(nonByocConfigurationProperties.isReservationBackupEnabled()).thenReturn(true);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(true);

        // When
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);

        // Then
        assertEquals(3, result);
    }

    /**
     * Tests that getAvailableCapacity from active instances for reservation from unhealthy zone.
     * Creates a unhealthy Non BYOC cluster with active reservations and verifies that:
     * 1. total capacity is equal to capacity calculated from active instances
     * 2. Reservation is considered while capacity calculation even if cluster is unhealthy
     */
    @Test
    void getAvailableCapacity_WithActiveAndUnHealthyReservationsAndBackupReservationDisabled_ignoreCluster() {
        // Given
        // Create ReadyClusterInfo for Non BYOC provider
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);

        // Create multiple active reservations
        ReservationEntity reservation1 = createReservation(TEST_CAPACITY_1, TEST_CLUSTER_ID);

        // Create reservation map
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId =
                createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation1));

        // Create healthy cloud health
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.UNHEALTHY);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);

        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(false);

        // When
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, null, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);

        // Then
        assertEquals(0, result);
    }

    /**
     * Tests that includeClusterBasedOnAccessLevel returns true when BYOC public clusters are included.
     * Verifies that:
     * 1. When the configuration allows BYOC public clusters and the cluster is BYOC, returns true
     * 2. The cluster group name is checked correctly
     */
    @Test
    void includeClusterBasedOnAccessLevel_WhenByocPublicClustersIncluded_ReturnsTrue() {
        // Given
        IcmsConfigurationProperties mockProperties = mock(IcmsConfigurationProperties.class);
        when(mockProperties.isIncludeCustomPublicClustersInAccountInfoApis()).thenReturn(true);

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(mockProperties, null, null, null, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        InstanceTypeAvailabilityResponse.Cluster cluster = new InstanceTypeAvailabilityResponse.Cluster();
        cluster.setClusterGroup("test-group");

        // When
        boolean result = clusterGpuInfoHelper.includeClusterBasedOnAccessLevel(cluster);

        // Then
        assertTrue(result);
    }

    /**
     * Tests that includeClusterBasedOnAccessLevel returns false when BYOC public clusters are not included.
     * Verifies that:
     * 1. When the configuration disallows BYOC public clusters, returns false
     * 2. The cluster group name is checked correctly
     */
    @Test
    void includeClusterBasedOnAccessLevel_WhenByocPublicClustersNotIncluded_ReturnsFalse() {
        // Given
        IcmsConfigurationProperties mockProperties = mock(IcmsConfigurationProperties.class);
        when(mockProperties.isIncludeCustomPublicClustersInAccountInfoApis()).thenReturn(false);

        clusterGpuInfoHelper = new ClusterGpuInfoHelper(mockProperties, null, null, null, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);

        InstanceTypeAvailabilityResponse.Cluster cluster = new InstanceTypeAvailabilityResponse.Cluster();
        cluster.setClusterGroup("test-group");

        // When
        boolean result = clusterGpuInfoHelper.includeClusterBasedOnAccessLevel(cluster);

        // Then
        assertFalse(result);
    }

    /**
     * Tests that toInstanceTypeDetails correctly maps all fields from instance type and GPU.
     * Verifies that:
     * 1. All fields from InstanceType are correctly copied
     * 2. GPU name and CPU architecture are correctly set from GPU
     * 3. Node type is correctly set to SINGLE
     * 4. Defaultable flag is correctly set
     */
    @Test
    void toInstanceTypeDetails_MapsAllFieldsCorrectly() {
        // Given
        InstanceTypeAvailabilityResponse.InstanceType instanceType = new InstanceTypeAvailabilityResponse.InstanceType();
        instanceType.setInstanceName("test-instance");
        instanceType.setValue("test-value");
        instanceType.setDescription("test description");
        instanceType.setCpuCores(4);
        instanceType.setSystemMemory("16G");
        instanceType.setGpuMemory("8G");
        instanceType.setGpuCount(2);
        instanceType.setOs("test-os");
        instanceType.setDriverVersion("test-driver");
        instanceType.setStorage("test-storage");
        instanceType.setNodeType(InstanceTypeAvailabilityResponse.NodeType.SINGLE);
        instanceType.setDefaultable(true);

        InstanceTypeAvailabilityResponse.Gpu gpu = new InstanceTypeAvailabilityResponse.Gpu();
        gpu.setGpuName("test-gpu");

        // When
        InstanceTypeDetails result = clusterGpuInfoHelper.toInstanceTypeDetails(instanceType, gpu);

        // Then
        assertEquals(instanceType.getInstanceName(), result.getName());
        assertEquals(instanceType.getValue(), result.getValue());
        assertEquals(instanceType.getDescription(), result.getDescription());
        assertEquals(instanceType.getCpuCores(), result.getCpuCores());
        assertEquals(instanceType.getSystemMemory(), result.getSystemMemory());
        assertEquals(instanceType.getGpuMemory(), result.getGpuMemory());
        assertEquals(instanceType.getGpuCount(), result.getGpuCount());
        assertEquals(0, result.getAvailableCapacity());
        assertEquals(gpu.getGpuName(), result.getGpuName());
        assertEquals(instanceType.isDefaultable(), result.getDefaultable());
        assertEquals(instanceType.getCpuArch(), result.getCpuArch());
        assertEquals(instanceType.getOs(), result.getOs());
        assertEquals(instanceType.getDriverVersion(), result.getDriverVersion());
        assertEquals(instanceType.getStorage(), result.getStorage());
        assertEquals(NodeTypeEnum.SINGLE, result.getNodeType());
        assertNotNull(result.getAttributes());
        assertTrue(result.getAttributes().isEmpty());
        assertNotNull(result.getRegions());
        assertTrue(result.getRegions().isEmpty());
        assertNotNull(result.getClusters());
        assertTrue(result.getClusters().isEmpty());
    }

    /**
     * Tests that toInstanceTypeDetails correctly handles MULTI node type.
     * Verifies that:
     * 1. Node type is correctly set to MULTI when specified
     * 2. Other fields remain unchanged
     */
    @Test
    void toInstanceTypeDetails_WithMultiNodeType_SetsCorrectNodeType() {
        // Given
        InstanceTypeAvailabilityResponse.InstanceType instanceType = new InstanceTypeAvailabilityResponse.InstanceType();
        instanceType.setNodeType(InstanceTypeAvailabilityResponse.NodeType.MULTI);
        instanceType.setDefaultable(true);

        InstanceTypeAvailabilityResponse.Gpu gpu = new InstanceTypeAvailabilityResponse.Gpu();
        gpu.setGpuName("test-gpu");

        // When
        InstanceTypeDetails result = clusterGpuInfoHelper.toInstanceTypeDetails(instanceType, gpu);

        // Then
        assertEquals(NodeTypeEnum.MULTI, result.getNodeType());
    }

    /**
     * Tests that toInstanceTypeDetails correctly handles non-defaultable instance type.
     * Verifies that:
     * 1. Defaultable flag is correctly set to false
     * 2. Other fields remain unchanged
     */
    @Test
    void toInstanceTypeDetails_WithNonDefaultableInstanceType_SetsDefaultableToFalse() {
        // Given
        InstanceTypeAvailabilityResponse.InstanceType instanceType = new InstanceTypeAvailabilityResponse.InstanceType();
        instanceType.setNodeType(InstanceTypeAvailabilityResponse.NodeType.SINGLE);
        instanceType.setDefaultable(false);

        InstanceTypeAvailabilityResponse.Gpu gpu = new InstanceTypeAvailabilityResponse.Gpu();
        gpu.setGpuName("test-gpu");

        // When
        InstanceTypeDetails result = clusterGpuInfoHelper.toInstanceTypeDetails(instanceType, gpu);

        // Then
        assertFalse(result.getDefaultable());
    }

    /**
     * Creates an InstanceTypeUdt object with test data.
     * 
     * @param gpuCount The number of GPUs for this instance type
     * @return An InstanceTypeUdt object with the specified GPU count and common test values
     */
    private InstanceTypeUdt createInstanceTypeUdt(int gpuCount) {
        InstanceTypeUdt instanceType = new InstanceTypeUdt();
        instanceType.setValue("test-value");
        instanceType.setDescription("test description");
        instanceType.setCpuCores(4);
        instanceType.setSystemMemory("16G");
        instanceType.setGpuMemory("8G");
        instanceType.setGpuCount(gpuCount);
        instanceType.setName("test-name");
        instanceType.setIsDefault(true);
        return instanceType;
    }

    // ========================================
    // Tests for spotPostReservedExhaustionForFunctionEnabled flag
    // ========================================

    /**
     * Tests that when the spotPostReservedExhaustion not allowed for NCA ID,
     * only reserved capacity is returned, even if spot capacity is available.
     * 
     * Scenario:
     * - Non BYOC cluster with active reservation: reserved capacity = 5
     * - Healthy cluster with available spot capacity = 10
     *
     * Expected: Returns reserved capacity only (5)
     */
    @Test
    void getAvailableCapacity_FallbackCapacityAfterReservationExhaustionNotAllowed_WithReservedCapacity_ReturnsOnlyReservedCapacity() {
        // Prepare
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);
        
        // Create active reservation with 5 GPUs available
        ReservationEntity reservation = createReservation(5.0, TEST_CLUSTER_ID);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation));
        
        // Create healthy cloud health with 10 spot GPUs available
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(10));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);
        
        // Mock configuration and reservation processor
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);
        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(false);
        
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation)).thenReturn(5.0);
        
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        
        // Act
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);
        
        // Assert
        assertEquals(5, result, "Should return only reserved capacity when flag is disabled");
    }

    /**
     * Tests that when the spotPostReservedExhaustion usage allowed for NCA ID,
     * reserved capacity + spot capacity are returned together.
     * 
     * Scenario:
     * - Non BYOC cluster with active reservation: reserved capacity = 5
     * - Healthy cluster with available spot capacity = 10
     *
     * Expected: Returns reserved + spot capacity (5 + 10 = 15)
     */
    @Test
    void getAvailableCapacity_FallbackCapacityAfterReservationExhaustionAllowed_WithReservedCapacity_ReturnsCombinedCapacity() {
        // Prepare
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);
        
        // Create active reservation with 5 GPUs available
        ReservationEntity reservation = createReservation(5.0, TEST_CLUSTER_ID);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation));
        
        // Create healthy cloud health with 10 spot GPUs available
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(10));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);
        
        // Mock configuration and reservation processor
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(true);
        
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation)).thenReturn(5.0);
        
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        
        // Act
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);
        
        // Assert
        assertEquals(15, result, "Should return reserved + spot capacity when flag is enabled (5 + 10 = 15)");
    }

    /**
     * Tests that when the spotPostReservedExhaustion usage allowed for NCA ID,
     * and reserved capacity is EXHAUSTED (0), reserved + spot capacity is returned.
     * 
     * Scenario:
     * - Non BYOC cluster with active reservation: reserved capacity = 0 (exhausted)
     * - Healthy cluster with available spot capacity = 10
     *
     * Expected: Returns 0 + 10 = 10 (combined capacity)
     */
    @Test
    void getAvailableCapacity_FallbackCapacityAfterReservationExhaustionAllowed_WithExhaustedReservedCapacity_ReturnsFallbackCapacity() {
        // Prepare
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);
        
        // Create active reservation with 0 GPUs available (exhausted)
        ReservationEntity reservation = createReservation(0.0, TEST_CLUSTER_ID);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation));
        
        // Create healthy cloud health with 10 spot GPUs available
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(10));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);
        
        // Mock configuration and reservation processor
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(true);
        
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation)).thenReturn(0.0);
        
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        
        // Act
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);
        
        // Assert
        assertEquals(10, result, "Should return reserved + spot capacity (0 + 10 = 10) when flag is enabled");
    }

    /**
     * Tests that when the spotPostReservedExhaustion usage not allowed for NCA ID,
     * and reserved capacity is EXHAUSTED (0), zero is returned (no fallback to spot).
     * 
     * Scenario:
     * - Non BYOC cluster with active reservation: reserved capacity = 0 (exhausted)
     * - Healthy cluster with available spot capacity = 10
     *
     * Expected: Returns 0 (strict reservation enforcement, no fallback)
     */
    @Test
    void getAvailableCapacity_FallbackCapacityAfterReservationExhaustionNotAllowed_WithExhaustedReservedCapacity_ReturnsZero() {
        // Prepare
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);
        
        // Create active reservation with 0 GPUs available (exhausted)
        ReservationEntity reservation = createReservation(0.0, TEST_CLUSTER_ID);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation));
        
        // Create healthy cloud health with 10 spot GPUs available
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(10));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);
        
        // Mock configuration and reservation processor
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(false);
        
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation)).thenReturn(0.0);
        
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        
        // Act
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);
        
        // Assert
        assertEquals(0, result, "Should return 0 when reserved is exhausted and flag is disabled (no fallback)");
    }

    /**
     * Tests that when the spotPostReservedExhaustion usage allowed for NCA ID,
     * reserved capacity is exhausted, but spot capacity is also unavailable.
     * 
     * Scenario:
     * - Non BYOC cluster with active reservation: reserved capacity = 0 (exhausted)
     * - Unhealthy cluster (no spot capacity available)
     *
     * Expected: Returns 0 + 0 = 0 (combined capacity when both are unavailable)
     */
    @Test
    void getAvailableCapacity_FallbackCapacityAfterReservationExhaustionAllowed_WithExhaustedReservedAndNoFallbackCapacity_ReturnsZero() {
        // Prepare
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);
        
        // Create active reservation with 0 GPUs available (exhausted)
        ReservationEntity reservation = createReservation(0.0, TEST_CLUSTER_ID);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation));
        
        // Create UNHEALTHY cloud health (no spot capacity available)
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.UNHEALTHY);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);
        
        // Mock configuration
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(true);
        
        // Note: No need to mock reservationProcessor for unhealthy cluster
        // because calculateAvailableCapacityForHealthyZone is never called for unhealthy clusters
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        
        // Act
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);
        
        // Assert
        assertEquals(0, result, "Should return reserved + spot capacity (0 + 0 = 0) when both are unavailable");
    }

    /**
     * Tests that when the spotPostReservedExhaustion usage allowed for NCA ID,
     * reserved capacity is exhausted, and spot capacity is zero.
     * 
     * Scenario:
     * - Non BYOC cluster with active reservation: reserved capacity = 0 (exhausted)
     * - Healthy cluster with spot capacity = 0
     * 
     * Expected: Returns 0 + 0 = 0 (combined capacity when both are zero)
     */
    @Test
    void getAvailableCapacity_FallbackCapacityAfterReservationExhaustionAllowed_WithExhaustedReservedAndZeroFallbackCapacity_ReturnsZero() {
        // Prepare
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);
        
        // Create active reservation with 0 GPUs available (exhausted)
        ReservationEntity reservation = createReservation(0.0, TEST_CLUSTER_ID);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation));
        
        // Create healthy cloud health with 0 spot GPUs available
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(0));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);
        
        // Mock configuration and reservation processor
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(true);
        
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation)).thenReturn(0.0);
        
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        
        // Act
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);
        
        // Assert
        assertEquals(0, result, "Should return reserved + spot capacity (0 + 0 = 0) when both are zero");
    }

    /**
     * Tests that when the FallbackCapacityAfterReservationExhaustionAllowed and there are multiple reservations,
     * returns the sum of reserved capacities + spot capacity.
     * 
     * Scenario:
     * - Non BYOC cluster with two active reservations: 3 + 2 = 5 total reserved
     * - Healthy cluster with available spot capacity = 10
     * - Flag ENABLED
     * 
     * Expected: Returns total reserved + spot capacity (5 + 10 = 15)
     */
    @Test
    void getAvailableCapacity_FallbackCapacityAfterReservationExhaustionAllowed_WithMultipleReservationsNotExhausted_ReturnsCombinedCapacity() {
        // Prepare
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);
        
        // Create multiple active reservations with available capacity
        ReservationEntity reservation1 = createReservation(3.0, TEST_CLUSTER_ID);
        ReservationEntity reservation2 = createReservation(2.0, TEST_CLUSTER_ID);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation1, reservation2));
        
        // Create healthy cloud health with 10 spot GPUs available
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(10));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);
        
        // Mock configuration and reservation processor
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(true);
        
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation1)).thenReturn(3.0);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation2)).thenReturn(2.0);
        
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        
        // Act
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);
        
        // Assert
        assertEquals(15, result, "Should return sum of reserved + spot capacity (5 + 10 = 15) when flag is enabled");
    }

    /**
     * Tests that when FallbackCapacityAfterReservationExhaustionAllowed and there are multiple reservations ALL exhausted,
     * reserved + spot capacity is returned.
     * 
     * Scenario:
     * - Non BYOC cluster with two active reservations: both with 0 capacity (all exhausted)
     * - Healthy cluster with available spot capacity = 10
     * - Flag ENABLED
     * 
     * Expected: Returns 0 + 10 = 10 (combined capacity)
     */
    @Test
    void getAvailableCapacity_FallbackCapacityAfterReservationExhaustionAllowed_WithMultipleReservationsAllExhausted_ReturnsFallbackCapacity() {
        // Prepare
        ReadyClusterInfo readyClusterInfo = createReadyClusterInfo(TEST_CLUSTER_ID, TEST_GPU_TYPE, ClusterProviderEnum.OCI);
        
        // Create multiple active reservations with 0 available capacity (all exhausted)
        ReservationEntity reservation1 = createReservation(0.0, TEST_CLUSTER_ID);
        ReservationEntity reservation2 = createReservation(0.0, TEST_CLUSTER_ID);
        Map<String, Map<String, List<ReservationEntity>>> reservationByGpuByClusterId = 
            createReservationMap(TEST_GPU_TYPE, TEST_CLUSTER_ID, List.of(reservation1, reservation2));
        
        // Create healthy cloud health with 10 spot GPUs available
        Map<String, CloudHealthEntity> cloudHealthByClusterId = new HashMap<>();
        CloudHealthEntity cloudHealth = new CloudHealthEntity();
        cloudHealth.setStatus(CloudHealthStatus.HEALTHY);
        Map<String, GpuCapacity> gpuUsage = new HashMap<>();
        gpuUsage.put(TEST_GPU_TYPE, createGpuCapacity(10));
        cloudHealth.setGpuUsage(gpuUsage);
        cloudHealthByClusterId.put(TEST_CLUSTER_ID, cloudHealth);
        
        // Mock configuration and reservation processor
        IcmsConfigurationProperties nonByocConfigurationProperties = mock(IcmsConfigurationProperties.class);

        InstanceLifecycleHelper instanceLifecycleHelper = mock(InstanceLifecycleHelper.class);
        when(instanceLifecycleHelper.useSpotCapacityPostReservedExhausted(TEST_NCA_ID)).thenReturn(true);
        
        ReservationProcessor reservationProcessor = mock(ReservationProcessor.class);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation1)).thenReturn(0.0);
        when(reservationProcessor.calculateAvailableCapacityForHealthyZone(reservation2)).thenReturn(0.0);
        
        clusterGpuInfoHelper = new ClusterGpuInfoHelper(nonByocConfigurationProperties, null, reservationProcessor, instanceLifecycleHelper, COMPUTE_PLATFORM_SERVICE_NON_BYOC, NON_BYOC_AUTHZ_NOOP);
        
        // Act
        int result = clusterGpuInfoHelper.getAvailableCapacity(TEST_NCA_ID, readyClusterInfo, reservationByGpuByClusterId, cloudHealthByClusterId);
        
        // Assert
        assertEquals(10, result, "Should return reserved + spot capacity (0 + 10 = 10) when flag is enabled");
    }
} 