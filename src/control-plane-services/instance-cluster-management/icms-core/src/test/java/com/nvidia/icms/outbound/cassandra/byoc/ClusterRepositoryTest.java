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

import static com.nvidia.icms.util.TestUtil.buildUpdatedGpusForCluster;
import static com.nvidia.icms.util.TestUtil.getDummyClusterEntity;

import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.InstanceTypeUdt;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
 import org.springframework.test.context.TestPropertySource;

// A generic first-party compute platform is configured locally (icms-core ships none by default)
// so the compute-platform branch of updateClusterInfo can be exercised without a deployment-specific
// platform name.
@TestPropertySource(properties = {
        "icms.compute-platforms[0].name=" + ClusterRepositoryTest.COMPUTE_PLATFORM_NAME,
        "icms.compute-platforms[0].cluster-group-name=" + ClusterRepositoryTest.COMPUTE_PLATFORM_CLUSTER_GROUP_NAME,
        "icms.compute-platforms[0].cluster-group-id=PLATFORM_A_GROUP_ID"
})
public class ClusterRepositoryTest extends IntegrationTest {

    static final String COMPUTE_PLATFORM_NAME = "PLATFORM_A";

    static final String COMPUTE_PLATFORM_CLUSTER_GROUP_NAME = "PLATFORM_A_REGION_TARGETING";

    @Autowired
    private ClusterRepository clusterRepository;

    @Test
    void saveClusterInfo_success() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();

