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
package com.nvidia.icms.service.byoc.nvca.clustermanagement;

import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;

import org.springframework.stereotype.Service;

import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterConfigurationRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterByGroupIdAndIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterGroupByGroupIdEntity;
import com.nvidia.icms.outbound.cassandra.byoc.entity.CreationQueueUdt;
import com.nvidia.icms.outbound.cassandra.byoc.entity.GpuV5Udt;
import com.nvidia.icms.outbound.sqs.QueueManager;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService;
import static com.nvidia.icms.service.byoc.nvca.NvcaClusterRegistrationService.isClusterTargetingEnabled;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getMetadataForCluster;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import static com.nvidia.icms.util.InstanceServiceUtil.isSetEmptyOrNull;
import static com.nvidia.icms.util.audit.AuditUtils.populateAuditValuesForDeletingCluster;

import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import com.nvidia.icms.service.workers.WorkerIdentifierService;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@Service
@Slf4j
@AllArgsConstructor
public class ClusterTerminateService {

    private final NvcaClusterRepository nvcaClusterRepository;

    private final ClusterRepository clusterRepository;

    private final QueueManager queueManager;

    private final InstanceServiceHelper instanceServiceHelper;

    private final AppAuditService auditService;

    private final TelemetryEventClient telemetryEventClient;

    private final NvcaClusterRegistrationService clusterRegistrationService;

    private final NvcaClusterConfigurationRepository nvcaClusterConfigurationRepository;

    private final WorkerIdentifierService workerIdentifierService;

    @Observed
    public void deleteCluster(String ncaId, String clusterId, Map<String, Object> auditProps) {
        // Fetch cluster ID details
        // Setting checkForHashedClusterId: false because NGC UI will only pass valid clusterId
        // We are not doing validation with auth token
        Optional<ClusterEntity> optionalClusterEntity =
                clusterRepository.getClusterInfoByClusterId(clusterId, false);

        if (optionalClusterEntity.isEmpty()) {
            log.error("Could not find any cluster registered with id {}", clusterId);
            throw new IcmsNotFoundException(
                    "Could not find any cluster registered with id " + clusterId);
        }

        // Validate if cluster ID is associated with ncaId provided or not
        ClusterEntity clusterEntity = optionalClusterEntity.get();
        validateNcaIdToDelete(clusterEntity.getNcaId(), ncaId);

        // Validate if cluster is registered with Nvca 1.0 flow
        // If cluster was registered with old flow then it should be deleted with NVCA 1.0 flow
        validateClusterForNvca2Flow(clusterEntity);

        // Check if active instances are present for cluster ID
        validateForActiveInstances(clusterId);

        try {
            // Delete entry from cluster
            deleteClusterOperations(clusterEntity, clusterId, auditProps);

        } catch (IcmsInternalServerException icmsInternalServerException) {
            log.error("Failed to delete cluster info, cluster name - {}, internalServerError: error - {}",
                    optionalClusterEntity.get().getClusterName(),
                    icmsInternalServerException.getBody().getDetail());

            // rethrowing caught exception
            throw icmsInternalServerException;

        } catch (Exception e) {
            log.error("Failed to delete cluster info, cluster name - {}, error - {}",
                      optionalClusterEntity.get().getClusterName(), e.getMessage(), e);
            throw new IcmsInternalServerException(String.format("Failed to un-register cluster, error: %s", e.getMessage()));
        }
    }

    public void deleteClusterOperations(
            ClusterEntity clusterEntity, String clusterId,
            Map<String, Object> auditProps) {

        // Delete cluster configuration entry
        nvcaClusterConfigurationRepository.deleteByClusterId(clusterId);

        // Delete entry from cluster
        nvcaClusterRepository.deleteClusterInfo(clusterEntity);

        // A deregistered cluster can no longer authorize any worker.
        workerIdentifierService.deleteForClusterQuietly(clusterId);

        // Audit log changes in DB
        populateAuditValuesForDeletingCluster(auditProps, clusterId);
        auditService.sendAuditEventForClusterEntity(auditProps, clusterEntity,
                                                    new ClusterEntity());

        // send telemetry
        sendTelemetryEvent(clusterEntity);

        // If SIS has nats enabled then no need to terminate queues
        if (!instanceServiceHelper.isNatsEnabled()) {

            // Check if termination queue is present then delete termination queue
            // It is possible the delete request is made before NVCA cluster got Ready
            if (clusterEntity.getTerminationQueueUrl() != null &&
                    !clusterEntity.getTerminationQueueUrl().isEmpty()) {
                queueManager.deleteQueue(clusterEntity.getTerminationQueueUrl());
            }

            // Delete all creation queues
            deleteAllClusterCreationQueues(clusterEntity);
        }
    }

