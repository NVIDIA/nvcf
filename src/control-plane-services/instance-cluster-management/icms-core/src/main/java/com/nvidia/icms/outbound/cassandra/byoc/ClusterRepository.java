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

import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClusterGroupsByAuthorizedAccountsEntity;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getClusterIdFromAuthClientId;

import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterHealthEntity;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.scheduled.request.ClusterHealthMonitorTask;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
// TODO: Once we remove BART support, evaluate if this repository can be deprecated and move useful function to NvcaClusterRepository class
public class ClusterRepository {

    public static final String WILDCARD = "WILDCARD";

    // Wildcard marker stored in the authorized_nca_ids column of cluster_by_cluster_id.
    public static final String AUTHORIZED_NCA_IDS_WILDCARD = "*";

    private final ClusterByIdRepo clusterByIdRepo;
    private final ClusterGroupsByAuthorizedAccountsRepo clusterGroupsByAuthorizedAccountsRepo;
    private final ClusterHealthRepo clusterHealthRepo;
    private final ComputePlatformService computePlatformService;

    public ClusterRepository(
            ClusterByIdRepo clusterByIdRepo,
            ClusterGroupsByAuthorizedAccountsRepo clusterGroupsByAuthorizedAccountsRepo,
            ClusterHealthRepo clusterHealthRepo,
            ComputePlatformService computePlatformService) {

        this.clusterByIdRepo = clusterByIdRepo;
        this.clusterGroupsByAuthorizedAccountsRepo = clusterGroupsByAuthorizedAccountsRepo;
        this.clusterHealthRepo = clusterHealthRepo;
        this.computePlatformService = computePlatformService;
    }

    public void saveClusterInfo(ClusterEntity entity) {

        String errMsg = String.format(
                "Cannot save cluster with name %s and id %s and group %s and group id %s.",
                entity.getClusterName(),
                entity.getClusterId(),
                entity.getClusterGroupName(),
                entity.getClusterGroupId());

        boolean groupExists = !clusterByIdRepo.findAllByClusterGroupId(entity.getClusterGroupId())
                .isEmpty();
        // TODO: Replace isInserted and its result check with a simple insert command.
        if (!clusterByIdRepo.isInserted(entity)) {
            log.error(errMsg);
            throw new IcmsConflictException(errMsg);
        }

        // cluster_groups_by_authorized_accounts belongs to the deprecated flow and will be
        // removed soon. A failed group write can be skipped on retry because the cluster exists;
        // this is an accepted temporary risk until that flow is removed.
        if (!groupExists && !insertForAuthorizedAccounts(entity)) {
            log.error(errMsg);
            throw new IcmsConflictException(errMsg);
        }
    }

    private boolean insertForAuthorizedAccounts(ClusterEntity entity) {
        Set<String> authorizedNcaIds = entity.getAuthorizedNcaIds();
        ClusterGroupsByAuthorizedAccountsEntity clusterGroupsByAuthorizedAccountsEntity =
                toClusterGroupsByAuthorizedAccountsEntity(entity,
                                                          entity.getNcaId());
        if (!clusterGroupsByAuthorizedAccountsRepo.isInserted(clusterGroupsByAuthorizedAccountsEntity)) {
            return false;
        }
        for (String ncaId : authorizedNcaIds) {
            if (ncaId.equals("*")) {
                clusterGroupsByAuthorizedAccountsEntity.getKey().setNcaIdKey(WILDCARD);
            } else {
                clusterGroupsByAuthorizedAccountsEntity.getKey().setNcaIdKey(ncaId);
            }
            if (!clusterGroupsByAuthorizedAccountsRepo.isInserted(
                    clusterGroupsByAuthorizedAccountsEntity)) {
                return false;
            }
        }
        return true;
    }

    /**
     * @param clusterId              clusterId of cluster
     * @param checkForHashedCusterId check for hashed clusterId
     * @return {@link ClusterEntity}
     * <p>
     * set checkForHashedCusterId: true if we have validated clusterId with authentication token in calling function
     */
    @Observed
    public Optional<ClusterEntity> getClusterInfoByClusterId(
            @NotNull String clusterId, boolean checkForHashedCusterId) {
        Optional<ClusterEntity> optionalClusterEntity = clusterByIdRepo.findById(clusterId);
        // Check for cluster for clientId presence in cache
        if (optionalClusterEntity.isPresent()) {
            return optionalClusterEntity;
        } else if (checkForHashedCusterId) {
            String hashedClusterId = getClusterIdFromAuthClientId(clusterId);
            return clusterByIdRepo.findById(hashedClusterId);
        }
        return optionalClusterEntity;
    }

