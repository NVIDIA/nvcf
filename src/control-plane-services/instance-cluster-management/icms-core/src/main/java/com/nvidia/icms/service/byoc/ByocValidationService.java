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

import static com.nvidia.icms.inbound.rest.model.CloudProvider.getCloudProviderFromClusterProvider;
import static com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum.READY;
import static com.nvidia.icms.util.InstanceServiceUtil.getStringValue;
import static com.nvidia.icms.util.InstanceServiceUtil.isRequestForTask;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;
import static com.nvidia.icms.util.InstanceServiceUtil.isTargetingEnabled;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClustersByAuthorizedAccountsEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.service.InstanceServiceHelper;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
public class ByocValidationService {

    private final ClusterRepository clusterRepository;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final InstanceServiceHelper instanceServiceHelper;
    private final InstanceLifecycleHelper instanceLifecycleHelper;
    private final ComputePlatformService computePlatformService;

    public boolean validateNotEmpty(@Nullable String ncaId, @Nullable String gpu) {
        if (StringUtils.isEmpty(ncaId)) {
            String errMsg = "LaunchSpecification.NcaId can't be empty";
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }

        if (StringUtils.isEmpty(gpu)) {
            String errMsg = "LaunchSpecification.Gpu can't be empty";
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }

        return true;
    }


    public @Nullable CloudProvider validateClustersStatusAndGetProviderForNvca(String clusterId) {
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);
        if (optionalClusterEntity.isEmpty()) {
            String errMsg = String.format("Could not find a cluster with %s clusterId", clusterId);
            log.error(errMsg);
            throw new IcmsInternalServerException(errMsg);
        }

        ClusterEntity clusterEntity = optionalClusterEntity.get();
        if (clusterEntity.getClusterStatus().equals(READY)) {
            CloudProvider cloudProvider = getCloudProviderFromClusterProvider(
                    clusterEntity.getClusterProvider());

            if (cloudProvider == null) {
                log.error(
                        "Could not able to find cloud provider for cluster - {} cluster-group {}, resource provider - {}",
                        clusterEntity.getClusterName(), clusterEntity.getClusterGroupName(),
                        clusterEntity.getClusterProvider());

                throw new IcmsInternalServerException(
                        String.format("Failed to find cloudProvider for %s clusterProvider",
                                      clusterEntity.getClusterProvider()));
            }

            return cloudProvider;
        }

