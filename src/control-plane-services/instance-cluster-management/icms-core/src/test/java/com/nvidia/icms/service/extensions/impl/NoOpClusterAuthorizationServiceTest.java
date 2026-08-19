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
package com.nvidia.icms.service.extensions.impl;

import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroups;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import org.junit.jupiter.api.Test;

import java.util.HashSet;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;

/**
 * Locks in the no-op contract for {@link NoOpClusterAuthorizationService}:
 * core (BYOC) callers must see {@code null} cluster groups and unchanged
 * input sets when the Non BYOC module is not active.
 */
class NoOpClusterAuthorizationServiceTest {

    private final NoOpClusterAuthorizationService noop = new NoOpClusterAuthorizationService();

    @Test
    void fetchAuthorizedClusterGroup_returnsNull() {
        ClusterGroups result = noop.fetchAuthorizedClusterGroup(
                "any-nca-id", InstanceTypeUsageEnum.DEFAULT);

        assertNull(result, "NoOp must report no Non BYOC cluster group is available");
    }

    @Test
    void filterAuthorizedClusters_leavesSetUnchanged() {
        ClusterByGroupIdAndIdEntity cluster1 = entity("cluster-1", "group-1");
        ClusterByGroupIdAndIdEntity cluster2 = entity("cluster-2", "group-2");
        Set<ClusterByGroupIdAndIdEntity> clusters = new HashSet<>(Set.of(cluster1, cluster2));
        Set<ClusterByGroupIdAndIdEntity> expectedContents = new HashSet<>(clusters);

        noop.filterAuthorizedClusters("any-nca-id", clusters);

        assertEquals(2, clusters.size(), "NoOp must not remove any cluster");
        assertEquals(expectedContents, clusters,
                "NoOp must leave the supplied cluster set exactly as provided");
    }

    @Test
    void filterAuthorizedClusters_emptySetRemainsEmpty() {
        Set<ClusterByGroupIdAndIdEntity> empty = new HashSet<>();

        noop.filterAuthorizedClusters("any-nca-id", empty);

        assertEquals(0, empty.size());
    }

    @Test
    void filterAuthorizedClusters_preservesIdentityOfSuppliedSet() {
        Set<ClusterByGroupIdAndIdEntity> clusters = new HashSet<>();
        clusters.add(entity("cluster-x", "group-x"));
        Set<ClusterByGroupIdAndIdEntity> suppliedReference = clusters;

        noop.filterAuthorizedClusters("any-nca-id", clusters);

        // Caller expects an in-place filter signature; confirm we operate on
        // the supplied reference rather than swapping it out.
        assertSame(suppliedReference, clusters);
    }

    private static ClusterByGroupIdAndIdEntity entity(String clusterId, String clusterGroupId) {
        ClusterByGroupIdAndIdEntity entity = new ClusterByGroupIdAndIdEntity();
        entity.setKey(new ClusterByGroupIdAndIdKey(clusterGroupId, clusterId));
        return entity;
    }
}