    public Set<ClusterEntity> getAllClustersInAGroup(String clusterGroupId) {
        return clusterByIdRepo.findAllByClusterGroupId(clusterGroupId).stream()
                .collect(Collectors.toSet());
    }

    public Optional<ClusterGroupsByAccountEntity> getClusterGroupInfoByAccountAndNameInMainAccount(
            String ncaId, String clusterGroupName) {

        // No index on cluster_group_name: query by nca_id SAI and filter in memory.
        return clusterByIdRepo.findAllByNcaId(ncaId).stream()
                .filter(entity -> clusterGroupName.equals(entity.getClusterGroupName()))
                .findFirst()
                .map(NvcaConverter::toClusterGroupsByAccountsEntity);
    }


    public List<ClusterGroupsByAuthorizedAccountsEntity> getAllClusterGroupsInAuthorizedAccount(
            String ncaId) {

        return clusterGroupsByAuthorizedAccountsRepo.findAllByKeyNcaIdKey(ncaId);
    }

    public Optional<ClusterGroupByGroupIdEntity> getClusterGroupInfoByClusterGroupId(
            String clusterGroupId) {

        return clusterByIdRepo.findAllByClusterGroupId(clusterGroupId).stream()
                .findFirst()
                .map(NvcaConverter::toClusterGroupByGroupIdEntity);
    }

    public Optional<ClusterEntity> getClusterByAccountAndName(String ncaId, String name) {
        // No index on cluster_name: query by nca_id SAI and filter in memory.
        return clusterByIdRepo.findAllByNcaId(ncaId).stream()
                .filter(entity -> name.equals(entity.getClusterName()))
                .findFirst();
    }

    public List<ClusterEntity> getAllClustersInAnAccount(String ncaId) {

        return clusterByIdRepo.findAllByNcaId(ncaId).stream()
                .collect(Collectors.toList());
    }

    /**
     * Reconstructs a {@code clusters_by_authorized_accounts} partition from
     * {@code cluster_by_cluster_id}. The wildcard partition contains public clusters, while an
     * account partition contains only owned or explicitly authorized clusters.
     */
    public List<ClusterEntity> getAllClustersForAuthorizationKey(String ncaId) {
        if (WILDCARD.equals(ncaId)) {
            return clusterByIdRepo.findAllByAuthorizedNcaIdsContains(AUTHORIZED_NCA_IDS_WILDCARD);
        }

        Map<String, ClusterEntity> clustersById = new LinkedHashMap<>();
        clusterByIdRepo.findAllByNcaId(ncaId)
                .forEach(entity -> clustersById.put(entity.getClusterId(), entity));
        clusterByIdRepo.findAllByAuthorizedNcaIdsContains(ncaId)
                .forEach(entity -> clustersById.put(entity.getClusterId(), entity));
        return List.copyOf(clustersById.values());
    }