    public void deleteClusterForReconfiguration(
            ClusterEntity clusterEntity, String clusterId, List<String> activeInstances,
            Map<String, Object> auditProps) {

        // GET all clusters in cluster group
        List<ClusterByGroupIdAndIdEntity> clusterByGroupIdList = clusterRepository
                .getClustersFromClusterGroup(clusterEntity.getClusterGroupId());

        // If single cluster in cluster group then we want to clear all creation queues before we delete cluster group
        // For this we need to validate if active instances are present
        if (clusterByGroupIdList.size() == 1) {
            // Validate if active instances are present before deleting
            if (!activeInstances.isEmpty()) {
                String errMsg =
                        String.format(
                                "Cluster reconfiguration failed, terminate following active instances %s",
                                activeInstances);
                log.error(
                        "Active instances exists for cluster-id {} which is the only cluster-id in cluster group "
                                + "{} hence avoiding cluster reconfiguration {}",
                        clusterId, clusterEntity.getClusterId(), activeInstances);
                throw new IcmsConflictException(errMsg);
            }
        }

        // Delete cluster configuration entry
        nvcaClusterConfigurationRepository.deleteByClusterId(clusterId);

        // Delete entry from cluster
        nvcaClusterRepository.deleteClusterInfo(clusterEntity);

        // A deregistered cluster can no longer authorize any worker.
        workerIdentifierService.deleteForClusterQuietly(clusterId);

        // Audit log changes in DB
        populateAuditValuesForDeletingCluster(auditProps, clusterId);
        auditService.sendAuditEventForClusterEntity(auditProps, clusterEntity,
                                                    new ClusterEntity());

        // send telemetry
        sendTelemetryEvent(clusterEntity);

        if (!instanceServiceHelper.isNatsEnabled()) {
            deleteAllClusterCreationQueues(clusterEntity);
        }
    }

    public void validateNcaIdToDelete(String expected, String actual) {
        if (!Objects.equals(expected, actual)) {
            String errorMsg = String.format(
                    "The provided cluster ID is registered with different ncaId. Specified ncaId %s, "
                            + "ncaId associated for cluster %s", actual, expected);
            log.error(errorMsg);
            throw new IcmsConflictException(errorMsg);
        }
    }

    private void deleteAllClusterCreationQueues(@NotNull ClusterEntity clusterEntity) {
        // Check if cluster group is present
        Optional<ClusterGroupByGroupIdEntity> optionalClusterGroupByGroupIdEntity =
                clusterRepository.getClusterGroupInfoByClusterGroupId(
                        clusterEntity.getClusterGroupId());

        // Cas1: Cluster group is deleted then delete all queues
        if (optionalClusterGroupByGroupIdEntity.isEmpty() &&
                clusterEntity.getCreationQueues() != null) {
            // If cluster group has been deleted then creation queues can be deleted
            for (CreationQueueUdt creationQueue : clusterEntity.getCreationQueues().values()) {
                queueManager.deleteQueue(creationQueue.getUrl());
                log.info(
                        "For {} clusterId from {} clusterGroup deleting {} queue for inactive GPU",
                        clusterEntity.getClusterId(), clusterEntity.getClusterGroupId(),
                        creationQueue.getUrl());
            }
            if (isClusterTargetingEnabled(clusterEntity.getAllowClusterTargeting())) {
                for (CreationQueueUdt creationQueue : clusterEntity.getClusterCreationQueues()
                        .values()) {
                    queueManager.deleteQueue(creationQueue.getUrl());
                    log.info(
                            "For {} clusterId from {} clusterGroup deleting {} cluster queue for inactive GPU",
                            clusterEntity.getClusterId(), clusterEntity.getClusterGroupId(),
                            creationQueue.getUrl());
                }

                // Delete cluster creation queues for tasks
                deleteClusterCreationQueuesForTasks(clusterEntity);
            }

        } else if (!isSetEmptyOrNull(NvcaConverter.getGpusV5(clusterEntity))) {
            // Case 2: Cluster group is not yet deleted
            // Check if there is any non-shared GPU in this cluster among all clusters
            // Fetch allCluster info when Gpus are not empty and find non-shared gpus
            deleteNonSharedCreationQueue(clusterEntity);
        }
    }

    private void deleteClusterCreationQueuesForTasks(ClusterEntity clusterEntity) {
        if (instanceServiceHelper.isTaskClusterCreationQueuesAllowed(clusterEntity.getAllowTaskClusterCreationQueues())
                && clusterEntity.getClusterCreationQueueForTasks() != null) {
            for (CreationQueueUdt creationQueue : clusterEntity.getClusterCreationQueueForTasks().values()) {
                queueManager.deleteQueue(creationQueue.getUrl());
                log.info("For {} clusterId from {} clusterGroup deleting {} tasks cluster queue for inactive GPU",
                         clusterEntity.getClusterId(), clusterEntity.getClusterGroupId(),
                         creationQueue.getUrl());
            }
        }
    }

