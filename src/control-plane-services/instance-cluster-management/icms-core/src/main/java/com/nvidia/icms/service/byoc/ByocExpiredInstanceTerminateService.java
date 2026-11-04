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

import static com.nvidia.icms.service.scheduled.instance.ExpiredInstanceServiceHelper.INSTANCE_LIFETIME_EXPIRED_ERROR_LOG;

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.scheduled.instance.ExpiredInstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.CopyUtil;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

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
public class ByocExpiredInstanceTerminateService {

    private final ByocTerminateService byocTerminateService;
    private final InstanceV2Repository instanceV2Repository;
    private final TelemetryEventClient telemetryEventClient;
    private final InstanceServiceHelper instanceServiceHelper;
    private final ExpiredInstanceServiceHelper expiredInstanceServiceHelper;

    public void persistAndTerminate(@NotNull List<InstanceV2Entity> expiredInstances) {
        if (expiredInstances.isEmpty()) {
            return;
        }
        log.debug("Updating expired BYOC instances state in db and send SQS termination message");

        Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap = new HashMap<>();
        Map<String, Set<InstanceV2Entity>> requestIdToInstanceIdsMap = new HashMap<>();
        Map<String, InstanceV2Entity> entityBefore = new HashMap<>();
        List<InstanceV2Entity> entityAfter = new ArrayList<>();

        for (InstanceV2Entity entity : expiredInstances) {
            entityBefore.put(entity.getInstanceId(), CopyUtil.deepCopy(entity));

            Optional<ClusterEntity> clusterEntity =
                    resolveClusterEntity(entity, requestIdToClusterEntitiesMap);
            if (clusterEntity.isEmpty()) {
                continue;
            }

            requestIdToClusterEntitiesMap
                    .computeIfAbsent(entity.getRequestId(), key -> new HashSet<>())
                    .add(clusterEntity.get());

            requestIdToInstanceIdsMap
                    .computeIfAbsent(entity.getRequestId(), key -> new HashSet<>())
                    .add(entity);

            entityAfter.add(byocTerminateService.updateInstanceEntityState(
                    entity, INSTANCE_LIFETIME_EXPIRED_ERROR_LOG));
        }

        if (requestIdToClusterEntitiesMap.isEmpty()) {
            return;
        }

        log.debug("Updating lifetime expired BYOC instances state in db and send termination message");

        // 1. Publishing message for instance termination
        // If SQS publish fails, abort early — no DB state has been modified yet
        try {
            byocTerminateService.sendSqsMessageForInstanceTermination(
                    requestIdToClusterEntitiesMap, requestIdToInstanceIdsMap);
        } catch (Exception exception) {
            log.error("ByocExpiredInstanceTerminateService: Failed to send SQS termination message"
                              + " for BYOC instances, error: {}",
                      exception.getMessage(), exception);
            return;
        }

        // 2. Updating instance entity
        // 3. Sending audit and telemetry data
        // Each entity is processed independently so one failure does not block the remaining instances
        List<String> failedInstanceIds = new ArrayList<>();
        for (InstanceV2Entity entity : entityAfter) {
            try {
                instanceV2Repository.update(entity);
                expiredInstanceServiceHelper.sendAuditForExpiredInstance(
                        entity, entityBefore.get(entity.getInstanceId()));

                instanceServiceHelper.gpuUsageEventForTerminatedInstance(entity);
                instanceServiceHelper.sendLatestInstanceStateEvent(entity);

                requestIdToClusterEntitiesMap.get(entity.getRequestId()).stream()
                        .filter(cluster -> cluster.getClusterId().equals(entity.getZone()))
                        .findFirst()
                        .ifPresent(cluster -> {
                            ClusterProviderEnum clusterProviderEnum = cluster.getClusterProvider();
                            CloudProvider cloudProvider = CloudProvider
                                    .getCloudProviderFromClusterProvider(clusterProviderEnum);
                            telemetryEventClient.triggerEvent(List.of(
                                    expiredInstanceServiceHelper.buildExpiredInstanceTerminationMetric(
                                            entity, cloudProvider, ResourceProvider.BYOC,
                                            cluster.getTerminationQueueUrl())));
                        });
            } catch (Exception e) {
                log.error("ByocExpiredInstanceTerminateService: Failed post-termination processing"
                                  + " for instance {}, error: {}",
                          entity.getInstanceId(), e.getMessage(), e);
                failedInstanceIds.add(entity.getInstanceId());
            }
        }

        if (!failedInstanceIds.isEmpty()) {
            log.warn("ByocExpiredInstanceTerminateService: Post-termination steps incomplete"
                             + " for {} instance(s): {}",
                     failedInstanceIds.size(), failedInstanceIds);
        }
    }

    /**
     * Returns the ClusterEntity for the given instance's zone, using the already-populated map as a
     * cache to avoid redundant lookups across instances sharing the same request and zone.
     * If the cluster is not found (unregistered), the instance is terminated in place and
     * Optional.empty() is returned so the caller can skip it.
     */
    private Optional<ClusterEntity> resolveClusterEntity(
            @NotNull InstanceV2Entity entity,
            @NotNull Map<String, Set<ClusterEntity>> requestIdToClusterEntitiesMap) {

        // Check the cache first
        Set<ClusterEntity> cachedClusters = requestIdToClusterEntitiesMap.get(entity.getRequestId());
        if (cachedClusters != null) {
            Optional<ClusterEntity> cached = cachedClusters.stream()
                    .filter(c -> c.getClusterId().equals(entity.getZone()))
                    .findFirst();
            if (cached.isPresent()) {
                return cached;
            }
        }

        // Not cached — fetch from the repository
        Optional<ClusterEntity> fetched =
                byocTerminateService.getClusterEntityFromClusterId(entity.getZone());
        if (fetched.isEmpty()) {
            // Cluster was unregistered — terminate this instance immediately
            InstanceV2Entity instanceEntityBefore = CopyUtil.deepCopy(entity);
            byocTerminateService.handleInstancesWithMissingClusterInfo(entity);
            expiredInstanceServiceHelper.sendAuditForExpiredInstance(entity, instanceEntityBefore);
            instanceServiceHelper.gpuUsageEventForTerminatedInstance(entity);
            // cloudProvider and terminationQueueUrl are unavailable (no cluster registered)
            telemetryEventClient.triggerEvent(List.of(
                    expiredInstanceServiceHelper.buildExpiredInstanceTerminationMetric(
                            entity, null, ResourceProvider.BYOC, null)));
            instanceServiceHelper.sendLatestInstanceStateEvent(entity);
            return Optional.empty();
        }
        return fetched;
    }
}
