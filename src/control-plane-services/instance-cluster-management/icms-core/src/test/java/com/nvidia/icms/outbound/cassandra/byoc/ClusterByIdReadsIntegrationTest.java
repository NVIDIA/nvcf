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

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

/**
 * Covers the {@code icms.cluster-by-id-reads-enabled=true} flow (set in application-test.yaml):
 * all reads are served from cluster_by_cluster_id through the storage-attached indexes.
 */
public class ClusterByIdReadsIntegrationTest extends IntegrationTest {

    @Autowired
    private ClusterRepository clusterRepository;

    @Autowired
    private NvcaClusterRepository nvcaClusterRepository;

    @Test
    void getAllClustersInAuthorizedAccount_returnsOwnedAuthorizedAndWildcardClusters() {
        // Prepare
        ClusterEntity owned = saveCluster("owner-a", Set.of("auth-a"));
        ClusterEntity authorized = saveCluster("owner-b", Set.of("auth-a"));
        ClusterEntity wildcard = saveCluster("owner-c", Set.of("*"));
        ClusterEntity unrelated = saveCluster("owner-d", Set.of("other"));

        // Act
        List<ClustersByAuthorizedAccountsEntity> forAuthorizedAccount =
                nvcaClusterRepository.getAllClustersInAuthorizedAccount("auth-a");
        List<ClustersByAuthorizedAccountsEntity> forOwnerAccount =
                nvcaClusterRepository.getAllClustersInAuthorizedAccount("owner-a");
        List<ClustersByAuthorizedAccountsEntity> forWildcardKey =
                nvcaClusterRepository.getAllClustersInAuthorizedAccount(ClusterRepository.WILDCARD);
        List<ClustersByAuthorizedAccountsEntity> forUnrelatedAccount =
                nvcaClusterRepository.getAllClustersInAuthorizedAccount("nobody");

        // Assert
        Assertions.assertEquals(
                Set.of(owned.getClusterId(), authorized.getClusterId(), wildcard.getClusterId()),
                toClusterIds(forAuthorizedAccount));
        Assertions.assertEquals(
                Set.of(owned.getClusterId(), wildcard.getClusterId()),
                toClusterIds(forOwnerAccount));
        Assertions.assertEquals(
                Set.of(wildcard.getClusterId()),
                toClusterIds(forWildcardKey));
        Assertions.assertEquals(
                Set.of(wildcard.getClusterId()),
                toClusterIds(forUnrelatedAccount));
        Assertions.assertFalse(
                toClusterIds(forAuthorizedAccount).contains(unrelated.getClusterId()));
    }

    @Test
    void getAllClustersInAuthorizedAccount_dedupesClusterMatchingMultipleBranches() {
        // Prepare: cluster owned by owner-e and authorized to owner-e matches both
        // nca_id = :x and authorized_nca_ids CONTAINS :x
        ClusterEntity cluster = saveCluster("owner-e", Set.of("owner-e"));

        // Act
        List<ClustersByAuthorizedAccountsEntity> result =
                nvcaClusterRepository.getAllClustersInAuthorizedAccount("owner-e");

        // Assert
        Assertions.assertEquals(1, result.size());
        Assertions.assertEquals(cluster.getClusterId(), result.get(0).getKey().getClusterId());
        Assertions.assertEquals(cluster.getClusterName(), result.get(0).getClusterName());
        Assertions.assertEquals(cluster.getClusterGroupId(), result.get(0).getClusterGroupId());
        Assertions.assertEquals(cluster.getNcaId(), result.get(0).getNcaId());
    }

