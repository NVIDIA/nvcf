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

import com.nvidia.icms.inbound.rest.model.InstanceTypeDetails;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.HashSet;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
class AccountInfoServiceTest {

    private static final String TEST_REGION = "us-west-1";
    private static final String TEST_CLUSTER = "test-cluster";

    @Mock
    private ClusterGpuInfoHelper clusterGpuInfoHelper;

    private AccountInfoService accountInfoService;

    @BeforeEach
    void setUp() {
        accountInfoService = new AccountInfoService(null, null, clusterGpuInfoHelper,
                ComputePlatformTestFixtures.nonByocComputePlatformService());
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