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

import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

class ClusterRepositoryTest extends IntegrationTest {

    @Autowired
    private ClusterRepository clusterRepository;

    @Autowired
    private NvcaClusterRepository nvcaClusterRepository;

    @Test
    void nvcaPersistenceSupportsLegacyRepositoryReadsAndHealthUpdates() {
        ClusterEntity entity = getDummyClusterEntity();
        entity.setClusterId(UUID.randomUUID().toString());
        entity.setClusterName("cluster-" + entity.getClusterId());
        entity.setClusterGroupId(UUID.randomUUID().toString());
        entity.setClusterGroupName("group-" + entity.getClusterGroupId());
        nvcaClusterRepository.saveClusterInfo(entity);

        try {
            assertTrue(clusterRepository.getClusterInfoByClusterId(entity.getClusterId(), false)
                    .isPresent());
            assertEquals(1, clusterRepository.getAllClustersInAGroup(entity.getClusterGroupId()).size());
            assertEquals(1, clusterRepository.getAllClustersInAnAccount(entity.getNcaId()).stream()
                    .filter(cluster -> entity.getClusterId().equals(cluster.getClusterId())).count());
            assertTrue(clusterRepository.getClusterGroupInfoByClusterGroupId(entity.getClusterGroupId())
                    .isPresent());
            assertTrue(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                    entity.getNcaId(), entity.getClusterGroupName()).isPresent());
            assertEquals(entity.getClusterId(), nvcaClusterRepository
                    .getAllClustersInAuthorizedAccount("ncaId1").stream()
                    .filter(cluster -> entity.getClusterId().equals(cluster.getKey().getClusterId()))
                    .findFirst().orElseThrow().getKey().getClusterId());

            entity.setClusterStatus(ClusterStatusEnum.ABANDONED);
            clusterRepository.updateClusterInfo(entity);
            assertEquals(ClusterStatusEnum.ABANDONED,
                    clusterRepository.getClusterInfoByClusterId(entity.getClusterId(), false)
                            .orElseThrow().getClusterStatus());
            assertEquals(ClusterStatusEnum.ABANDONED,
                    clusterRepository.getClustersFromClusterGroup(entity.getClusterGroupId())
                            .stream().filter(cluster -> entity.getClusterId().equals(
                                    cluster.getKey().getClusterId())).findFirst().orElseThrow()
                            .getClusterStatus());
        } finally {
            nvcaClusterRepository.deleteClusterInfo(entity);
        }

        assertFalse(clusterRepository.getClusterInfoByClusterId(entity.getClusterId(), false)
                .isPresent());
    }
}
