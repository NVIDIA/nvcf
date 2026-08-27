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
package com.nvidia.icms.outbound.cassandra.byoc;

import static org.junit.jupiter.api.Assertions.assertEquals;

import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAccountEntity;
import org.junit.jupiter.api.Test;

/**
 * Smoke coverage for {@link NvcaConverter} fan-out. OIDC/PSAT fields
 * (jwks, oidcIssuer, jwksFingerprint) live only on the canonical
 * cluster_by_cluster_id row, so secondary views remain OIDC-free.
 */
class NvcaConverterTest {

    private static ClusterEntity baseClusterEntity() {
        return ClusterEntity.builder()
                .clusterName("test-cluster")
                .clusterId("cluster-123")
                .clusterGroupId("group-456")
                .clusterGroupName("test-group")
                .ncaId("nca-789")
                .clusterProvider(ClusterProviderEnum.GDN)
                .clusterStatus(ClusterStatusEnum.NOT_READY)
                .build();
    }

    @Test
    void toClusterByGroupIdAndIdEntity_propagatesCoreIdentity() {
        ClusterByGroupIdAndIdEntity out =
                NvcaConverter.toClusterByGroupIdAndIdEntity(baseClusterEntity());

        assertEquals("cluster-123", out.getKey().getClusterId());
        assertEquals("group-456", out.getKey().getClusterGroupId());
        assertEquals("test-cluster", out.getClusterName());
        assertEquals("nca-789", out.getNcaId());
    }

    @Test
    void toClustersByAccountEntity_propagatesCoreIdentity() {
        ClustersByAccountEntity out =
                NvcaConverter.toClustersByAccountEntity(baseClusterEntity());

        assertEquals("cluster-123", out.getClusterId());
        assertEquals("test-cluster", out.getKey().getClusterName());
        assertEquals("nca-789", out.getKey().getNcaId());
    }
}