    @Test
    void clusterReads_useOnlyClusterByIdTable() {
        // Prepare
        ClusterEntity entity = getDummyClusterEntity();
        entity.setClusterId(UUID.randomUUID().toString());
        entity.setClusterName("name-" + entity.getClusterId());
        nvcaClusterRepository.saveClusterInfo(entity);

        // Act
        Set<ClusterEntity> clustersInGroup =
                clusterRepository.getAllClustersInAGroup(entity.getClusterGroupId());
        Optional<ClusterGroupByGroupIdEntity> groupById =
                clusterRepository.getClusterGroupInfoByClusterGroupId(entity.getClusterGroupId());
        Optional<ClusterGroupsByAccountEntity> groupByAccount =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        entity.getNcaId(), entity.getClusterGroupName());
        Optional<ClusterEntity> byAccountAndName =
                clusterRepository.getClusterByAccountAndName(
                        entity.getNcaId(), entity.getClusterName());
        List<ClusterEntity> allInAccount =
                clusterRepository.getAllClustersInAnAccount(entity.getNcaId());
        var fromGroup = clusterRepository.getClustersFromClusterGroup(entity.getClusterGroupId());

        // Assert
        Assertions.assertEquals(Set.of(entity.getClusterId()), toEntityClusterIds(clustersInGroup));
        Assertions.assertTrue(groupById.isPresent());
        Assertions.assertEquals(entity.getClusterGroupId(), groupById.get().getClusterGroupId());
        Assertions.assertTrue(groupByAccount.isPresent());
        Assertions.assertEquals(entity.getClusterGroupId(),
                                groupByAccount.get().getClusterGroupId());
        Assertions.assertTrue(byAccountAndName.isPresent());
        Assertions.assertEquals(entity.getClusterId(), byAccountAndName.get().getClusterId());
        Assertions.assertEquals(List.of(entity.getClusterId()),
                                allInAccount.stream().map(ClusterEntity::getClusterId).toList());
        Assertions.assertEquals(1, fromGroup.size());
        Assertions.assertEquals(entity.getClusterId(), fromGroup.get(0).getKey().getClusterId());
    }

    @Test
    void clusterReads_returnEmptyForUnknownKeys() {
        // Act + Assert
        Assertions.assertTrue(clusterRepository.getAllClustersInAGroup("missing-group").isEmpty());
        Assertions.assertTrue(
                clusterRepository.getClusterGroupInfoByClusterGroupId("missing-group").isEmpty());
        Assertions.assertTrue(clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                "missing-nca", "missing-name").isEmpty());
        Assertions.assertTrue(
                clusterRepository.getClusterByAccountAndName("missing-nca", "missing").isEmpty());
        Assertions.assertTrue(clusterRepository.getAllClustersInAnAccount("missing-nca").isEmpty());
        Assertions.assertTrue(
                clusterRepository.getClustersFromClusterGroup("missing-group").isEmpty());
        Assertions.assertTrue(clusterRepository.getAllClusterGroupsInAuthorizedAccount(
                "missing-nca").isEmpty());
        Assertions.assertTrue(nvcaClusterRepository.getAllClustersInAuthorizedAccount(
                "missing-nca").isEmpty());
    }

    private ClusterEntity saveCluster(String ncaId, Set<String> authorizedNcaIds) {
        return saveCluster(ncaId, authorizedNcaIds, "1.0.0");
    }

    private ClusterEntity saveCluster(String ncaId, Set<String> authorizedNcaIds,
                                      String nvcaVersion) {
        ClusterEntity entity = getDummyClusterEntity();
        entity.setClusterId(UUID.randomUUID().toString());
        entity.setClusterName("name-" + entity.getClusterId());
        entity.setNcaId(ncaId);
        entity.setClusterGroupName("group-" + entity.getClusterId());
        entity.setClusterGroupId("group-id-" + entity.getClusterId());
        entity.setAuthorizedNcaIds(authorizedNcaIds);
        entity.setNvcaVersion(nvcaVersion);
        nvcaClusterRepository.saveClusterInfo(entity);
        return entity;
    }

    private static Set<String> toClusterIds(List<ClustersByAuthorizedAccountsEntity> entities) {
        return entities.stream()
                .map(entity -> entity.getKey().getClusterId())
                .collect(Collectors.toSet());
    }

    private static Set<String> toEntityClusterIds(Set<ClusterEntity> entities) {
        return entities.stream().map(ClusterEntity::getClusterId).collect(Collectors.toSet());
    }
}
