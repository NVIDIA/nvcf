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
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClusterGroupByGroupIdEntity;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClusterGroupsByAccountsEntity;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClustersByAccountEntity;
import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClustersByAuthorizedAccountsEntity;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupsByAccountKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAccountKey;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsKey;

import java.util.List;
import java.util.Optional;
import java.util.Set;

import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class NvcaClusterRepository {

    public static final String WILDCARD = "WILDCARD";

    private final ClusterByIdRepo clusterByIdRepo;
    private final ClusterByGroupIdAndIdRepo clusterByGroupIdAndIdRepo;
    private final ClustersByAccountRepo clustersByAccountRepo;
    private final ClusterGroupByGroupIdRepo clusterGroupByGroupIdRepo;
    private final ClusterGroupsByAccountRepo clusterGroupsByAccountRepo;
    private final ClustersByAuthorizedAccountsRepo clustersByAuthorizedAccountsRepo;

    private final ClusterRepository clusterRepository;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    /*
    Updating NVCA registration in below tables:
    1. cluster_by_cluster_id
    2. cluster_by_group_id_and_cluster_id
    3. clusters_by_account
    4. clusters_by_authorized_accounts
     */
    public void updateClusterRegistration(ClusterEntity clusterEntity) {
        try {

            clusterByIdRepo.update(clusterEntity);
            clusterByGroupIdAndIdRepo.update(toClusterByGroupIdAndIdEntity(clusterEntity));
            clustersByAccountRepo.update(toClustersByAccountEntity(clusterEntity));

            ClustersByAuthorizedAccountsEntity clustersByAuthorizedAccountsEntity =
                    toClustersByAuthorizedAccountsEntity(clusterEntity, clusterEntity.getNcaId());

            // Updating primary nca-id entity in DB
            clustersByAuthorizedAccountsRepo.update(clustersByAuthorizedAccountsEntity);

            // Updating authorizedNcaId entity in DB
            Set<String> authorizedNcaIds = clusterEntity.getAuthorizedNcaIds();
            for (String ncaId : authorizedNcaIds) {
                if (ncaId.equals("*")) {
                    clustersByAuthorizedAccountsEntity.getKey().setNcaIdKey(WILDCARD);
                } else {
                    clustersByAuthorizedAccountsEntity.getKey().setNcaIdKey(ncaId);
                }
                clustersByAuthorizedAccountsRepo.update(clustersByAuthorizedAccountsEntity);
            }
        } catch (Exception exception) {
            String errMsg = String.format(
                    "Failed to update cluster registration in DB, cluster-id: %s error: %s",
                    clusterEntity.getClusterId(),
                    exception.getMessage());
            log.error(errMsg);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    public void updateClusterConfiguration(ClusterEntity clusterEntity, Set<String> oldAuthNcaIds) {
        try {
            clusterByIdRepo.update(clusterEntity);
            clusterByGroupIdAndIdRepo.update(toClusterByGroupIdAndIdEntity(clusterEntity));
            clustersByAccountRepo.update(toClustersByAccountEntity(clusterEntity));
            // Delete old auth entries and add new entries
            updateForAuthorizedAccountsForCluster(clusterEntity, oldAuthNcaIds);

        } catch (Exception exception) {
            String errMsg = String.format(
                    "Failed to update cluster configuration in DB, cluster-id: %s error: %s",
                    clusterEntity.getClusterId(),
                    exception.getMessage());
            log.error(errMsg);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    /**
     * We should use this function ONLY if we are updating fields which are ONLY part of ClusterEntity
     * If the fields are duplicated in other Entities for NCAID then we should use {@link #updateClusterConfiguration}
     */
    public void updateClusterEntity(@NotNull ClusterEntity clusterEntity) {

        try {
            clusterByIdRepo.update(clusterEntity);
        } catch (Exception exception) {
            String errMsg = String.format(
                    "Failed to update clusterEntity in DB, cluster-id: %s error: %s",
                    clusterEntity.getClusterId(),
                    exception.getMessage());
            log.error("Class: NvcaClusterRepository, function: updateClusterEntity, error: {}, exception: ", errMsg, exception);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    public List<ClustersByAuthorizedAccountsEntity> getAllClustersInAuthorizedAccount(
            String ncaId) {
        return clustersByAuthorizedAccountsRepo.findAllByKeyNcaIdKey(ncaId);
    }

    public void saveClusterInfo(ClusterEntity entity) {

        String errMsg = String.format(
                "Cannot save cluster with name %s and id %s and group %s and group id %s.",
                entity.getClusterName(),
                entity.getClusterId(),
                entity.getClusterGroupName(),
                entity.getClusterGroupId());

        if (!clusterByIdRepo.isInserted(entity) ||
                !clustersByAccountRepo.isInserted(toClustersByAccountEntity(entity)) ||
                !clusterByGroupIdAndIdRepo.isInserted(toClusterByGroupIdAndIdEntity(entity)) ||
                !insertForAuthorizedAccountsInClusterEntity(entity)
        ) {
            log.error(errMsg);
            try {
                // Try delete entry from previous table
                clusterByIdRepo.deleteById(entity.getClusterId());
                clustersByAccountRepo.deleteById(ClustersByAccountKey.builder()
                                                         .ncaId(entity.getNcaId())
                                                         .clusterName(entity.getClusterName())
                                                         .build());
                clusterByGroupIdAndIdRepo.deleteById(ClusterByGroupIdAndIdKey.builder()
                                                             .clusterGroupId(entity.getClusterGroupId())
                                                             .clusterId(entity.getClusterId())
                                                             .build());
                deleteForAuthorizedAccountsInClusterEntity(entity.getNcaId(),
                                                           entity.getAuthorizedNcaIds(),
                                                           entity.getClusterId());
            } catch (Exception e) {
                log.error("NvcaClusterRepository: Failed to delete entity, clusterName {} clusterId {}, error- {}, exception-",
                          entity.getClusterName(), entity.getClusterId(), e.getMessage(), e);
            }
            throw new IcmsConflictException(errMsg);
        }

        // insert in group only if group is not already present
        Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                clusterRepository.getClusterGroupInfoByClusterGroupId(
                        entity.getClusterGroupId());
        if (optionalClusterGroupByGroupIdEntity.isPresent()) {
            return;
        }

        // TODO: Decide if we should add authorized nca id as union for all clusters at group level
        // Currently the authorized nca_ids will be the first registered clusters nca_id
        if (!clusterGroupByGroupIdRepo.isInserted(toClusterGroupByGroupIdEntity(entity)) ||
                !clusterGroupsByAccountRepo.isInserted(toClusterGroupsByAccountsEntity(entity))) {
            // NOTE: We are not adding the changes in table !clusterRepository.insertForAuthorizedAccounts(entity)
            // Since this table is only needed in NVCA 1.0 old flow as nca id keys were at cluster group level
            log.error(errMsg);
            try {
                // Try deleting entry from previous table
                clusterByIdRepo.deleteById(entity.getClusterId());
                clustersByAccountRepo.deleteById(ClustersByAccountKey.builder()
                                                         .ncaId(entity.getNcaId())
                                                         .clusterName(entity.getClusterName())
                                                         .build());
                clusterByGroupIdAndIdRepo.deleteById(ClusterByGroupIdAndIdKey.builder()
                                                             .clusterGroupId(entity.getClusterGroupId())
                                                             .clusterId(entity.getClusterId())
                                                             .build());

                deleteForAuthorizedAccountsInClusterEntity(entity.getNcaId(),
                                                           entity.getAuthorizedNcaIds(),
                                                           entity.getClusterId());
                clusterGroupsByAccountRepo.deleteById(ClusterGroupsByAccountKey.builder()
                                                              .ncaId(entity.getNcaId())
                                                              .clusterGroupName(entity.getClusterGroupName())
                                                              .build());
                clusterGroupByGroupIdRepo.deleteById(entity.getClusterGroupId());
            } catch (Exception e) {
                log.error("Failed while deleting entries from DB, error: {}", e.getMessage(), e);
            }
            throw new IcmsConflictException(errMsg);
        }

    }

    private boolean insertForAuthorizedAccountsInClusterEntity(ClusterEntity entity) {
        Set<String> authorizedNcaIds = entity.getAuthorizedNcaIds();
        ClustersByAuthorizedAccountsEntity clustersByAuthorizedAccountsEntity =
                toClustersByAuthorizedAccountsEntity(entity, entity.getNcaId());
        if (!clustersByAuthorizedAccountsRepo.isInserted(clustersByAuthorizedAccountsEntity)) {
            return false;
        }
        for (String ncaId : authorizedNcaIds) {
            if (ncaId.equals("*")) {
                clustersByAuthorizedAccountsEntity.getKey().setNcaIdKey(WILDCARD);
            } else {
                clustersByAuthorizedAccountsEntity.getKey().setNcaIdKey(ncaId);
            }
            if (!clustersByAuthorizedAccountsRepo.isInserted(clustersByAuthorizedAccountsEntity)) {
                return false;
            }
        }
        return true;
    }

    private void deleteForAuthorizedAccountsInClusterEntity(
            String ncaId,
            Set<String> authorizedNcaIds,
            String clusterId) {

        clustersByAuthorizedAccountsRepo.deleteById(ClustersByAuthorizedAccountsKey.builder()
                                                        .ncaIdKey(ncaId)
                                                        .clusterId(clusterId)
                                                        .build());
        for (String authorizedNcaId : authorizedNcaIds) {
            if (authorizedNcaId.equals("*")) {
                clustersByAuthorizedAccountsRepo.deleteById(ClustersByAuthorizedAccountsKey.builder()
                                                                    .ncaIdKey(WILDCARD)
                                                                    .clusterId(clusterId)
                                                                    .build());
            } else {
                clustersByAuthorizedAccountsRepo.deleteById(ClustersByAuthorizedAccountsKey.builder()
                                                                    .ncaIdKey(authorizedNcaId)
                                                                    .clusterId(clusterId)
                                                                    .build());
            }
        }
    }

    private void updateForAuthorizedAccountsForCluster(
            ClusterEntity entity, Set<String> oldAuthorizedNcaId) {

        // Deleting older authorized entries
        if (!oldAuthorizedNcaId.isEmpty()) {
            deleteForAuthorizedAccountsInClusterEntity(entity.getNcaId(), oldAuthorizedNcaId,
                    entity.getClusterId());
        }

        // Adding updated authorized NCA-ID
        Set<String> authorizedNcaIds = entity.getAuthorizedNcaIds();
        ClustersByAuthorizedAccountsEntity clustersByAuthorizedAccountsEntity =
                toClustersByAuthorizedAccountsEntity(entity, entity.getNcaId());
        clustersByAuthorizedAccountsEntity.getKey().setNcaIdKey(
                clustersByAuthorizedAccountsEntity.getNcaId());
        clustersByAuthorizedAccountsRepo.isInserted(clustersByAuthorizedAccountsEntity);
        for (String ncaId : authorizedNcaIds) {
            if (ncaId.equals("*")) {
                clustersByAuthorizedAccountsEntity.getKey().setNcaIdKey(WILDCARD);
            } else {
                clustersByAuthorizedAccountsEntity.getKey().setNcaIdKey(ncaId);
            }
            clustersByAuthorizedAccountsRepo.isInserted(clustersByAuthorizedAccountsEntity);
        }
    }

    public void deleteClusterInfo(ClusterEntity entity) {
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
            // Delete from clustersByAuthorizedAccountsDao
            deleteForAuthorizedAccountsInClusterEntity(entity.getNcaId(),
                                                       entity.getAuthorizedNcaIds(),
                                                       entity.getClusterId());

            // if this is the last cluster in the cluster group then remove the group references as well
            if (clusterRepository.getAllClustersInAGroup(entity.getClusterGroupId()).isEmpty()) {
                clusterGroupByGroupIdRepo.deleteById(entity.getClusterGroupId());
                clusterGroupsByAccountRepo.deleteById(ClusterGroupsByAccountKey.builder()
                                                              .ncaId(entity.getNcaId())
                                                              .clusterGroupName(entity.getClusterGroupName())
                                                              .build());
            }
        } catch (Exception e) {
            log.error("NvcaClusterRepository: Failed to delete cluster info, clusterName {} clusterId {}, error- {}, exception-",
                      entity.getClusterName(), entity.getClusterId(), e.getMessage(), e);
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

}