        // Act
        clusterRepository.saveClusterInfo(clusterEntity);

        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false);

        Optional<ClusterGroupsByAccountEntity> optionalClusterGroupsByAccountEntity =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        clusterEntity.getNcaId(), clusterEntity.getClusterGroupName());

        Set<ClusterEntity> clusterEntities =
                clusterRepository.getAllClustersInAGroup(clusterEntity.getClusterGroupId());
        Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                clusterRepository.getClusterGroupInfoByClusterGroupId("group_id");
        List<ClusterEntity> clustersByAccountEntities =
                clusterRepository.getAllClustersInAnAccount(clusterEntity.getNcaId());

        // Assert
        // For cluster specific entries
        Assertions.assertTrue(optionalClusterEntity.isPresent());
        Assertions.assertEquals(1, clusterEntities.size());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                optionalClusterEntity.get().getClusterId());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                clusterEntities.stream().findFirst().get().getClusterId());
        Assertions.assertEquals(1, clustersByAccountEntities.size());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                clustersByAccountEntities.stream().findFirst().get()
                                        .getClusterId());

        // For cluster group specific entries
        Assertions.assertTrue(optionalClusterGroupByGroupIdEntity.isPresent());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                optionalClusterGroupByGroupIdEntity.get().getClusterGroupId());

        // For cluster groups in main account
        Assertions.assertTrue(optionalClusterGroupsByAccountEntity.isPresent());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                optionalClusterGroupsByAccountEntity.get().getClusterGroupId());
    }

    @Test
    void update_test() {
        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        String clusterId = clusterEntity.getClusterId();

        clusterRepository.saveClusterInfo(clusterEntity);

        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        Assertions.assertTrue(optionalClusterEntity.isPresent());

        ClusterEntity entity = optionalClusterEntity.get();
        entity.setClusterStatus(ClusterStatusEnum.ABANDONED);

        // Act
        clusterRepository.updateClusterInfo(entity, new HashSet<>(), false);

        // Assert
        Optional<ClusterEntity> optionalClusterEntity2 =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        Set<ClusterEntity> clusterEntities =
                clusterRepository.getAllClustersInAGroup(clusterEntity.getClusterGroupId());

        Assertions.assertTrue(optionalClusterEntity2.isPresent());
        ClusterEntity entity2 = optionalClusterEntity2.get();
        Assertions.assertEquals(clusterEntity.getClusterId(), entity2.getClusterId());
        Assertions.assertEquals(ClusterStatusEnum.ABANDONED, entity2.getClusterStatus());
        Assertions.assertEquals(1, clusterEntities.size());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                clusterEntities.stream().findFirst().get().getClusterId());
        Assertions.assertEquals(ClusterStatusEnum.ABANDONED,
                                clusterEntities.stream().findFirst().get().getClusterStatus());
    }

    @Test
    void updateClusterInfo_withComputePlatformCluster_success() {
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(UUID.randomUUID().toString());
        clusterEntity.setClusterName("STATIC-ZONE");
        clusterEntity.setClusterGroupName(COMPUTE_PLATFORM_NAME);
        clusterEntity.setNcaId("compute-platform-nca-id");
        clusterEntity.setClusterProvider(ClusterProviderEnum.ONPREM);
        String clusterId = clusterEntity.getClusterId();
        String clusterGroupId = clusterEntity.getClusterGroupId();

        clusterRepository.saveClusterInfo(clusterEntity);

        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        Assertions.assertTrue(optionalClusterEntity.isPresent());

        ClusterEntity entity = optionalClusterEntity.get();
        entity.setClusterStatus(ClusterStatusEnum.ABANDONED);
        entity.setGpus(buildUpdatedGpusForCluster());

        // Act
        clusterRepository.updateClusterInfo(entity, new HashSet<>(), false);

        // Assert
        Optional<ClusterEntity> optionalClusterEntity2 =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        Set<ClusterEntity> clusterEntities =
                clusterRepository.getAllClustersInAGroup(clusterGroupId);

        Optional<ClusterGroupsByAccountEntity> optionalClusterGroupsByAccountEntity =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        clusterEntity.getNcaId(), clusterEntity.getClusterGroupName());

        Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                clusterRepository.getClusterGroupInfoByClusterGroupId("group_id");
        List<ClusterEntity> clustersByAccountEntities =
                clusterRepository.getAllClustersInAnAccount(clusterEntity.getNcaId());

        // For cluster specific entries by cluster id
        Assertions.assertTrue(optionalClusterEntity2.isPresent());
        ClusterEntity entity2 = optionalClusterEntity2.get();
        Assertions.assertEquals(clusterEntity.getClusterId(), entity2.getClusterId());
        Assertions.assertEquals(ClusterStatusEnum.ABANDONED, entity2.getClusterStatus());
        Assertions.assertEquals(buildUpdatedGpusForCluster(), entity2.getGpus());
        Assertions.assertEquals(1, clusterEntities.size());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                clusterEntities.stream().findFirst().get().getClusterId());
        Assertions.assertEquals(ClusterStatusEnum.ABANDONED,
                                clusterEntities.stream().findFirst().get().getClusterStatus());

        // For cluster specific entries by account
        Assertions.assertFalse(clustersByAccountEntities.isEmpty());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                clustersByAccountEntities.get(0).getClusterGroupId());
        Assertions.assertEquals(buildUpdatedGpusForCluster(),
                                clustersByAccountEntities.get(0).getGpus());

        // For cluster group specific entries by group ID
        Assertions.assertTrue(optionalClusterGroupByGroupIdEntity.isPresent());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                optionalClusterGroupByGroupIdEntity.get().getClusterGroupId());
        Assertions.assertEquals(buildUpdatedGpusForCluster(),
                                optionalClusterGroupByGroupIdEntity.get().getGpus());

        // For cluster groups in main account
        Assertions.assertTrue(optionalClusterGroupsByAccountEntity.isPresent());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                optionalClusterGroupsByAccountEntity.get().getClusterGroupId());
        Assertions.assertEquals(buildUpdatedGpusForCluster(),
                                optionalClusterGroupsByAccountEntity.get().getGpus());

        // For cluster specific entries by group ID
        List<ClusterByGroupIdAndIdEntity> optionalClusterEntitybyClusterGroup =
                clusterRepository.getClustersFromClusterGroup(clusterGroupId);
        Assertions.assertFalse(optionalClusterEntitybyClusterGroup.isEmpty());
        ClusterByGroupIdAndIdEntity entity3 = optionalClusterEntitybyClusterGroup.get(0);
        Assertions.assertEquals(buildUpdatedGpusForCluster(), entity3.getGpus());
    }

    @Test
    void updateClusterInfo_withAuthorizedNcaIdUpdated_success() {
        // Prepare
        Set<String> oldNcaIds = Set.of("old1", "old2");
        Set<String> newNcaIds = Set.of("new1", "new2");

        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setAuthorizedNcaIds(oldNcaIds);
        String clusterId = clusterEntity.getClusterId();
        String clusterGroupId = clusterEntity.getClusterGroupId();

        clusterRepository.saveClusterInfo(clusterEntity);

        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        Assertions.assertTrue(optionalClusterEntity.isPresent());

        ClusterEntity entity = optionalClusterEntity.get();
        entity.setClusterStatus(ClusterStatusEnum.ABANDONED);
        entity.setGpus(buildUpdatedGpusForCluster());
        entity.setAuthorizedNcaIds(newNcaIds);

        // Act
        clusterRepository.updateClusterInfo(entity, oldNcaIds, true);

        // Assert
        Optional<ClusterEntity> optionalClusterEntity2 =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        Set<ClusterEntity> clusterEntities =
                clusterRepository.getAllClustersInAGroup(clusterGroupId);

        Optional<ClusterGroupsByAccountEntity> optionalClusterGroupsByAccountEntity =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        clusterEntity.getNcaId(), clusterEntity.getClusterGroupName());

        Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                clusterRepository.getClusterGroupInfoByClusterGroupId("group_id");
        List<ClusterEntity> clustersByAccountEntities =
                clusterRepository.getAllClustersInAnAccount(clusterEntity.getNcaId());

        // For cluster specific entries by cluster id
        Assertions.assertTrue(optionalClusterEntity2.isPresent());
        ClusterEntity entity2 = optionalClusterEntity2.get();
        Assertions.assertEquals(clusterEntity.getClusterId(), entity2.getClusterId());
        Assertions.assertEquals(ClusterStatusEnum.ABANDONED, entity2.getClusterStatus());
        Assertions.assertEquals(buildUpdatedGpusForCluster(), entity2.getGpus());
        Assertions.assertEquals(1, clusterEntities.size());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                clusterEntities.stream().findFirst().get().getClusterId());
        Assertions.assertEquals(ClusterStatusEnum.ABANDONED,
                                clusterEntities.stream().findFirst().get().getClusterStatus());

        // For cluster specific entries by account
        Assertions.assertFalse(clustersByAccountEntities.isEmpty());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                clustersByAccountEntities.get(0).getClusterGroupId());
        Assertions.assertEquals(buildUpdatedGpusForCluster(),
                                clustersByAccountEntities.get(0).getGpus());

        // For cluster group specific entries by group ID
        Assertions.assertTrue(optionalClusterGroupByGroupIdEntity.isPresent());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                optionalClusterGroupByGroupIdEntity.get().getClusterGroupId());
        Assertions.assertEquals(buildUpdatedGpusForCluster(),
                                optionalClusterGroupByGroupIdEntity.get().getGpus());

        // For cluster groups in main account
        Assertions.assertTrue(optionalClusterGroupsByAccountEntity.isPresent());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                optionalClusterGroupsByAccountEntity.get().getClusterGroupId());
        Assertions.assertEquals(buildUpdatedGpusForCluster(),
                                optionalClusterGroupsByAccountEntity.get().getGpus());

        // For cluster specific entries by group ID
        List<ClusterByGroupIdAndIdEntity> optionalClusterEntitybyClusterGroup =
                clusterRepository.getClustersFromClusterGroup(clusterGroupId);
        Assertions.assertFalse(optionalClusterEntitybyClusterGroup.isEmpty());
        ClusterByGroupIdAndIdEntity entity3 = optionalClusterEntitybyClusterGroup.get(0);
        Assertions.assertEquals(buildUpdatedGpusForCluster(), entity3.getGpus());
    }

    @Test
    void getClusterInfoByClusterId_success() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();
        clusterEntity.setClusterId(UUID.randomUUID().toString());

        // Act
        clusterRepository.saveClusterInfo(clusterEntity);

        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false);

        // Assert
        Assertions.assertTrue(optionalClusterEntity.isPresent());

        assertClusterEntity(clusterEntity, optionalClusterEntity.get());
    }

    @Test
    void getAllClustersInAGroup_success() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();

        // Act
        clusterRepository.saveClusterInfo(clusterEntity);

        Set<ClusterEntity> clusterEntities = clusterRepository.getAllClustersInAGroup("group_id");

        // Assert
        Assertions.assertEquals(1, clusterEntities.size());

        for (ClusterEntity entity : clusterEntities) {
            assertClusterEntity(clusterEntity, entity);
        }
    }

    @Test
    void getClusterGroupInfoByClusterGroupId_success() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();

        // Act
        clusterRepository.saveClusterInfo(clusterEntity);

        Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                clusterRepository.getClusterGroupInfoByClusterGroupId("group_id");

        // Assert
        Assertions.assertTrue(optionalClusterGroupByGroupIdEntity.isPresent());

        assertClusterGroupInfo(clusterEntity, optionalClusterGroupByGroupIdEntity.get());
    }

    @Test
    void getClusterGroupInfoByAccountAndNameInMainAccount_success() {

        // Prepare
        ClusterEntity entity = getDummyClusterEntity();
        clusterRepository.saveClusterInfo(entity);

        // Act
        Optional<ClusterGroupsByAccountEntity> optionalClusterGroupsByAccountEntity =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        entity.getNcaId(), entity.getClusterGroupName());

        // Assert
        Assertions.assertTrue(optionalClusterGroupsByAccountEntity.isPresent());
        Assertions.assertEquals(entity.getCreationQueueUrl(),
                                optionalClusterGroupsByAccountEntity.get().getCreationQueueUrl());
    }

       @Test
    void getAllClustersInAnAccount_success() {

        // Prepare
        ClusterEntity entity = getDummyClusterEntity();
        clusterRepository.saveClusterInfo(entity);

        // Act
        List<ClusterEntity> clusterEntities =
                clusterRepository.getAllClustersInAnAccount(entity.getNcaId());

        // Assert
        Assertions.assertEquals(1, clusterEntities.size());
        Assertions.assertEquals(entity.getClusterId(),
                                clusterEntities.stream().findFirst().get().getClusterId());
    }

    @Test
    void getClusterByAccountAndName_success() {

        // Prepare
        ClusterEntity entity = getDummyClusterEntity();
        clusterRepository.saveClusterInfo(entity);

        // Act
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterByAccountAndName(entity.getNcaId(),
                                                             entity.getClusterName());

        // Assert
        Assertions.assertTrue(optionalClusterEntity.isPresent());
        Assertions.assertEquals(entity.getClusterId(), optionalClusterEntity.get().getClusterId());
    }

    @Test
    void getAllClusterGroupsInAuthorizedAccount_success() {

        // Prepare
        ClusterEntity entity = getDummyClusterEntity();
        clusterRepository.saveClusterInfo(entity);

        ClusterEntity entity2 = getDummyClusterEntity();
        entity2.setNcaId("new-nca-id");
        entity2.setClusterGroupName("new-group");
        entity2.setClusterGroupId("new-group-id");
        entity2.setClusterId("new-cluster-id");
        entity2.setClusterName("new-name");
        clusterRepository.saveClusterInfo(entity2);

        // Act
        List<ClusterGroupsByAuthorizedAccountsEntity> clusterGroupsByAuthorizedAccountsEntities =
                clusterRepository.getAllClusterGroupsInAuthorizedAccount(
                        entity.getAuthorizedNcaIds().stream().findFirst().get());

        // Assert
        Assertions.assertEquals(2, clusterGroupsByAuthorizedAccountsEntities.size());
        Set<String> clusterGroupNames = clusterGroupsByAuthorizedAccountsEntities.stream()
                .map(ClusterGroupsByAuthorizedAccountsEntity::getClusterGroupName)
                .collect(
                        Collectors.toSet());
        Assertions.assertEquals(2, clusterGroupNames.size());
        Assertions.assertTrue(clusterGroupNames.contains(entity.getClusterGroupName()));
        Assertions.assertTrue(clusterGroupNames.contains(entity2.getClusterGroupName()));
    }

    @Test
    void deleteClusterInfo_success() {

        // Prepare
        ClusterEntity clusterEntity = getDummyClusterEntity();

        // Act
        clusterRepository.saveClusterInfo(clusterEntity);

        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false);

        Optional<ClusterGroupsByAccountEntity> optionalClusterGroupsByAccountEntity =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        clusterEntity.getNcaId(), clusterEntity.getClusterGroupName());

        List<String> authorizedNcaIdsList = new ArrayList<>(clusterEntity.getAuthorizedNcaIds());

        Set<ClusterEntity> clusterEntities =
                clusterRepository.getAllClustersInAGroup(clusterEntity.getClusterGroupId());
        Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                clusterRepository.getClusterGroupInfoByClusterGroupId("group_id");
        List<ClusterEntity> clustersByAccountEntities =
                clusterRepository.getAllClustersInAnAccount(clusterEntity.getNcaId());

        // Assert
        // For cluster specific entries
        Assertions.assertTrue(optionalClusterEntity.isPresent());
        Assertions.assertEquals(1, clusterEntities.size());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                optionalClusterEntity.get().getClusterId());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                clusterEntities.stream().findFirst().get().getClusterId());
        Assertions.assertEquals(1, clustersByAccountEntities.size());
        Assertions.assertEquals(clusterEntity.getClusterId(),
                                clustersByAccountEntities.stream().findFirst().get()
                                        .getClusterId());

        // For cluster group specific entries
        Assertions.assertTrue(optionalClusterGroupByGroupIdEntity.isPresent());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                optionalClusterGroupByGroupIdEntity.get().getClusterGroupId());

        // For cluster groups in main account
        Assertions.assertTrue(optionalClusterGroupsByAccountEntity.isPresent());
        Assertions.assertEquals(clusterEntity.getClusterGroupId(),
                                optionalClusterGroupsByAccountEntity.get().getClusterGroupId());

        // delete
        clusterRepository.deleteClusterInfo(clusterEntity);

        // Assert for delete
        optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterEntity.getClusterId(), false);

        optionalClusterGroupsByAccountEntity =
                clusterRepository.getClusterGroupInfoByAccountAndNameInMainAccount(
                        clusterEntity.getNcaId(), clusterEntity.getClusterGroupName());

        clusterEntities =
                clusterRepository.getAllClustersInAGroup(clusterEntity.getClusterGroupId());
        optionalClusterGroupByGroupIdEntity =
                clusterRepository.getClusterGroupInfoByClusterGroupId("group_id");
        clustersByAccountEntities =
                clusterRepository.getAllClustersInAnAccount(clusterEntity.getNcaId());

        // For cluster specific entries
        Assertions.assertTrue(optionalClusterEntity.isEmpty());
        Assertions.assertEquals(0, clusterEntities.size());
        Assertions.assertEquals(0, clustersByAccountEntities.size());

        // For cluster group specific entries
        Assertions.assertTrue(optionalClusterGroupByGroupIdEntity.isEmpty());

        // For cluster groups in main account
        Assertions.assertTrue(optionalClusterGroupsByAccountEntity.isEmpty());

    }

    void assertClusterEntity(ClusterEntity expected, ClusterEntity actual) {
        Assertions.assertEquals(expected.getClusterName(), actual.getClusterName());
        Assertions.assertEquals(expected.getClusterId(), actual.getClusterId());
        Assertions.assertEquals(expected.getNcaId(), actual.getNcaId());
        Assertions.assertEquals(expected.getTerminationQueueUrl(),
                                actual.getTerminationQueueUrl());
        Assertions.assertEquals(expected.getTerminationQueueType(),
                                actual.getTerminationQueueType());
        Assertions.assertEquals(expected.getClusterProvider(), actual.getClusterProvider());
        Assertions.assertEquals(expected.getClusterStatus(), actual.getClusterStatus());
        Assertions.assertEquals(expected.getClusterGroupName(), actual.getClusterGroupName());
        Assertions.assertEquals(expected.getClusterGroupId(), actual.getClusterGroupId());
        Assertions.assertEquals(expected.getCreationQueueUrl(), actual.getCreationQueueUrl());
        Assertions.assertEquals(expected.getCreationQueueType(), actual.getCreationQueueType());
        Assertions.assertEquals(expected.getRequestDump(), actual.getRequestDump());

        Assertions.assertTrue(
                expected.getAuthorizedNcaIds().containsAll(actual.getAuthorizedNcaIds()));

        Set<GpuUdt> expectedGpus = expected.getGpus();
        Set<GpuUdt> actualGpus = actual.getGpus();
        assertGpus(expectedGpus, actualGpus);
    }

    void assertClusterGroupInfo(ClusterEntity expected, ClusterGroupByGroupIdEntity actual) {
        Assertions.assertEquals(expected.getClusterGroupName(), actual.getClusterGroupName());
        Assertions.assertEquals(expected.getClusterGroupId(), actual.getClusterGroupId());
        Assertions.assertEquals(expected.getCreationQueueUrl(), actual.getCreationQueueUrl());
        Assertions.assertEquals(expected.getCreationQueueType(), actual.getCreationQueueType());

        Assertions.assertTrue(
                expected.getAuthorizedNcaIds().containsAll(actual.getAuthorizedNcaIds()));

        Set<GpuUdt> expectedGpus = expected.getGpus();
        Set<GpuUdt> actualGpus = actual.getGpus();
        assertGpus(expectedGpus, actualGpus);
    }

    void assertGpus(Set<GpuUdt> expected, Set<GpuUdt> actual) {
        Assertions.assertEquals(expected.size(), actual.size());
        Set<String> expectedNames = expected.stream().map(GpuUdt::getName).collect(Collectors.toSet());
        Set<String> actualNames = actual.stream().map(GpuUdt::getName).collect(Collectors.toSet());
        Assertions.assertEquals(expectedNames.size(), actualNames.size());
        int matching = 0;
        for (String name : actualNames) {
            if (expectedNames.contains(name)) {
                matching++;
            }
        }
        Assertions.assertEquals(actualNames.size(), matching);
        for (GpuUdt expectedGpu : expected) {
            for (GpuUdt actualGpu : actual) {
                if (expectedGpu.getName().equals(actualGpu.getName())) {
                    assertInstanceTypeSpec(expectedGpu.getInstanceTypes(),
                                           actualGpu.getInstanceTypes());
                }
            }
        }
    }

    void assertInstanceTypeSpec(Set<InstanceTypeUdt> expected, Set<InstanceTypeUdt> actual) {
        Assertions.assertEquals(expected.size(), actual.size());
        Set<InstanceTypeUdt> expectedInstanceSet = new HashSet<>(actual);
        for (InstanceTypeUdt exp : expected) {
            InstanceTypeUdt found = null;
            for (InstanceTypeUdt act : expectedInstanceSet) {
                if (act.getName().equals(exp.getName()) &&
                        act.getValue().equals(exp.getValue()) &&
                        act.getDescription().equals(exp.getDescription()) &&
                        Objects.equals(act.getIsDefault(), exp.getIsDefault()) &&
                        act.getGpuMemory().equals(exp.getGpuMemory()) &&
                        act.getSystemMemory().equals(exp.getSystemMemory()) &&
                        act.getCpuCores() == exp.getCpuCores()) {
                    found = act;
                }
            }
            if (found == null) {
                Assertions.fail("Could not find expected instance type");
            }
            expectedInstanceSet.remove(found);
        }
        Assertions.assertTrue(expectedInstanceSet.isEmpty());
    }
}