        log.info(
                "cluster with id {} name {} is not READY in cluster group with id {} name {} to serve instance request in NVCA",
                clusterEntity.getClusterId(), clusterEntity.getClusterName(),
                clusterEntity.getClusterGroupId(), clusterEntity.getClusterGroupName());
        return null;
    }

    @Observed
    public @Nullable String getCreationQueueForReadyCluster(
            SpotInstanceRequestSchema instanceRequest,
            ClusterByGroupIdAndIdEntity entity,
            String providedGpuName) {

        // If Nats is enabled then return queue creation as empty for cluster as stream will be used
        if (instanceServiceHelper.isNatsEnabled()) {
            return "";
        }

        // Handling creation queue URL generation for first-party compute platform clusters
        if (computePlatformService.isPlatformCluster(entity.getClusterGroupName())) {
            return getCreationQueueForNonByoc(instanceRequest, entity, providedGpuName);

        } else {
            // Handling creation queue URL generation for NVCA
            return getCreationQueueForNvca(instanceRequest, entity, providedGpuName);
        }
    }

    private String getCreationQueueForNvca(
            SpotInstanceRequestSchema instanceRequest,
            ClusterByGroupIdAndIdEntity entity,
            String providedGpuName) {

        // If request is for task and task specific queues are enabled from SIS and NVCA
        // then use task specific cluster creation queue
        if (isRequestForTask(instanceRequest) &&
                instanceServiceHelper.isTaskClusterCreationQueuesAllowed(entity.getAllowTaskClusterCreationQueues())) {
            return getTasksCusterCreationQueueUrlForGpu(entity, providedGpuName);
        }

        // Using cluster specific function creation queue
        return getCusterCreationQueueUrlForGpu(entity, providedGpuName);
    }

    private String getCreationQueueForNonByoc(
            SpotInstanceRequestSchema instanceRequest,
            ClusterByGroupIdAndIdEntity entity,
            String providedGpuName) {

        if (instanceServiceHelper.isNatsEnabled()) {
            return "";
        }

        // For task, we will use global creation queue of task for respective gpu
        if (isRequestForTask(instanceRequest)) {
            return instanceLifecycleHelper.getGlobalCreationQueueUrlForNonByoc(providedGpuName, true);

        // If region filter not provided then we will use global creation queue fof function for respective gpu
        } else if (isSetEmptyOrNull(instanceRequest.getRegions())) {
            return instanceLifecycleHelper.getGlobalCreationQueueUrlForNonByoc(providedGpuName, false);
        }

        // Using region specific queue URL configured at the time of registration
        return getCusterCreationQueueUrlForGpu(entity, providedGpuName);
    }

    private static String getCusterCreationQueueUrlForGpu(ClusterByGroupIdAndIdEntity entity,
                                                   String gpuName) {
        Map<String, CreationQueueUdt> clusterCreationQueueMap =
                entity.getClusterCreationQueues() == null
                        ? new HashMap<>()
                        : entity.getClusterCreationQueues();

        if (clusterCreationQueueMap.containsKey(gpuName)) {
            return clusterCreationQueueMap.get(gpuName).getUrl();
        }

        String errMsg = String.format(
                "For %s GPU in %s clusterId and %s clusterGroupId cluster creation queue doesn't exists",
                gpuName, entity.getKey().getClusterId(), entity.getKey().getClusterGroupId());
        log.error(errMsg);

        throw new PreConditionFailedException(
                String.format("For %s GPU %s backend is under reconfiguration", gpuName,
                              entity.getClusterGroupName()));
    }

    private static String getTasksCusterCreationQueueUrlForGpu(ClusterByGroupIdAndIdEntity entity,
                                                               String gpuName) {
        Map<String, CreationQueueUdt> clusterCreationQueueMapForTasks =
                entity.getClusterCreationQueuesForTasks() == null
                        ? new HashMap<>()
                        : entity.getClusterCreationQueuesForTasks();

        if (clusterCreationQueueMapForTasks.containsKey(gpuName)) {
            return clusterCreationQueueMapForTasks.get(gpuName).getUrl();
        }

        String errMsg = String.format(
                "For %s GPU in %s clusterId and %s clusterGroupId tasks cluster creation queue doesn't exists. Existing queues: %s",
                gpuName, entity.getKey().getClusterId(), entity.getKey().getClusterGroupId(), clusterCreationQueueMapForTasks.toString());
        log.error(errMsg);

        throw new PreConditionFailedException(
                String.format("For %s GPU %s backend is under reconfiguration", gpuName,
                              entity.getClusterGroupName()));
    }

    public String getByocCreationQueueUrlForGpu(
            ClustersByAuthorizedAccountsEntity entity,
            String gpuName) {

        if (instanceServiceHelper.isNatsEnabled()) {
            return "";
        }

        Map<String, CreationQueueUdt> creationQueueMap =
                entity.getCreationQueues() == null ? new HashMap<>() : entity.getCreationQueues();
        if (creationQueueMap.containsKey(gpuName)) {
            return creationQueueMap.get(gpuName).getUrl();
        }

        String errMsg = String.format(
                "For %s GPU in %s clusterId and %s clusterGroupId creation queue doesn't exists",
                gpuName, entity.getKey().getClusterId(), entity.getClusterGroupId());
        log.error(errMsg);

        throw new PreConditionFailedException(
                String.format("For %s GPU %s backend is under reconfiguration", gpuName,
                        entity.getClusterGroupName()));
    }



    public int getRequiredGpuCountForInstance(int gpuCountInRequest) {
        if (gpuCountInRequest == 0 || gpuCountInRequest == 1) {
            return 1;
        } else if (gpuCountInRequest > 1) {
            return gpuCountInRequest;
        } else {
            return 0;
        }
    }

}
