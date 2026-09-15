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
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClusterGroupsByAuthorizedAccountsEntity;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClustersByAccountEntity;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getClusterIdFromAuthClientId;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAuthorizedAccountsKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterHealthEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAccountEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAccountKey;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.scheduled.request.ClusterHealthMonitorTask;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.util.HashSet;
import java.util.List;
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

    private final ClusterByIdRepo clusterByIdRepo;
    private final ClusterByGroupIdAndIdRepo clusterByGroupIdAndIdRepo;
    private final ClustersByAccountRepo clustersByAccountRepo;
    private final ClusterGroupByGroupIdRepo clusterGroupByGroupIdRepo;
    private final ClusterGroupsByAccountRepo clusterGroupsByAccountRepo;
    private final ClusterGroupsByAuthorizedAccountsRepo clusterGroupsByAuthorizedAccountsRepo;
    private final ClusterHealthRepo clusterHealthRepo;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final ComputePlatformService computePlatformService;

    public ClusterRepository(
            ClusterByIdRepo clusterByIdRepo,
            ClusterByGroupIdAndIdRepo clusterByGroupIdAndIdRepo,
            ClusterGroupByGroupIdRepo clusterGroupByGroupIdRepo,
            ClusterGroupsByAccountRepo clusterGroupsByAccountRepo,
            ClusterGroupsByAuthorizedAccountsRepo clusterGroupsByAuthorizedAccountsRepo,
            ClustersByAccountRepo clustersByAccountRepo,
            ClusterHealthRepo clusterHealthRepo,
            IcmsConfigurationProperties icmsConfigurationProperties,
            ComputePlatformService computePlatformService) {

        this.clusterByIdRepo = clusterByIdRepo;
        this.clusterByGroupIdAndIdRepo = clusterByGroupIdAndIdRepo;
        this.clusterGroupByGroupIdRepo = clusterGroupByGroupIdRepo;
        this.clusterGroupsByAccountRepo = clusterGroupsByAccountRepo;
        this.clusterGroupsByAuthorizedAccountsRepo = clusterGroupsByAuthorizedAccountsRepo;
        this.clustersByAccountRepo = clustersByAccountRepo;
        this.clusterHealthRepo = clusterHealthRepo;
        this.icmsConfigurationProperties = icmsConfigurationProperties;
        this.computePlatformService = computePlatformService;
    }

    public void saveClusterInfo(ClusterEntity entity) {

        // Todo :-  Batch insertion or cleanup if insertion to any table fails, so that subsequent
        //  requests are not affected.

        String errMsg = String.format(
                "Cannot save cluster with name %s and id %s and group %s and group id %s.",
                entity.getClusterName(),
                entity.getClusterId(),
                entity.getClusterGroupName(),
                entity.getClusterGroupId());

        if (!clusterByIdRepo.isInserted(entity) ||
                !clustersByAccountRepo.isInserted(toClustersByAccountEntity(entity)) ||
                !clusterByGroupIdAndIdRepo.isInserted(toClusterByGroupIdAndIdEntity(entity))) {
            log.error(errMsg);
            throw new IcmsConflictException(errMsg);
        }

        // insert in group only if group is not already present
        Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                getClusterGroupInfoByClusterGroupId(
                        entity.getClusterGroupId());

        if (optionalClusterGroupByGroupIdEntity.isPresent()) {
            return;
        }

        if (!clusterGroupByGroupIdRepo.isInserted(toClusterGroupByGroupIdEntity(entity)) ||
                !clusterGroupsByAccountRepo.isInserted(toClusterGroupsByAccountsEntity(entity)) ||
                !insertForAuthorizedAccounts(entity)
        ) {
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
        List<ClusterByGroupIdAndIdEntity> clusterByGroupIdAndIdEntities =
                clusterByGroupIdAndIdRepo.findByKeyClusterGroupId(clusterGroupId);
        Set<ClusterEntity> clusterEntities = new HashSet<>();
        clusterByGroupIdAndIdEntities.forEach(clusterByGroupIdAndIdEntity -> clusterEntities.add(
                toClusterEntity(clusterByGroupIdAndIdEntity)));
        return clusterEntities;
    }

    public Optional<ClusterGroupsByAccountEntity> getClusterGroupInfoByAccountAndNameInMainAccount(
            String ncaId, String clusterGroupName) {

        return clusterGroupsByAccountRepo.findByKeyNcaIdAndKeyClusterGroupName(ncaId, clusterGroupName);
    }


    public List<ClusterGroupsByAuthorizedAccountsEntity> getAllClusterGroupsInAuthorizedAccount(
            String ncaId) {

        return clusterGroupsByAuthorizedAccountsRepo.findAllByKeyNcaIdKey(ncaId);
    }

    public Optional<ClusterGroupByGroupIdEntity> getClusterGroupInfoByClusterGroupId(
            String clusterGroupId) {

        return clusterGroupByGroupIdRepo.findById(clusterGroupId);
    }

    public Optional<ClusterEntity> getClusterByAccountAndName(String ncaId, String name) {
        Optional<ClustersByAccountEntity> optionalClustersByAccountEntity =
                clustersByAccountRepo.findByKeyNcaIdAndKeyClusterName(ncaId, name);
        if (optionalClustersByAccountEntity.isEmpty()) {
            return Optional.empty();
        }
        return Optional.of(toClusterEntity(optionalClustersByAccountEntity.get()));
    }

    public List<ClusterEntity> getAllClustersInAnAccount(String ncaId) {

        List<ClustersByAccountEntity> clustersByAccountEntities;
        clustersByAccountEntities = clustersByAccountRepo.findAllByKeyNcaId(ncaId);
        return clustersByAccountEntities.stream()
                .map(NvcaConverter::toClusterEntity).collect(
                        Collectors.toList());
    }

    public void updateClusterInfo(
            ClusterEntity entity, Set<String> oldAuthorizedNcaId,
            boolean isAuthorizedNcaIdUpdated) {
        // Todo :-  Batch updation or some strategy on what to do if updation to any table fails,
        //  so that subsequent requests are not affected.
        try {
            clusterByIdRepo.update(entity);
            clusterByGroupIdAndIdRepo.update(toClusterByGroupIdAndIdEntity(entity));
            clustersByAccountRepo.update(toClustersByAccountEntity(entity));

            // oldAuthorizedNcaId will have values when authorized-nca-id update allowed and authorized-nca-ids are updated
            // NOTE: preserves the original semantics to match against the
            // compute-platform provider NAME, not the cluster-group-name. 
            if (computePlatformService.isComputePlatformBackend(entity.getClusterGroupName()) ||
                    isAuthorizedNcaIdUpdated) {
                clusterGroupByGroupIdRepo.update(toClusterGroupByGroupIdEntity(entity));
                clusterGroupsByAccountRepo.update(toClusterGroupsByAccountsEntity(entity));
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
        // Todo :-  Batch deletion or some strategy on what to do if deletion to any table fails,
        //  so that subsequent requests are not affected.
        try {
            clusterByIdRepo.deleteById(entity.getClusterId());
            clusterByGroupIdAndIdRepo.deleteById(ClusterByGroupIdAndIdKey.builder()
                                                         .clusterGroupId(entity.getClusterGroupId())
                                                         .clusterId(entity.getClusterId())
                                                         .build());

            clustersByAccountRepo.deleteById(ClustersByAccountKey.builder()
                                                     .ncaId(entity.getNcaId())
                                                     .clusterName(entity.getClusterName())
                                                     .build());

            // if this is the last cluster in the cluster group then remove the group references as well
            if (getAllClustersInAGroup(entity.getClusterGroupId()).isEmpty()) {
                clusterGroupByGroupIdRepo.deleteById(entity.getClusterGroupId());
                clusterGroupsByAccountRepo.deleteById(ClusterGroupsByAccountKey.builder()
                                                              .ncaId(entity.getNcaId())
                                                              .clusterGroupName(entity.getClusterGroupName())
                                                              .build());
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

        return clusterByGroupIdAndIdRepo.findByKeyClusterGroupId(clusterGroupId);

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
