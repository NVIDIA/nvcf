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
package com.nvidia.icms.inbound.rest.model.account;

import java.util.HashSet;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.*;
import java.util.ArrayList;

class InstanceTypeAvailabilityResponseTest {

    private InstanceTypeAvailabilityResponse response;

    @BeforeEach
    void setUp() {
        response = new InstanceTypeAvailabilityResponse();
    }

    @Test
    void testInitialState() {
        assertNotNull(response.getGpus());
        assertTrue(response.getGpus().isEmpty());
    }

    @Test
    void testFindOrCreateGpu() {
        // Test creating a new GPU
        InstanceTypeAvailabilityResponse.Gpu gpu = response .findOrCreateGpu("RTX 4090");
        assertNotNull(gpu);
        assertEquals("RTX 4090", gpu.getGpuName());
        assertNotNull(gpu.getInstanceTypes());
        assertTrue(gpu.getInstanceTypes().isEmpty());

        // Test finding existing GPU
        InstanceTypeAvailabilityResponse.Gpu sameGpu = response.findOrCreateGpu("RTX 4090");
        assertSame(gpu, sameGpu);
        assertEquals(1, response.getGpus().size());
    }

    @Test
    void testFindOrCreateInstanceType() {
        InstanceTypeAvailabilityResponse.Gpu gpu = response.findOrCreateGpu("RTX 4090");
        
        // Test creating a new instance type
        InstanceTypeAvailabilityResponse.InstanceType instanceType = gpu.findOrCreateInstanceType("g4dn.xlarge");
        assertNotNull(instanceType);
        assertEquals("g4dn.xlarge", instanceType.getInstanceName());
        assertNotNull(instanceType.getRegions());
        assertTrue(instanceType.getRegions().isEmpty());

        // Test finding existing instance type
        InstanceTypeAvailabilityResponse.InstanceType sameInstanceType = gpu.findOrCreateInstanceType("g4dn.xlarge");
        assertSame(instanceType, sameInstanceType);
        assertEquals(1, gpu.getInstanceTypes().size());
    }

    @Test
    void testFindOrCreateRegion() {
        InstanceTypeAvailabilityResponse.Gpu gpu = response.findOrCreateGpu("RTX 4090");
        InstanceTypeAvailabilityResponse.InstanceType instanceType = gpu.findOrCreateInstanceType("g4dn.xlarge");
        
        // Test creating a new region
        InstanceTypeAvailabilityResponse.Region region = instanceType.findOrCreateRegion("us-west-2");
        assertNotNull(region);
        assertEquals("us-west-2", region.getRegionName());
        assertNotNull(region.getClusters());
        assertTrue(region.getClusters().isEmpty());

        // Test finding existing region
        InstanceTypeAvailabilityResponse.Region sameRegion = instanceType.findOrCreateRegion("us-west-2");
        assertSame(region, sameRegion);
        assertEquals(1, instanceType.getRegions().size());
    }

    @Test
    void testFindOrCreateCluster() {
        InstanceTypeAvailabilityResponse.Gpu gpu = response.findOrCreateGpu("RTX 4090");
        InstanceTypeAvailabilityResponse.InstanceType instanceType = gpu.findOrCreateInstanceType("g4dn.xlarge");
        InstanceTypeAvailabilityResponse.Region region = instanceType.findOrCreateRegion("us-west-2");
        
        // Test creating a new cluster
        InstanceTypeAvailabilityResponse.Cluster cluster = region.findOrCreateCluster("cluster-1");
        assertNotNull(cluster);
        assertEquals("cluster-1", cluster.getClusterId());

        // Test finding existing cluster
        InstanceTypeAvailabilityResponse.Cluster sameCluster = region.findOrCreateCluster("cluster-1");
        assertSame(cluster, sameCluster);
        assertEquals(1, region.getClusters().size());
    }

    @Test
    void testBuilder() {
        InstanceTypeAvailabilityResponse response = InstanceTypeAvailabilityResponse.builder()
                .gpus(new HashSet<>())
                .build();
        assertNotNull(response.getGpus());
        assertTrue(response.getGpus().isEmpty());
    }

    @Test
    void testNodeTypeEnum() {
        assertEquals(2, InstanceTypeAvailabilityResponse.NodeType.values().length);
        assertNotNull(InstanceTypeAvailabilityResponse.NodeType.valueOf("SINGLE"));
        assertNotNull(InstanceTypeAvailabilityResponse.NodeType.valueOf("MULTI"));
    }
} 