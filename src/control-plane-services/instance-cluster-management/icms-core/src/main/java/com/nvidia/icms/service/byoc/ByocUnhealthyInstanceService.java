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

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.fnds.model.FndsStages;
import com.nvidia.icms.service.FunctionDeploymentStagesService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.scheduled.instance.UnhealthyInstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

@Service
@Slf4j
@AllArgsConstructor
public class ByocUnhealthyInstanceService {

    private static final String WILDCARD = "*";
    private static final String UNHEALTHY_INSTANCE_ERROR_LOG = "Terminated instance from unhealthy cloud";

    private final ByocTerminateService byocTerminateService;
    private final ClusterRepository clusterRepository;
    private final InstanceV2Repository instanceV2Repository;
    private final FunctionDeploymentStagesService functionDeploymentStagesService;
    private final InstanceServiceHelper instanceServiceHelper;
    private final TelemetryEventClient telemetryEventClient;
    private final ByocConfigurationProperties byocConfigurationProperties;
    private final UnhealthyInstanceServiceHelper unhealthyInstanceServiceHelper;

    public void persistAndTerminate(@NotNull List<InstanceV2Entity> unhealthyInstances) {
        if (unhealthyInstances.isEmpty()) {
            return;
        }

        Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceEntitiesMap = new HashMap<>();
        Map<String, InstanceV2Entity> entityBefore = new HashMap<>();
        List<InstanceV2Entity> entityAfter = new ArrayList<>();
        Map<String, ClusterEntity> clusterIdToUnhealthyClusterEntityMap = new HashMap<>();

        for (InstanceV2Entity entity : unhealthyInstances) {

            // Collecting data for unhealthy cluster
            // For BYOC zone name will be cluster id
            if (!clusterIdToUnhealthyClusterEntityMap.containsKey(entity.getZone())) {

                // If cluster info is missing due to un-registration then terminate instance
                Optional<ClusterEntity> clusterEntity = getClusterInfoByClusterId(entity.getZone());

                if (clusterEntity.isEmpty()) {
                    // Terminate instance state and update in database
                    InstanceV2Entity instanceEntityBefore = CopyUtil.deepCopy(entity);
                    byocTerminateService.handleInstancesWithMissingClusterInfo(entity);

                    // Sending GPU usage event for terminated instance
                    instanceServiceHelper.gpuUsageEventForTerminatedInstance(entity);

                    // Send latest instance state event
                    instanceServiceHelper.sendLatestInstanceStateEvent(entity);

                    unhealthyInstanceServiceHelper.sendAuditForUnhealthyInstance(entity, instanceEntityBefore);
                    continue;
                }
                clusterIdToUnhealthyClusterEntityMap.put(entity.getZone(), clusterEntity.get());
            }

            // Collecting instance info only for termination enabled cluster-ids
            ClusterEntity clusterEntity = clusterIdToUnhealthyClusterEntityMap.get(entity.getZone());
            if (isClusterEnabledForInstanceTermination(clusterEntity)) {
                entityBefore.put(entity.getInstanceId(), CopyUtil.deepCopy(entity));

                // Adding entries in requestIdToClusterEntityMap
                requestIdToClusterEntitiesMap.computeIfAbsent(
                                entity.getRequestId(), key -> new HashSet<>(Set.of(clusterEntity)))
                        .add(clusterEntity);

                // Adding entries in requestIdToInstanceIdsMap
                requestIdToInstanceEntitiesMap.computeIfAbsent(entity.getRequestId(),
                                                               key -> new HashSet<>(Set.of(entity)))
                        .add(entity);

                entityAfter.add(byocTerminateService.updateInstanceEntityState(entity,
                                                                                   UNHEALTHY_INSTANCE_ERROR_LOG));
            }
        }

        // Sending telemetry event for unhealthy BYOC clusters
        sendTelemetryForUnhealthyCloud(clusterIdToUnhealthyClusterEntityMap);

        // Terminating whitelisted instances
        terminateInstancesFromUnhealthyCloud(requestIdToClusterEntitiesMap,
                                             requestIdToInstanceEntitiesMap,
                                             entityAfter,
                                             entityBefore);
    }

