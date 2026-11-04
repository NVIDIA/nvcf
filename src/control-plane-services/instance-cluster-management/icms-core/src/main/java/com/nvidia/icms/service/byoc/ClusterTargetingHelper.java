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
package com.nvidia.icms.service.byoc;

import static com.nvidia.icms.service.CloudHealthService.logIfStatusIsNull;
import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.isClusterTargetingEnabled;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;

import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.GpuCapacity;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ClusterTargetingHelper {

    private final NvcaClusterRepository nvcaClusterRepository;

    private final ClusterRepository clusterRepository;

    private final CloudHealthRepository cloudHealthRepository;

    private final WildCardAllowedClustersCacheService wildCardAllowedClustersCacheService;


    public static boolean isClusterHealthy(@Nullable CloudHealthEntity cloudHealth) {
        if (cloudHealth != null) {
            logIfStatusIsNull(cloudHealth, "isClusterHealthyAndCapacityAvailable", cloudHealth.getKeyZoneValue());

            return cloudHealth.getStatus() != null &&
                    cloudHealth.getStatus().equals(CloudHealthStatus.HEALTHY);
        }
        return false;
    }


    public Set<ClusterByGroupIdAndIdEntity> getWildCardAllowedClusterCachedInfo() {
        return wildCardAllowedClustersCacheService.getCachedReadyClusters();
    }


    @Observed
    public @NotNull Set<ClusterByGroupIdAndIdEntity> getReadyClusterEntitiesForNcaId(@NotNull String ncaId) {
        List<ClustersByAuthorizedAccountsEntity> allClustersInAuthorizedAccount =
                nvcaClusterRepository.getAllClustersInAuthorizedAccount(ncaId);

        Set<String> processedClusterGroups = new HashSet<>();
        Set<ClusterByGroupIdAndIdEntity> readyClusterEntities = new HashSet<>();
        for (ClustersByAuthorizedAccountsEntity authorizedAccountsEntity : allClustersInAuthorizedAccount) {

            // If cluster group is already processed, then skipping it
            if (processedClusterGroups.contains(authorizedAccountsEntity.getClusterGroupId())) {
                continue;
            }

            // Fetching ready clusters from cluster group
            processedClusterGroups.add(authorizedAccountsEntity.getClusterGroupId());
            Set<ClusterByGroupIdAndIdEntity> allReadyClusters = getAllReadyClustersFromClusterGroup(
                    authorizedAccountsEntity.getClusterGroupId());
            readyClusterEntities.addAll(allReadyClusters);
        }

        return readyClusterEntities;
    }

    public Map<String, CloudHealthEntity> getAllClusterHealthInMap() {
        return cloudHealthRepository.findAllInMap();
    }


    public static boolean isClusterHealthyAndCapacityAvailable(@Nullable CloudHealthEntity cloudHealth, @Nullable Set<String> gpuNames, boolean checkCapacity) {
        try {
            if (isClusterHealthy(cloudHealth) &&
                    cloudHealth.getGpuUsage() != null &&
                    !cloudHealth.getGpuUsage().isEmpty()) {

                if (gpuNames == null) { // if GPUs are not specify check any capacity
                    if (!checkCapacity) {
                        return true;
                    }
                    // If at least 1 gpu capacity has available gpus then return true
                    for (GpuCapacity gpuCapacity : cloudHealth.getGpuUsage().values()) {
                        if (gpuCapacity != null && gpuCapacity.getAvailable() > 0) {
                            return true;
                        }
                    }
                }
                else {
                    boolean gpuAvailable = false;
                    for (String gpuName : gpuNames) {
                        if (cloudHealth.getGpuUsage().containsKey(gpuName) &&
                                (!checkCapacity || cloudHealth.getGpuUsage().get(gpuName).getAvailable() > 0)) {
                            gpuAvailable = true;
                            break;
                        }
                    }
                    return gpuAvailable;
                }
            }
            return false;
        } catch (Exception exception) {
            log.error(
                    "CLOUD_HEALTH_STATUS_LOGGING: function: isClusterHealthyAndCapacityAvailable, failed to fetch cloud health status, cluster-id {}, error: {} exception: ",
                    cloudHealth != null ? cloudHealth.getKeyZoneValue() : "null", exception.getMessage(), exception);
            throw exception;
        }
    }


    private Set<ClusterByGroupIdAndIdEntity> getAllReadyClustersFromClusterGroup(String clusterGroupId) {
        return clusterRepository.getClustersFromClusterGroup(clusterGroupId)
                .stream()
                .filter(entity -> ClusterStatusEnum.READY.equals(entity.getClusterStatus())
                        && entity.getNvcaVersion() != null
                        && isClusterTargetingEnabled(entity.getAllowClusterTargeting())
                        && !isSetEmptyOrNull(NvcaConverter.getGpusV5(entity)))
                .collect(Collectors.toSet());
    }
}
