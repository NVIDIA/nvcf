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
package com.nvidia.icms.service.scheduled.request;

import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_GROUP_ID;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_GROUP_NAME;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_PROVIDER;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.CLUSTER_STATUS;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForClusterHealthMonitorTask;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterHealthEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class ClusterHealthMonitorTask {

    QueueManager queueManager;

    ClusterRepository clusterRepository;

    CloudHealthRepository cloudHealthRepository;

    ByocConfigurationProperties byocConfigurationProperties;

    AppAuditService auditService;

    TelemetryEventClient telemetryEventClient;

    InstanceServiceHelper instanceServiceHelper;

    ComputePlatformService computePlatformService;

    @Data
    @Builder
    private static class ClusterGroupStatus {

        boolean active;
        String creationQueueUrl;
    }

    /**
     * 1. Update cluster health from cloud health <p>
     * 2. Monitor cluster health <p>
     * 3. Delete queues of Abandoned cluster <p>
     */
    public void monitorClusterHealth() {

        // Updating cluster health
        updateClusterHealth();

        Set<String> skipHealthCheckStatus = instanceServiceHelper.getClusterIdsOfClusterToSkipHealthCheck();

        // Monitoring cluster health
        int filterClusterOlderThanDays = byocConfigurationProperties.getClusterExpiryTimeInDays();
        List<ClusterEntity> allClusterInfo = clusterRepository.getAllClusters();
        Set<ClusterEntity> clusterEntitySet = allClusterInfo.stream()
                .filter(clusterEntity -> !clusterEntity.getClusterStatus()
                        .equals(ClusterStatusEnum.ABANDONED)).collect(Collectors.toSet());

        Set<ClusterEntity> clustersToBeAbandoned = new HashSet<>();
        Set<String> terminationQueuesToBeDeleted = new HashSet<>();
        Map<String, ClusterGroupStatus> clusterGroupStatusMap = new HashMap<>();
        for (ClusterEntity entity : clusterEntitySet) {

            if (!skipHealthCheckStatus.contains(entity.getClusterId()) &&
                    isClusterValidForMarkingAbandoned(entity.getRegistrationTime(),
                                                      filterClusterOlderThanDays)) {

                Optional<ClusterHealthEntity> optionalClusterHealthById =
                        clusterRepository.getClusterHealthById(entity.getClusterId());

                if (optionalClusterHealthById.isPresent()) {

                    // Overriding cluster group status to active
                    // The cluster is active hence, the cluster group is active and creation queue is in use
                    markClusterGroupAsActive(entity, clusterGroupStatusMap);

                } else {

                    // cluster health not present, consider this cluster as Abandoned
                    markClusterAbandonedAndClusterGroupInactive(entity, clustersToBeAbandoned,
                                                                terminationQueuesToBeDeleted,
                                                                clusterGroupStatusMap);
                }

            } else {
                // cluster is new, marking cluster group active
                markClusterGroupAsActive(entity, clusterGroupStatusMap);
            }
        }

        // Deleting termination-queue of abandoned cluster
        for (String queueUrl : terminationQueuesToBeDeleted) {
            log.info("Deleting termination queue with URL '{}' because the cluster is marked" +
                             " as '{}'", queueUrl, ClusterStatusEnum.ABANDONED);
            queueManager.deleteQueue(queueUrl);
        }

        // Deleting creation-queue of inactive cluster groups
        Set<ClusterGroupStatus> clusterGroupStatuses =
                new HashSet<>(clusterGroupStatusMap.values());
        for (ClusterGroupStatus clusterGroupStatus : clusterGroupStatuses) {
            if (!clusterGroupStatus.isActive()) {
                log.info("Deleting creation queue with URL '{}' because the cluster is marked" +
                                 " as '{}'", clusterGroupStatus.getCreationQueueUrl(),
                         ClusterStatusEnum.ABANDONED);
                queueManager.deleteQueue(clusterGroupStatus.getCreationQueueUrl());
            }
        }

        // Marking cluster as Abandoned
        for (ClusterEntity entity : clustersToBeAbandoned) {

            // Terminate cluster specific queues of NVCA 2.0 clusters
            terminateClusterSpecificCreationQueues(entity);

            ClusterEntity entityBefore = CopyUtil.deepCopy(entity);
            entity.setClusterStatus(ClusterStatusEnum.ABANDONED);

            // Updating in DB
            clusterRepository.updateClusterInfo(entity, new HashSet<>(), false);

            // Sending audit logs
            Map<String, Object> auditProps = new HashMap<>();
            populateAuditValuesForClusterHealthMonitorTask(auditProps, entity.getClusterId());
            auditService.sendAuditEventForClusterEntity(auditProps, entityBefore, entity);

            // Sending telemetry event
            sendTelemetryEventForAbandoningCluster(entity);

            log.info("Updated '{}' cluster status to '{}'", entity.getClusterName(),
                     ClusterStatusEnum.ABANDONED);
        }
    }

    private void terminateClusterSpecificCreationQueues(ClusterEntity entity) {
        // For NVCA 2. 0 clusters
        if (entity.getNvcaVersion() != null) {
            // Terminate group creation queues
            if (entity.getCreationQueues() != null) {
                for (CreationQueueUdt creationQueue : entity.getCreationQueues().values()) {
                    queueManager.deleteQueue(creationQueue.getUrl());
                }
            }
            // Terminate cluster specific queues
            if (entity.getClusterCreationQueues() != null) {
                for (CreationQueueUdt clusterCreationQueue : entity.getClusterCreationQueues().values()) {
                    queueManager.deleteQueue(clusterCreationQueue.getUrl());
                }
            }
        }
    }

    private void updateClusterHealth() {

        int filterClusterOlderThanDays = byocConfigurationProperties.getClusterExpiryTimeInDays();
        int ttl = getSecFromDays(filterClusterOlderThanDays);
        var cloudHealthEntities = cloudHealthRepository.findAll().stream()
                .filter(cloudHealthEntity -> cloudHealthEntity.getKey().getCloudProvider()
                        .equals(ResourceProvider.BYOC)).collect(Collectors.toSet());

        for (CloudHealthEntity entity : cloudHealthEntities) {
            clusterRepository.saveClusterHealth(
                    ClusterHealthEntity.builder().clusterId(entity.getKey().getZone())
                            .healthUpdatedTs(Instant.now()).build(), ttl);
        }
    }

    private void markClusterAbandonedAndClusterGroupInactive(
            ClusterEntity entity,
            Set<ClusterEntity> clustersToBeAbandoned,
            Set<String> terminationQueuesToBeDeleted,
            Map<String, ClusterGroupStatus> clusterGroupStatusMap) {
        if (computePlatformService.isPlatformCluster(entity.getClusterGroupName())) {
            // First-party platform clusters manage their own queues, so skip queue deletion here
            return;
        }
        clustersToBeAbandoned.add(entity);
        terminationQueuesToBeDeleted.add(entity.getTerminationQueueUrl());

        // If cluster is of BART cluster then only use clusterGroupStatusMap for creation queue
        // Not overriding previous cluster group status
        if (entity.getNvcaVersion() == null &&
                !clusterGroupStatusMap.containsKey(entity.getClusterGroupId())) {
            clusterGroupStatusMap.put(entity.getClusterGroupId(),
                                      ClusterGroupStatus.builder()
                                              .creationQueueUrl(entity.getCreationQueueUrl())
                                              .active(false).build());
        }
    }

    private void markClusterGroupAsActive(
            ClusterEntity entity,
            Map<String, ClusterGroupStatus> clusterGroupStatusMap) {

        // Overriding previous cluster group status to "active"
        clusterGroupStatusMap.put(entity.getClusterGroupId(),
                                  ClusterGroupStatus.builder()
                                          .creationQueueUrl(entity.getCreationQueueUrl())
                                          .active(true).build());
    }

    private boolean isClusterValidForMarkingAbandoned(Instant instant, int olderThanDays) {
        Instant currentInstant = Instant.now();
        Duration duration = Duration.between(instant, currentInstant);
        long days = duration.toDays();

        return days > olderThanDays;
    }

    private int getSecFromDays(int days) {
        return days * 24 * 60 * 60;
    }

    private void sendTelemetryEventForAbandoningCluster(ClusterEntity clusterEntity) {

        Map<String, Object> metaData = new HashMap<>();
        metaData.put(CLUSTER_GROUP_ID.getName(), clusterEntity.getClusterGroupId());
        metaData.put(CLUSTER_GROUP_NAME.getName(), clusterEntity.getClusterGroupName());
        metaData.put(CLUSTER_PROVIDER.getName(), clusterEntity.getClusterProvider());
        metaData.put(CLUSTER_STATUS.getName(), clusterEntity.getClusterStatus());

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withMetadata(metaData)
                                                           .withClusterId(clusterEntity.getClusterId())
                                                           .withClusterName(clusterEntity.getClusterName())
                                                           .withEventName(
                                                                   Events.BYOC_CLUSTER_ABANDONED.toString())));
    }
}
