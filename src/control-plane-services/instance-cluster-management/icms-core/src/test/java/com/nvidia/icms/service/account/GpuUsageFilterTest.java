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

import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;

class GpuUsageFilterTest {

    private static final String TEST_GPU_NAME = "NVIDIA-A100";
    private static final String TEST_CLUSTER_GROUP = "test-cluster-group";
    private static final String TEST_INSTANCE_TYPE = "g4dn.xlarge";
    private static final String TEST_REGION = "us-west-2";
    private static final String TEST_CLUSTER = "test-cluster";
    private static final String TEST_ATTRIBUTE = "test-attribute";

    private GpuUsageFilter filter;

    @BeforeEach
    void setUp() {
        filter = new GpuUsageFilter();
    }

    @Test
    void testIsGpuNameAllowed_WhenFilterIsNull_ReturnsTrue() {
        assertTrue(filter.isGpuNameAllowed(TEST_GPU_NAME));
    }

    @Test
    void testIsGpuNameAllowed_WhenValueMatches_ReturnsTrue() {
        filter.setGpuNames(Set.of(TEST_GPU_NAME));
        assertTrue(filter.isGpuNameAllowed(TEST_GPU_NAME));
    }

    @Test
    void testIsGpuNameAllowed_WhenValueDoesNotMatch_ReturnsFalse() {
        filter.setGpuNames(Set.of("different-gpu"));
        assertFalse(filter.isGpuNameAllowed(TEST_GPU_NAME));
    }

    @ParameterizedTest
    @ValueSource(strings = {"", "  ", "\t", "\n"})
    void testIsGpuNameAllowed_WithBlankValues(String blankValue) {
        filter.setGpuNames(Set.of(blankValue));
        assertTrue(filter.isGpuNameAllowed(""));
        assertTrue(filter.isGpuNameAllowed(null));
        assertTrue(filter.isGpuNameAllowed("  "));
    }

    @Test
    void testIsClusterGroupNameAllowed_WhenFilterIsNull_ReturnsTrue() {
        assertTrue(filter.isClusterGroupNameAllowed(TEST_CLUSTER_GROUP));
    }

    @Test
    void testIsClusterGroupNameAllowed_WhenValueMatches_ReturnsTrue() {
        filter.setClusterGroupNames(Set.of(TEST_CLUSTER_GROUP));
        assertTrue(filter.isClusterGroupNameAllowed(TEST_CLUSTER_GROUP));
    }

    @Test
    void testIsInstanceTypeAllowed_WhenFilterIsNull_ReturnsTrue() {
        assertTrue(filter.isInstanceTypeAllowed(TEST_INSTANCE_TYPE));
    }

    @Test
    void testIsInstanceTypeAllowed_WhenValueMatches_ReturnsTrue() {
        filter.setInstanceTypes(Set.of(TEST_INSTANCE_TYPE));
        assertTrue(filter.isInstanceTypeAllowed(TEST_INSTANCE_TYPE));
    }

    @Test
    void testIsRegionNameAllowed_WhenFilterIsNull_ReturnsTrue() {
        assertTrue(filter.isRegionNameAllowed(TEST_REGION));
    }

    @Test
    void testIsRegionNameAllowed_WhenValueMatches_ReturnsTrue() {
        filter.setRegionNames(Set.of(TEST_REGION));
        assertTrue(filter.isRegionNameAllowed(TEST_REGION));
    }

    @Test
    void testIsClusterNameAllowed_WhenFilterIsNull_ReturnsTrue() {
        assertTrue(filter.isClusterNameAllowed(TEST_CLUSTER));
    }

    @Test
    void testIsClusterNameAllowed_WhenValueMatches_ReturnsTrue() {
        filter.setClusterNames(Set.of(TEST_CLUSTER));
        assertTrue(filter.isClusterNameAllowed(TEST_CLUSTER));
    }

    @Test
    void testAreAttributesAllowed_WhenFilterIsNull_ReturnsTrue() {
        assertTrue(filter.areAttributesAllowed(Set.of(TEST_ATTRIBUTE)));
    }

    @Test
    void testAreAttributesAllowed_WhenAllValuesMatch_ReturnsTrue() {
        filter.setAttributes(Set.of(TEST_ATTRIBUTE));
        assertTrue(filter.areAttributesAllowed(Set.of(TEST_ATTRIBUTE, "another-attribute")));
    }

    @Test
    void testAreAttributesAllowed_WhenSomeValuesDoNotMatch_ReturnsFalse() {
        filter.setAttributes(Set.of(TEST_ATTRIBUTE,  "non-matching-attribute"));
        assertFalse(filter.areAttributesAllowed(Set.of(TEST_ATTRIBUTE)));
    }

    @Test
    void testIsInstanceUsageAllowed_WhenFilterIsNull_ReturnsTrue() {
        assertTrue(filter.isInstanceUsageAllowed(Set.of(NodeTypeEnum.SINGLE)));
    }

    @Test
    void testIsInstanceUsageAllowed_WhenValueMatches_ReturnsTrue() {
        filter.setInstanceTypeUsageFilter(InstanceTypeUsageEnum.DEFAULT);
        assertTrue(filter.isInstanceUsageAllowed(Set.of(NodeTypeEnum.SINGLE)));
    }

    @Test
    void testIsInstanceUsageAllowed_WhenValueDoesNotMatch_ReturnsFalse() {
        filter.setInstanceTypeUsageFilter(InstanceTypeUsageEnum.CONTAINER);
        assertFalse(filter.isInstanceUsageAllowed(Set.of(NodeTypeEnum.MULTI)));
    }

    @Test
    void testDoesSetContainAnotherSet_WithNullSets_ReturnsTrue() {
        assertTrue(filter.areAttributesAllowed(null));
    }

    @Test
    void testDoesSetContainAnotherSet_WithEmptySets_ReturnsTrue() {
        assertTrue(filter.areAttributesAllowed(Set.of()));
    }

    @Test
    void testDoesSetContainAnotherSet_WithMatchingSets_ReturnsTrue() {
        filter.setAttributes(Set.of("attr1"));
        assertTrue(filter.areAttributesAllowed(Set.of("attr1", "attr2")));
    }

    @Test
    void testDoesSetContainAnotherSet_WithNonMatchingSets_ReturnsFalse() {
        filter.setAttributes(Set.of("attr1", "attr2"));
        assertFalse(filter.areAttributesAllowed(Set.of("attr1")));
    }
} 