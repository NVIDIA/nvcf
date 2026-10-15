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
package com.nvidia.icms.scheduled;


import static com.nvidia.icms.configuration.SchedulingConfiguration.SCHEDULED_JOBS_PROFILES;

import com.google.common.base.Stopwatch;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByDayEntity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2ByDayEntity;
import com.nvidia.icms.service.ExpiredInstanceTerminateService;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.TerminateInstanceService;
import com.nvidia.icms.service.scheduled.cleanup.DatabaseCleanupTask;
import com.nvidia.icms.service.scheduled.cleanup.InstancesByDayCleanupExecutor;
import com.nvidia.icms.service.scheduled.cleanup.RequestsByDayCleanupExecutor;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
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
@AllArgsConstructor
@Profile(SCHEDULED_JOBS_PROFILES)
public class DatabaseCleanupTaskController {
    public static final String DATABASE_CLEANUP_JOB_NAME = "DatabaseCleanupJob";

    private final TelemetryEventClient telemetryEventClient;
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    private final TerminateInstanceService terminateInstanceService;

    private final InstanceRequestV2Repository instanceRequestV2Repository;

    private final InstanceV2Repository instanceV2Repository;

    private final ExpiredInstanceTerminateService expiredInstanceTerminateService;


    @Scheduled(initialDelayString = "${icms.async-hourly-task-schedule-initial-delay}",
            fixedDelayString = "${icms.async-hourly-task-schedule-duration}")
    public void performCleanupForAllTables() {
        Stopwatch stopwatch = Stopwatch.createUnstarted();
        stopwatch.start();

        // Requests
        cleanupRequests();

        //instances
        cleanupInstances();

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                           .withMetadata(Map.of(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName(),
                                                                                stopwatch.elapsed(TimeUnit.SECONDS),
                                                                                EventMetaData.THREAD_NAME.getName(), Thread.currentThread().getName()))
                                                           .withEventName(Events.DATABASE_CLEANUP_TASK.toString())));
    }

    public void cleanupRequests() {
        try {
            if (!icmsConfigurationProperties.isDatabaseCleanupRequestsEnabled()) {
                 log.info("Job {}: Cleanup request tables is not enabled", DATABASE_CLEANUP_JOB_NAME);
                return;
            }

            DatabaseCleanupTask<InstanceRequestV2ByDayEntity> task = new DatabaseCleanupTask<>(
                    icmsConfigurationProperties,
                    telemetryEventClient,
                    lockProviderService);
            RequestsByDayCleanupExecutor executor = new RequestsByDayCleanupExecutor(
                    instanceRequestV2Repository,
                    terminateInstanceService,
                    icmsConfigurationProperties);

            task.execute(executor, "requests_by_day");
        }
        catch(Exception e) {
            log.error("Job {} :Error of cleaning tables for requests {}", DATABASE_CLEANUP_JOB_NAME, e.getMessage(), e);
        }
    }

    public void cleanupInstances() {
        try {
            if (!icmsConfigurationProperties.isDatabaseCleanupInstancesEnabled()) {
                log.info("Job {}: Cleanup instance tables is not enabled", DATABASE_CLEANUP_JOB_NAME);
                return;
            }

            DatabaseCleanupTask<InstanceByDayEntity> task = new DatabaseCleanupTask<>(
                    icmsConfigurationProperties,
                    telemetryEventClient,
                    lockProviderService);
            InstancesByDayCleanupExecutor executor = new InstancesByDayCleanupExecutor(
                    instanceV2Repository,
                    instanceRequestV2Repository,
                    expiredInstanceTerminateService,
                    icmsConfigurationProperties);

            task.execute(executor, "instances_by_day");
        }
        catch(Exception e) {
            log.error("Job {} :Error of cleaning tables for instances {}", DATABASE_CLEANUP_JOB_NAME, e.getMessage(), e);
        }
    }

}