    public void updateClusterInfo(
            ClusterEntity entity, Set<String> oldAuthorizedNcaId,
            boolean isAuthorizedNcaIdUpdated) {
        try {
            clusterByIdRepo.update(entity);

            // Preserve the original comparison against the compute-platform provider name.
            if (computePlatformService.isComputePlatformBackend(entity.getClusterGroupName()) ||
                    isAuthorizedNcaIdUpdated) {
                updateForAuthorizedAccounts(entity, oldAuthorizedNcaId);
            }
        } catch (Exception e) {
            String errMsg = String.format(
                    "Cannot update cluster with name %s and id %s and group %s and group id %s.",
                    entity.getClusterName(),
                    entity.getClusterId(),
                    entity.getClusterGroupName(),
                    entity.getClusterGroupId());
            log.error("{} exception - {}", errMsg, e.getMessage(), e);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    private void updateForAuthorizedAccounts(ClusterEntity entity, Set<String> oldAuthorizedNcaId) {

        // Deleting older authorized entries
        if (!oldAuthorizedNcaId.isEmpty()) {
            deleteForAuthorizedAccounts(entity.getNcaId(), oldAuthorizedNcaId,
                                        entity.getClusterGroupName(), entity.getClusterGroupId());
        }

        // Adding updated authorized NCA-ID
        Set<String> authorizedNcaIds = entity.getAuthorizedNcaIds();
        ClusterGroupsByAuthorizedAccountsEntity clusterGroupsByAuthorizedAccountsEntity =
                toClusterGroupsByAuthorizedAccountsEntity(entity,
                                                          entity.getNcaId());
        clusterGroupsByAuthorizedAccountsEntity.getKey().setNcaIdKey(
                clusterGroupsByAuthorizedAccountsEntity.getNcaId());
        clusterGroupsByAuthorizedAccountsRepo.update(clusterGroupsByAuthorizedAccountsEntity);
        for (String ncaId : authorizedNcaIds) {
            if (ncaId.equals("*")) {
                clusterGroupsByAuthorizedAccountsEntity.getKey().setNcaIdKey(WILDCARD);
            } else {
                clusterGroupsByAuthorizedAccountsEntity.getKey().setNcaIdKey(ncaId);
            }
            clusterGroupsByAuthorizedAccountsRepo.update(clusterGroupsByAuthorizedAccountsEntity);
        }
    }


    public void deleteClusterInfo(ClusterEntity entity) {
        try {
            clusterByIdRepo.deleteById(entity.getClusterId());

            // if this is the last cluster in the cluster group then remove the group references as well
            if (getAllClustersInAGroup(entity.getClusterGroupId()).isEmpty()) {
                deleteForAuthorizedAccounts(entity.getNcaId(), entity.getAuthorizedNcaIds(),
                                            entity.getClusterGroupName(),
                                            entity.getClusterGroupId());
            }
        } catch (Exception e) {
            String errMsg = String.format(
                    "Cannot remove references of cluster with name %s and id %s and group %s and group id %s.",
                    entity.getClusterName(),
                    entity.getClusterId(),
                    entity.getClusterGroupName(),
                    entity.getClusterGroupId());
            log.error(errMsg);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    public List<ClusterByGroupIdAndIdEntity> getClustersFromClusterGroup(String clusterGroupId) {

        return clusterByIdRepo.findAllByClusterGroupId(clusterGroupId).stream()
                .map(NvcaConverter::toClusterByGroupIdAndIdEntity)
                .collect(Collectors.toList());

    }

    private void deleteForAuthorizedAccounts(
            String ncaId, Set<String> authorizedNcaIds,
            String groupName, String groupId) {
        clusterGroupsByAuthorizedAccountsRepo.deleteById(ClusterGroupsByAuthorizedAccountsKey.builder()
                        .ncaIdKey(ncaId)
                        .clusterGroupName(groupName)
                        .clusterGroupId(groupId)
                        .build());
        for (String authorizedNcaId : authorizedNcaIds) {
            if (authorizedNcaId.equals("*")) {
                clusterGroupsByAuthorizedAccountsRepo.deleteById(ClusterGroupsByAuthorizedAccountsKey.builder()
                                                                         .ncaIdKey(WILDCARD)
                                                                         .clusterGroupName(groupName)
                                                                         .clusterGroupId(groupId)
                                                                         .build());
            } else {
                clusterGroupsByAuthorizedAccountsRepo.deleteById(ClusterGroupsByAuthorizedAccountsKey.builder()
                                                                         .ncaIdKey(authorizedNcaId)
                                                                         .clusterGroupName(groupName)
                                                                         .clusterGroupId(groupId)
                                                                         .build());
            }
        }
    }

    /**
     * @param clusterId clusterId from Entity fetched from DB
     * @return {@link ClusterHealthEntity}
     * <p>
     * Calling function must pass clusterId from Entity
     * In this function we are not fetching data for hashed clusterId
     */
    public Optional<ClusterHealthEntity> getClusterHealthById(String clusterId) {
        return clusterHealthRepo.findById(clusterId);
    }

    public void saveClusterHealth(ClusterHealthEntity entity, int ttl) {
        clusterHealthRepo.insertWithTtl(entity, Duration.ofSeconds(ttl), false);
    }

    /**
     * This function should be used only from {@link ClusterHealthMonitorTask} <p>
     * This can return huge data as we are not deleting entry after marking cluster as Abandoned
     *
     * @return all {@link ClusterEntity} from db
     */
    public List<ClusterEntity> getAllClusters() {
        return clusterByIdRepo.findAll();
    }
}
