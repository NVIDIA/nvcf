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
import com.nvidia.icms.service.scheduled.instance.ActiveInstanceMonitoringTaskService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.service.LockProviderService;
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
public class ActiveInstanceMonitoringTaskController {
    public static final String ACTIVE_INSTANCE_MONITORING_TASK = "ACTIVE_INSTANCE_MONITORING_TASK";

    private final ActiveInstanceMonitoringTaskService activeInstanceMonitoringTaskService;
    private final TelemetryEventClient telemetryEventClient;
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    // This task will execute after every "1 min" and will update the instance state whose cloud
    // is unhealthy
    @Scheduled(initialDelayString = "${icms.async-short-task-schedule-initial-delay}",
               fixedDelayString = "${icms.async-short-task-schedule-duration}")
    public void executeMonitoringTask() {

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;

        try {
            // When switched to V2 job, this lock should be done by day or be removed since executor has another lock
            if (!lockProviderService.obtainLockWithTtl(ACTIVE_INSTANCE_MONITORING_TASK,
                                                        icmsConfigurationProperties.getCloudFailureDetectionTaskLockTtlInSeconds())) {
                return;
            }

            stopwatch.start();
            activeInstanceMonitoringTaskService.execute();
        } catch (Exception exception) {
            log.error("{} job failed with error: {} exception: ", ACTIVE_INSTANCE_MONITORING_TASK,
                      exception.getMessage(),
                      exception);

            capturedError = exception.getMessage();
        }

        // Sending event per job execution
        GenericMetric genericMetric = new GenericMetric()
                .withError(capturedError)
                .withMetadata(Map.of(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName(),
                                     stopwatch.elapsed(TimeUnit.SECONDS),
                                     EventMetaData.THREAD_NAME.getName(), Thread.currentThread().getName()))
                .withEventName(Events.ACTIVE_INSTANCE_MONITORING_TASK.toString());
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }
}
