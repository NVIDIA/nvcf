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
package com.nvidia.icms.service.scheduled.instance;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.activeInstanceStateList;
import static com.nvidia.icms.service.InstanceServiceHelper.isReservedBackupInstance;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.extensions.api.ReservedBackupInstanceProcessor;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import jakarta.validation.constraints.NotNull;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class ActiveInstanceMonitoringTaskService {

    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final ReservedBackupInstanceProcessor reservedBackupInstanceProcessor;
    private final InstanceV2Repository instanceV2Repository;
    private final ProcessUnhealthyInstance processUnhealthyInstance;
    private final InstanceServiceHelper instanceServiceHelper;
    private final TelemetryEventClient telemetryEventClient;
    private final CloudHealthRepository cloudHealthRepository;

    @Observed
    public void execute() {

        try {

            // 1. Validate least one instance monitoring service enabled
            if (!icmsConfigurationProperties.isCloudFailureDetectionEnabled() &&
                    !reservedBackupInstanceProcessor.isBackupToPrimaryZoneMigrationEnabled()) {
                log.debug("All instance monitoring service disabled");
                return;
            }

            processingInstancesV2();
        } catch (Exception e) {
            log.error("Exception occurred while executing active instance monitoring task error: {}, exception: ",
                    e.getMessage(), e);
            sendTaskFailureException(e.getMessage());

            throw e;
        }
    }


    private void processingInstancesV2() {

        Set<String> skipHealthCheckStatusClusterIds =
                instanceServiceHelper.getClusterIdsOfClusterToSkipHealthCheck();
        List<InstanceV2Entity> unhealthyInstances = new ArrayList<>();
        List<InstanceV2Entity> healthyReservedBackupInstances = new ArrayList<>();
        Map<String, CloudHealthEntity> clusterIdToCloudHealthMap = cloudHealthRepository.findAllInMap();

        instanceV2Repository.findAllInstancesAndApplyAction(
                r -> filterInstancesBasedOnHealth(r,
                                                  unhealthyInstances, healthyReservedBackupInstances, clusterIdToCloudHealthMap),
                                                icmsConfigurationProperties.getCloudFailureDetectionTaskPauseBetweenDaysInMilliseconds());

        // Processing unhealthy instances
        processUnhealthyInstances(unhealthyInstances, skipHealthCheckStatusClusterIds);

        // Processing RESERVED_BACKUP instances
        processHealthyReservedBackupInstances(healthyReservedBackupInstances);
    }

    private void processUnhealthyInstances(
            @NotNull List<InstanceV2Entity> unhealthyInstances,
            @NotNull Set<String> skipHealthCheckStatusClusterIds) {
        if (!unhealthyInstances.isEmpty() && icmsConfigurationProperties.isCloudFailureDetectionEnabled()) {
            try {
                processUnhealthyInstance.execute(unhealthyInstances, skipHealthCheckStatusClusterIds);
            } catch (Exception exception) {

                // Suppressing the error to execute other instance monitoring task
                log.error("ActiveInstanceMonitoringTaskService: failed to process instance health monitoring task, error: {}, exception: ",
                        exception.getMessage(), exception);
            }
        }
    }

    private void processHealthyReservedBackupInstances(
            @NotNull List<InstanceV2Entity> healthyReservedBackupInstances) {
        if (!healthyReservedBackupInstances.isEmpty()
                && reservedBackupInstanceProcessor.isBackupToPrimaryZoneMigrationEnabled()) {
            try {
                reservedBackupInstanceProcessor.execute(healthyReservedBackupInstances);

            } catch (Exception exception) {

                // Suppressing the error to execute other instance monitoring task
                log.error("ActiveInstanceMonitoringTaskService: failed to process instance reserved backup instance monitoring task, error: {}, exception: ",
                        exception.getMessage(), exception);
            }
        }
    }

    private void filterInstancesBasedOnHealth(
            @NotNull InstanceV2Entity instanceV2Entity,
            @NotNull List<InstanceV2Entity> unhealthyInstances,
            @NotNull List<InstanceV2Entity> healthyReservedBackupInstances,
            @NotNull Map<String, CloudHealthEntity> clusterIdToCloudHealthMap) {

        if (activeInstanceStateList.contains(instanceV2Entity.getInstanceStateName())) {

            // Filtering unhealthy instances for processing unhealthy instances
            if (!isInstanceHealthy(clusterIdToCloudHealthMap, instanceV2Entity.getZone())) {
                unhealthyInstances.add(instanceV2Entity);

            // Filtering healthy RESERVED_BACKUP instances for instanceExpiration validation
            } else if (isReservedBackupInstance(instanceV2Entity)) {
                healthyReservedBackupInstances.add(instanceV2Entity);
            }
        }
    }

    /**
     * Instance will be considered healthy if it's cluster is healthy
     * We will validate cluster's health to identify the instance's heath
     * <p>
     * no cloud health status in DB = UNHEALTHY
     * UNHEALTHY cloud health status in DB = UNHEALTHY
     * HEALTHY cloud health status in DB = HEALTHY
     * <p>
     * If the cloud health status is reported as HEALTHY then it will be considered as healthy for 15 mins (cloud-health-ttl-in-sec)
     * If the cloud health status is reported as UNHEALTHY then it will be immediately considered as unhealthy
     */
    private boolean isInstanceHealthy(Map<String, CloudHealthEntity> clusterIdToCloudHealthMap, String clusterId) {

        // Checking if cloud is healthy
        CloudHealthEntity cloudHealthEntity = clusterIdToCloudHealthMap.getOrDefault(clusterId, null);

        // Health entity not found in DB, considering zone as UNHEALTHY
        if (cloudHealthEntity == null) {
            return false;
        }

        return CloudHealthRepository.isCloudHealthy(cloudHealthEntity);
    }

    private void sendTaskFailureException(String errMsg) {
        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withError(errMsg)
                                                           .withEventName(
                                                                   Events.ACTIVE_INSTANCE_MONITORING_TASK_FAILED.toString())));
    }
}