    private void deleteNonSharedCreationQueue(ClusterEntity clusterEntity) {
        List<ClusterByGroupIdAndIdEntity> clusterByGroupIdList =
                clusterRepository.getClustersFromClusterGroup(clusterEntity.getClusterGroupId());

        // Find non-shared GPUs set
        Set<GpuV5Udt> nonSharedGpus = getNonSharedGpus(clusterByGroupIdList,
                                                    NvcaConverter.getGpusV5(clusterEntity));

        // Delete creation queues for non-shared GPUs set
        for (GpuV5Udt gpuV5 : nonSharedGpus) {
            Map<String, CreationQueueUdt> gpuToCreationQueueMap =
                    clusterEntity.getCreationQueues() == null ? new HashMap<>() :
                            clusterEntity.getCreationQueues();

            if (gpuToCreationQueueMap.containsKey(gpuV5.getName())) {
                var creationQueue = gpuToCreationQueueMap.get(gpuV5.getName());
                queueManager.deleteQueue(creationQueue.getUrl());
                log.info(
                        "For {} clusterId from {} clusterGroup deleting {} queue for {} inactive GPU",
                        clusterEntity.getClusterId(), clusterEntity.getClusterGroupId(),
                        creationQueue.getUrl(), gpuV5.getName());
            }

            Map<String, CreationQueueUdt> gpuToClusterCreationQueueMap =
                    clusterEntity.getClusterCreationQueues() == null ? new HashMap<>() :
                            clusterEntity.getClusterCreationQueues();
            if (isClusterTargetingEnabled(clusterEntity.getAllowClusterTargeting())) {
                if (gpuToClusterCreationQueueMap.containsKey(gpuV5.getName())) {
                    var clusterCreationQueue = gpuToClusterCreationQueueMap.get(gpuV5.getName());
                    queueManager.deleteQueue(clusterCreationQueue.getUrl());
                    log.info(
                            "For {} clusterId from {} clusterGroup deleting {} cluster queue for {} inactive GPU",
                            clusterEntity.getClusterId(), clusterEntity.getClusterGroupId(),
                            clusterCreationQueue.getUrl(), gpuV5.getName());
                }

                // Delete non shared task specific creation queue
                deleteNonSharedTaskCreationQueue(clusterEntity, gpuV5);
            }
        }
    }

    private void deleteNonSharedTaskCreationQueue(ClusterEntity clusterEntity, GpuV5Udt gpuV5) {

        Map<String, CreationQueueUdt> gpuToTaskClusterCreationQueueMap =
                clusterEntity.getClusterCreationQueuesForTasks() == null ? new HashMap<>() :
                        clusterEntity.getClusterCreationQueuesForTasks();
        if (instanceServiceHelper.isTaskClusterCreationQueuesAllowed(clusterEntity.getAllowTaskClusterCreationQueues())
                && gpuToTaskClusterCreationQueueMap.containsKey(gpuV5.getName())) {

            var taskClusterCreationQueue = gpuToTaskClusterCreationQueueMap.get(gpuV5.getName());
            queueManager.deleteQueue(taskClusterCreationQueue.getUrl());
            log.info(
                    "For {} clusterId from {} clusterGroup deleting {} tasks cluster queue for {} inactive GPU",
                    clusterEntity.getClusterId(), clusterEntity.getClusterGroupId(),
                    taskClusterCreationQueue.getUrl(), gpuV5.getName());
        }
    }

    private void validateForActiveInstances(String clusterId) {
        List<String> activeInstanceIds = instanceServiceHelper.getActiveInstancesFromZone(clusterId);
        if (!activeInstanceIds.isEmpty()) {
            String errMsg =
                    String.format(
                            "Cluster un-registration failed, terminate following active instances %s",
                            activeInstanceIds);
            log.error(
                    "Active instances exists for cluster-id {} hence avoiding cluster un-registration {}",
                    clusterId, activeInstanceIds);
            throw new IcmsConflictException(errMsg);
        }
    }

    private Set<GpuV5Udt> getNonSharedGpus(
            List<ClusterByGroupIdAndIdEntity> clusterByGroupIdList,
            Set<GpuV5Udt> clusterGpus) {

        var copyGpuSet = new HashSet<>(clusterGpus);
        for (ClusterByGroupIdAndIdEntity clusterInGroup : clusterByGroupIdList) {
            // Remove shared GPUs from copy set
            copyGpuSet.removeAll(NvcaConverter.getGpusV5(clusterInGroup));
        }
        // Now copy set contains only GPUs which are not shared among clusters
        return copyGpuSet;
    }

    private void validateClusterForNvca2Flow(ClusterEntity clusterEntity) {

        if (clusterEntity.getNvcaVersion() == null) {
            String errorMsg = String.format(
                    "This cluster %s can not be deleted since it was registered with NVCA 1.0 flow",
                    clusterEntity.getClusterId());
            log.error(errorMsg);
            throw new PreConditionFailedException(errorMsg);
        }
    }

    private void sendTelemetryEvent(ClusterEntity clusterEntity) {
        try {
            Map<String, Object> metaData = getMetadataForCluster(clusterEntity,
                                                                 "nvcaClusterTerminated");
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withMetadata(metaData)
                                                               .withClusterId(clusterEntity.getClusterId())
                                                               .withClusterName(clusterEntity.getClusterName())
                                                               .withEventName(
                                                                       Events.NVCA_CLUSTER_TERMINATED.toString())));
        } catch (Exception e) {
            // Do not throw exceptions for telemetry failures
            log.warn(
                    "Error sending telemetry for the termination of cluster {}, with cluster group {}",
                    clusterEntity.getClusterName(), clusterEntity.getClusterGroupName());
        }
    }

}
