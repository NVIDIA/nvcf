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

import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getClusterIdFromAuthClientId;

import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterHealthEntity;
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
public class ClusterRepository {

    public static final String WILDCARD = "WILDCARD";

    // Wildcard marker stored in the authorized_nca_ids column of cluster_by_cluster_id.
    public static final String AUTHORIZED_NCA_IDS_WILDCARD = "*";

    private final ClusterByIdRepo clusterByIdRepo;
    private final ClusterHealthRepo clusterHealthRepo;

    public ClusterRepository(
            ClusterByIdRepo clusterByIdRepo,
            ClusterHealthRepo clusterHealthRepo) {

        this.clusterByIdRepo = clusterByIdRepo;
        this.clusterHealthRepo = clusterHealthRepo;
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

    public void updateClusterInfo(ClusterEntity entity) {
        try {
            clusterByIdRepo.update(entity);
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

    public List<ClusterByGroupIdAndIdEntity> getClustersFromClusterGroup(String clusterGroupId) {

        return clusterByIdRepo.findAllByClusterGroupId(clusterGroupId).stream()
                .map(NvcaConverter::toClusterByGroupIdAndIdEntity)
                .collect(Collectors.toList());

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
