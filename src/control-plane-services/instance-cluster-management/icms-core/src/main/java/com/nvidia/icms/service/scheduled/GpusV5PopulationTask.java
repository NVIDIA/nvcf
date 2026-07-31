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
package com.nvidia.icms.service.scheduled;

import static com.nvidia.icms.configuration.SchedulingConfiguration.SCHEDULED_JOBS_PROFILES;
import static com.nvidia.icms.service.byoc.nvca.clustermanagement.ClusterCreationService.getMetadataForCluster;
import static com.nvidia.icms.service.telemetry.model.Events.GPUS_V5_POPULATION_EVENT;

import com.google.common.annotations.VisibleForTesting;
import com.google.common.base.Stopwatch;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaClusterRepository;
import com.nvidia.icms.outbound.cassandra.byoc.NvcaConverter;
import com.nvidia.icms.outbound.cassandra.byoc.entity.ClusterEntity;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.audit.AuditUtils;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.context.annotation.Profile;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@Profile(SCHEDULED_JOBS_PROFILES)
@AllArgsConstructor
public class GpusV5PopulationTask {
    public static final String GPUS_V5_POPULATION_TASK_NAME = "GpusV5PopulationTask";

    private final ClusterRepository clusterRepository;
    private final NvcaClusterRepository nvcaClusterRepository;
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final AppAuditService auditService;
    private final TelemetryEventClient telemetryEventClient;


    @Scheduled(initialDelayString = "${icms.async-hourly-task-schedule-initial-delay}",
            fixedDelayString = "${icms.async-hourly-task-schedule-duration}")
    public void execute() {

        // Existing if task is not enabled
        if (!icmsConfigurationProperties.isGpuV5PopulationTaskEnabled()) {
            return;
        }

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;
        try {
            if (!lockProviderService.obtainLockWithTtl(
                    GPUS_V5_POPULATION_TASK_NAME,
                    icmsConfigurationProperties.getGpusV5PopulationTaskLockTtlInSeconds())) {
                return;
            }

            log.info("Begin task: " + GPUS_V5_POPULATION_TASK_NAME);
            stopwatch.start();
            updateClusters();
            log.info("End task: " + GPUS_V5_POPULATION_TASK_NAME);
        } catch (Exception exception) {
            log.error("{} job failed with error: {} exception: ",
                      GPUS_V5_POPULATION_TASK_NAME,
                      exception.getMessage(),
                      exception);
            capturedError = exception.getMessage();
        }

        // Sending event per job execution
        GenericMetric genericMetric = new GenericMetric()
                .withError(capturedError)
                .withMetadata(
                        Map.of(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName(),
                               stopwatch.elapsed(TimeUnit.SECONDS)))
                .withEventName(Events.GPUS_V5_POPULATION_EVENT.toString());
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    @VisibleForTesting
    void updateClusters() {
        List<ClusterEntity> allClusterInfo = clusterRepository.getAllClusters();
        for (ClusterEntity entity : allClusterInfo) {
            if ((entity.getGpusV5() == null || entity.getGpusV5().isEmpty()) && entity.getNvcaVersion() != null) {
                ClusterEntity entityBefore = entity.toBuilder().build();
                entity.setGpusV5(NvcaConverter.getGpusV5(entity));
                nvcaClusterRepository.updateClusterRegistration(entity);

                // Sending telemetry event
                sendTelemetryEvent(entity);

                // Sending audit event
                Map<String, Object> auditProps = new HashMap<>();
                AuditUtils.populateAuditValuesForGpuV4PopulationTask(
                        auditProps, entity.getClusterId());
                auditService.sendAuditEventForClusterEntity(auditProps, entityBefore, entity);
            }
        }
    }

    private void sendTelemetryEvent(ClusterEntity clusterEntity) {
        try {
            Map<String, Object> metaData =
                    getMetadataForCluster(clusterEntity, "ClusterUpdated");
            telemetryEventClient.triggerEvent(List.of(
                    new GenericMetric()
                            .withClusterId(clusterEntity.getClusterId())
                            .withClusterName(clusterEntity.getClusterName())
                            .withMetadata(metaData)
                            .withEventName(GPUS_V5_POPULATION_EVENT.toString())));
        } catch (Exception e) {
            // Do not throw exceptions for telemetry failures
            log.warn(
                    "Error sending telemetry for the registration of cluster {}, with cluster group {}",
                    clusterEntity.getClusterName(), clusterEntity.getClusterGroupName());
        }
    }
}
