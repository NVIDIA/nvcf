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

import static com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter.toClustersByAuthorizedAccountsEntity;

import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;

import java.util.List;
import java.util.stream.Collectors;

import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class NvcaClusterRepository {

    private final ClusterByIdRepo clusterByIdRepo;
    private final ClusterRepository clusterRepository;

    public void updateClusterRegistration(ClusterEntity clusterEntity) {
        try {

            clusterByIdRepo.update(clusterEntity);
        } catch (Exception exception) {
            String errMsg = String.format(
                    "Failed to update cluster registration in DB, cluster-id: %s error: %s",
                    clusterEntity.getClusterId(),
                    exception.getMessage());
            log.error(errMsg);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    public void updateClusterConfiguration(ClusterEntity clusterEntity) {
        try {
            clusterByIdRepo.update(clusterEntity);

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
     * Updates a cluster row without changing its configuration.
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
        return clusterRepository.getAllClustersForAuthorizationKey(ncaId).stream()
                .map(entity -> toClustersByAuthorizedAccountsEntity(entity, entity.getNcaId()))
                .collect(Collectors.toList());
    }

    public void saveClusterInfo(ClusterEntity entity) {

        String errMsg = String.format(
                "Cannot save cluster with name %s and id %s and group %s and group id %s.",
                entity.getClusterName(),
                entity.getClusterId(),
                entity.getClusterGroupName(),
                entity.getClusterGroupId());

        // TODO: Replace isInserted and its result check with a simple insert command.
        if (!clusterByIdRepo.isInserted(entity)) {
            log.error(errMsg);
            throw new IcmsConflictException(errMsg);
        }
    }

    public void deleteClusterInfo(ClusterEntity entity) {
        try {
            clusterByIdRepo.deleteById(entity.getClusterId());
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