    public static GenericMetric getMetricForCloudOffline(ClusterEntity clusterEntity) {
        Map<String, Object> metadata = new HashMap<>();
        metadata.put(EventMetaData.NCA_ID.getName(), clusterEntity.getNcaId());
        metadata.put(EventMetaData.AUTHORIZED_NCA_ID.getName(), clusterEntity.getAuthorizedNcaIds());

        log.info("ByocUnhealthyInstanceService: Detected unhealthy BYOC cloud with cluster-info: {} ", metadata);
        return new GenericMetric()
                .withEventName(Events.UNHEALTHY_CLOUD.toString())
                .withMetadata(metadata)
                .withResourceProvider(ResourceProvider.BYOC)
                .withClusterId(clusterEntity.getClusterId())
                .withClusterName(clusterEntity.getClusterName())
                .withCloudProvider(CloudProvider
                                           .getCloudProviderFromClusterProvider(
                                                   clusterEntity.getClusterProvider()));
    }

    private void terminateInstancesFromUnhealthyCloud(
            Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap,
            Map<String, Set<InstanceV2Entity>> requestIdToInstanceEntitiesMap,
            List<InstanceV2Entity> entityAfter,
            Map<String, InstanceV2Entity> entityBefore) {

        if (requestIdToClusterEntitiesMap.isEmpty()) {
            return;
        }

        log.debug("ByocUnhealthyInstanceService: Updating unhealthy BYOC instances state in db and send termination"
                          + " message, request-ids {}",
                  requestIdToInstanceEntitiesMap.keySet());

        // 1. Publishing message for instance termination
        // If SQS publish fails, abort early — no DB state has been modified yet
        try {
            byocTerminateService.sendSqsMessageForInstanceTermination(requestIdToClusterEntitiesMap,
                                                                      requestIdToInstanceEntitiesMap);
        } catch (Exception exception) {
            log.error("ByocUnhealthyInstanceService: Failed to send SQS termination message for BYOC instances,"
                              + " error: {}",
                      exception.getMessage(), exception);
            return;
        }

        // 2. Updating InstanceV2Entity
        // 3. Sending audit and telemetry data
        // Each entity is processed independently so one failure does not block the remaining instances
        entityAfter.forEach(entity -> {
            try {
                instanceV2Repository.update(entity);
                functionDeploymentStagesService.sendFunctionDeploymentStage(entity,
                                                                            FndsStages.STAGE_DESTROYED.toString());
                unhealthyInstanceServiceHelper.sendAuditForUnhealthyInstance(
                        entityBefore.get(entity.getInstanceId()), entity);

                // Sending GPU usage event for terminated instance
                instanceServiceHelper.gpuUsageEventForTerminatedInstance(entity);

                // Send latest instance state event
                instanceServiceHelper.sendLatestInstanceStateEvent(entity);

                Optional<ClusterEntity> optionalClusterEntity = requestIdToClusterEntitiesMap
                        .get(entity.getRequestId()).stream()
                        .filter(cluster -> cluster.getClusterId().equals(entity.getZone()))
                        .findFirst();
                // ClusterEntity will be always present as we are making sure that
                // One telemetry event per InstanceV2Entity is being sent
                // ClusterEntity is used for cloudProvider and termination Queue URL
                if (optionalClusterEntity.isPresent()) {
                    ClusterEntity clusterEntity = optionalClusterEntity.get();
                    ClusterProviderEnum clusterProviderEnum = clusterEntity.getClusterProvider();
                    CloudProvider cloudProvider = CloudProvider
                            .getCloudProviderFromClusterProvider(clusterProviderEnum);
                    telemetryEventClient.triggerEvent(List.of(
                            unhealthyInstanceServiceHelper.buildCloudOfflineInstanceTerminationMetric(
                                    entity, cloudProvider,
                                    ResourceProvider.BYOC,
                                    clusterEntity.getTerminationQueueUrl(),
                                    clusterEntity.getClusterName())));
                }
            } catch (Exception exception) {
                log.error("ByocUnhealthyInstanceService: Failed post-termination steps for instance {},"
                                  + " error: {}",
                          entity.getInstanceId(), exception.getMessage(), exception);
            }
        });
    }

