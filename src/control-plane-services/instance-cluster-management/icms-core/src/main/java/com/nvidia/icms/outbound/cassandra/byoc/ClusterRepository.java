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

import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClusterByGroupIdAndIdEntity;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClusterEntity;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClusterGroupByGroupIdEntity;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClusterGroupsByAccountsEntity;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClustersByAccountEntity;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getClusterIdFromAuthClientId;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterHealthEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAccountEntity;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.scheduled.request.ClusterHealthMonitorTask;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.util.HashSet;
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
    private final ClusterByGroupIdAndIdRepo clusterByGroupIdAndIdRepo;
    private final ClustersByAccountRepo clustersByAccountRepo;
    private final ClusterGroupByGroupIdRepo clusterGroupByGroupIdRepo;
    private final ClusterGroupsByAccountRepo clusterGroupsByAccountRepo;
    private final ClusterHealthRepo clusterHealthRepo;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final ComputePlatformService computePlatformService;

    public ClusterRepository(
            ClusterByIdRepo clusterByIdRepo,
            ClusterByGroupIdAndIdRepo clusterByGroupIdAndIdRepo,
            ClusterGroupByGroupIdRepo clusterGroupByGroupIdRepo,
            ClusterGroupsByAccountRepo clusterGroupsByAccountRepo,
            ClustersByAccountRepo clustersByAccountRepo,
            ClusterHealthRepo clusterHealthRepo,
            IcmsConfigurationProperties icmsConfigurationProperties,
            ComputePlatformService computePlatformService) {

        this.clusterByIdRepo = clusterByIdRepo;
        this.clusterByGroupIdAndIdRepo = clusterByGroupIdAndIdRepo;
        this.clusterGroupByGroupIdRepo = clusterGroupByGroupIdRepo;
        this.clusterGroupsByAccountRepo = clusterGroupsByAccountRepo;
        this.clustersByAccountRepo = clustersByAccountRepo;
        this.clusterHealthRepo = clusterHealthRepo;
        this.icmsConfigurationProperties = icmsConfigurationProperties;
        this.computePlatformService = computePlatformService;
    }

    /**
     * Write-path group lookup. Always reads the legacy group table, ignoring
     * {@code icms.cluster-by-id-reads-enabled}: the cluster row is already in
     * cluster_by_cluster_id when the save flow checks group existence, so an
     * SAI-backed read would always find it and skip the legacy group writes.
     */
    public Optional<ClusterGroupByGroupIdEntity> getLegacyClusterGroupInfoByClusterGroupId(
            String clusterGroupId) {
        return clusterGroupByGroupIdRepo.findById(clusterGroupId);
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
        if (icmsConfigurationProperties.isClusterByIdReadsEnabled()) {
            return clusterByIdRepo.findAllByClusterGroupId(clusterGroupId).stream()
                    .collect(Collectors.toSet());
        }

        List<ClusterByGroupIdAndIdEntity> clusterByGroupIdAndIdEntities =
                clusterByGroupIdAndIdRepo.findByKeyClusterGroupId(clusterGroupId);
        Set<ClusterEntity> clusterEntities = new HashSet<>();
        clusterByGroupIdAndIdEntities.forEach(clusterByGroupIdAndIdEntity -> clusterEntities.add(
                toClusterEntity(clusterByGroupIdAndIdEntity)));
        return clusterEntities;
    }

    public Optional<ClusterGroupsByAccountEntity> getClusterGroupInfoByAccountAndNameInMainAccount(
            String ncaId, String clusterGroupName) {

        if (icmsConfigurationProperties.isClusterByIdReadsEnabled()) {
            // No index on cluster_group_name: query by nca_id SAI and filter in memory.
            return clusterByIdRepo.findAllByNcaId(ncaId).stream()
                    .filter(entity -> clusterGroupName.equals(entity.getClusterGroupName()))
                    .findFirst()
                    .map(NvcaConverter::toClusterGroupsByAccountsEntity);
        }

        return clusterGroupsByAccountRepo.findByKeyNcaIdAndKeyClusterGroupName(ncaId, clusterGroupName);
    }


    public Optional<ClusterGroupByGroupIdEntity> getClusterGroupInfoByClusterGroupId(
            String clusterGroupId) {

        if (icmsConfigurationProperties.isClusterByIdReadsEnabled()) {
            return clusterByIdRepo.findAllByClusterGroupId(clusterGroupId).stream()
                    .findFirst()
                    .map(NvcaConverter::toClusterGroupByGroupIdEntity);
        }

        return clusterGroupByGroupIdRepo.findById(clusterGroupId);
    }

    public Optional<ClusterEntity> getClusterByAccountAndName(String ncaId, String name) {
        if (icmsConfigurationProperties.isClusterByIdReadsEnabled()) {
            // No index on cluster_name: query by nca_id SAI and filter in memory.
            return clusterByIdRepo.findAllByNcaId(ncaId).stream()
                    .filter(entity -> name.equals(entity.getClusterName()))
                    .findFirst();
        }
        Optional<ClustersByAccountEntity> optionalClustersByAccountEntity =
                clustersByAccountRepo.findByKeyNcaIdAndKeyClusterName(ncaId, name);
        if (optionalClustersByAccountEntity.isEmpty()) {
            return Optional.empty();
        }
        return Optional.of(toClusterEntity(optionalClustersByAccountEntity.get()));
    }

    public List<ClusterEntity> getAllClustersInAnAccount(String ncaId) {

        if (icmsConfigurationProperties.isClusterByIdReadsEnabled()) {
            return clusterByIdRepo.findAllByNcaId(ncaId).stream()
                    .collect(Collectors.toList());
        }

        List<ClustersByAccountEntity> clustersByAccountEntities;
        clustersByAccountEntities = clustersByAccountRepo.findAllByKeyNcaId(ncaId);
        return clustersByAccountEntities.stream()
                .map(NvcaConverter::toClusterEntity).collect(
                        Collectors.toList());
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
        // Todo :-  Batch updation or some strategy on what to do if updation to any table fails,
        //  so that subsequent requests are not affected.
        try {
            clusterByIdRepo.update(entity);
            clusterByGroupIdAndIdRepo.update(toClusterByGroupIdAndIdEntity(entity));
            clustersByAccountRepo.update(toClustersByAccountEntity(entity));

            if (computePlatformService.isComputePlatformBackend(entity.getClusterGroupName())) {
                clusterGroupByGroupIdRepo.update(toClusterGroupByGroupIdEntity(entity));
                clusterGroupsByAccountRepo.update(toClusterGroupsByAccountsEntity(entity));
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

    public List<ClusterByGroupIdAndIdEntity> getClustersFromClusterGroup(String clusterGroupId) {

        if (icmsConfigurationProperties.isClusterByIdReadsEnabled()) {
            return clusterByIdRepo.findAllByClusterGroupId(clusterGroupId).stream()
                    .map(NvcaConverter::toClusterByGroupIdAndIdEntity)
                    .collect(Collectors.toList());
        }

        return clusterByGroupIdAndIdRepo.findByKeyClusterGroupId(clusterGroupId);

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
