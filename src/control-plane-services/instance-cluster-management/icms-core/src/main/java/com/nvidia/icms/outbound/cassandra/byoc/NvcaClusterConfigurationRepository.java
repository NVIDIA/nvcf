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

import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterConfigurationByClusterIdEntity;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.Optional;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class NvcaClusterConfigurationRepository {

    private final ClusterConfigurationByClusterIdRepo clusterConfigurationByClusterIdRepo;

    @Observed
    public void saveOrUpdateConfiguration(
            @NotNull String clusterId,
            @Nullable Map<String, String> clusterConfigurations,
            @Nullable Map<String, String> clusterConfigurationFiles) {
        try {
            ClusterConfigurationByClusterIdEntity entity = ClusterConfigurationByClusterIdEntity.builder()
                    .clusterId(clusterId)
                    .clusterConfigurations(clusterConfigurations)
                    .clusterConfigurationFiles(clusterConfigurationFiles)
                    .build();

            // Using "updateWithInsertNulls" instead of "update" to write null values for one or more non PK column
            clusterConfigurationByClusterIdRepo.updateWithInsertNulls(entity);
        } catch (Exception e) {
            String errMsg = String.format("Failed to save/update cluster configuration for clusterId %s, error: %s",
                                          clusterId, e.getMessage());
            log.error("NvcaClusterConfigurationRepository: error: {}, exception: ", errMsg, e);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    @Observed
    public Optional<ClusterConfigurationByClusterIdEntity> findByClusterId(@NotNull String clusterId) {
        try {
            return clusterConfigurationByClusterIdRepo.findById(clusterId);
        } catch (Exception e) {
            String errMsg = String.format("Failed to fetch cluster configuration for clusterId %s, error: %s",
                                          clusterId, e.getMessage());
            log.error("NvcaClusterConfigurationRepository: error: {}, exception: ", errMsg, e);
            throw new IcmsInternalServerException(errMsg);
        }
    }

    @Observed
    public void deleteByClusterId(@NotNull String clusterId) {
        try {
            clusterConfigurationByClusterIdRepo.deleteById(clusterId);
        } catch (Exception e) {
            String errMsg = String.format("Failed to delete cluster configuration for clusterId %s, error: %s",
                                          clusterId, e.getMessage());
            log.error("NvcaClusterConfigurationRepository: error: {}, exception: ", errMsg, e);
            throw new IcmsInternalServerException(errMsg);
        }
    }
}