    private void sendTelemetryForUnhealthyCloud(
            Map<String, ClusterEntity> clusterIdToUnhealthyClusterEntityMap) {
        List<GenericMetric> genericMetricList = clusterIdToUnhealthyClusterEntityMap.values()
                .stream()
                .map(ByocUnhealthyInstanceService::getMetricForCloudOffline)
                .toList();
        telemetryEventClient.triggerEvent(genericMetricList);
    }

    private boolean isClusterEnabledForInstanceTermination(@NotNull ClusterEntity clusterEntity) {
        String clusterId = clusterEntity.getClusterId();

        // We will give first preference to config driven enabling cluster for termination
        // This will help in incident to terminate instances from unhealthy clusters at run time without autoTerminationWaitTime
        boolean isClusterEnabledForTermination = configDrivenClusterEnablingForTermination(clusterId);
        if (isClusterEnabledForTermination) {
            log.info("ByocUnhealthyInstanceService: clusterId {} is enabled in config for terminating instances"
                             + " from unhealthy BYOC clusters", clusterId);
            return true;
        }

        // If cluster is not whitelisted in config, then checking for auto termination
        if (byocConfigurationProperties.isAutoTerminationOfInstancesFromUnhealthyCloudEnabled()) {
            return shouldTerminateBasedOnHeartbeat(clusterEntity);
        }

        // If auto termination not enabled then returning false
        return false;
    }

    // We will use configuration to check if instances can be terminated from unhealthy cluster
    private boolean configDrivenClusterEnablingForTermination(String clusterId) {
        Set<String> enabledClusterIds =
                byocConfigurationProperties.getTerminateCloudFailedInstancesFromClusters();
        // Checking for wildcard
        return enabledClusterIds.contains(WILDCARD) || enabledClusterIds.contains(clusterId);
    }

    private boolean shouldTerminateBasedOnHeartbeat(@NotNull ClusterEntity clusterEntity) {
        String clusterId = clusterEntity.getClusterId();
        Instant recentHealthyHeartbeatReportTime = clusterEntity.getHealthyHeartbeatReportTime();

        int autoTerminateInstanceTime =
                byocConfigurationProperties.getTimeForAutoTerminatingInstancesFromUnhealthyCloudInHours();
        Instant currentTime = Instant.now();

        // If recentHealthyHeartbeatReportTime is null it means cluster didn't report heartbeat since feature is enabled,
        // considering instances from cluster for termination
        if (recentHealthyHeartbeatReportTime == null) {
            log.warn("shouldTerminateBasedOnHeartbeat: Cluster with clusterId: {} didn't report heartbeat since"
                             + " auto termination feature is enabled, considering instances from this cluster for termination",
                     clusterId);
            return true;
        }

        // Check if the recent healthy heartbeat is older than or equal to autoTerminateInstanceTime(24) hours
        Duration timeSinceLastHealthyHeartbeat = Duration.between(recentHealthyHeartbeatReportTime, currentTime);
        if (timeSinceLastHealthyHeartbeat.toHours() >= autoTerminateInstanceTime) {
            log.warn("shouldTerminateBasedOnHeartbeat: Cluster with clusterId: {} last healthy heartbeat was"
                             + " {} minutes ago, considering instances from this cluster for termination",
                     clusterId, timeSinceLastHealthyHeartbeat.toMinutes());
            return true;
        }

        // If heartbeat is recent (less than 24 hours), don't terminate instances
        log.info("shouldTerminateBasedOnHeartbeat: Cluster with clusterId: {} has recent healthy heartbeat"
                         + " ({} minutes ago), not considering for termination",
                 clusterId, timeSinceLastHealthyHeartbeat.toMinutes());
        return false;
    }

    private Optional<ClusterEntity> getClusterInfoByClusterId(String clusterId) {
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, true);
        if (optionalClusterEntity.isEmpty()) {
            log.error("ByocUnhealthyInstanceService: Could not find cluster info for {} cluster id", clusterId);
        }
        return optionalClusterEntity;
    }

}
