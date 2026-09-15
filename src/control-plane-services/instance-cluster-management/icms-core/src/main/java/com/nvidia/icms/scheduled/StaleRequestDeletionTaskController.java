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
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.scheduled.request.StaleRequestDeletionTask;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.time.Instant;
import java.time.ZoneId;
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
public class StaleRequestDeletionTaskController {
    public static final String STALE_REQUEST_DELETION_JOB_NAME = "StaleRequestDeletionTask";

    private final StaleRequestDeletionTask staleRequestDeletionTask;
    private final TelemetryEventClient telemetryEventClient;
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    @Scheduled(initialDelayString = "${icms.async-long-task-schedule-initial-delay}",
               fixedDelayString = "${icms.stale-request-deletion-task-schedule-duration}")
    public void instanceUpdateTask() {
        if (!CanJobRunBasedOnSchedule()) {
            return;
        }

        if (!icmsConfigurationProperties.isStateRequestDeletionTaskEnabled()) {
            log.info("Job: {} is not enabled existing the scheduled task",
                     STALE_REQUEST_DELETION_JOB_NAME);
            return;
        }

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;
        try {
            if (!lockProviderService.obtainLockWithTtl(STALE_REQUEST_DELETION_JOB_NAME,
                                                        icmsConfigurationProperties.getStaleRequestDeletionTaskLockTtlInSeconds())) {
                return;
            }
            stopwatch.start();
            staleRequestDeletionTask.execute();

        } catch (Exception exception) {
            log.error("{} job failed with error {}, exception:",
                      STALE_REQUEST_DELETION_JOB_NAME,
                      exception.getMessage(),
                      exception);
            capturedError = exception.getMessage();
        }

        GenericMetric genericMetric = new GenericMetric()
                .withError(capturedError)
                .withMetadata(Map.of(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName(),
                                     stopwatch.elapsed(TimeUnit.SECONDS),
                                     EventMetaData.THREAD_NAME.getName(), Thread.currentThread().getName()))
                .withEventName(Events.STALE_REQUEST_DELETION_TASK.toString());
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    private boolean CanJobRunBasedOnSchedule() {
        // Check current time to allow job to be run only during required period of time
        // [getStaleRequestDeletionStartUtcHour24H(), getStaleRequestDeletionEndUtcHour24H()]
        // if getStaleRequestDeletionStartUtcHour24H() == getStaleRequestDeletionEndUtcHour24H() then check is disabled
        if (icmsConfigurationProperties.getStaleRequestDeletionStartUtcHour24H() != icmsConfigurationProperties.getStaleRequestDeletionEndUtcHour24H()) {
            int currentHour = Instant.now().atZone(ZoneId.of("UTC")).getHour();
            if (icmsConfigurationProperties.getStaleRequestDeletionStartUtcHour24H() < icmsConfigurationProperties.getStaleRequestDeletionEndUtcHour24H()) {
                // normal order in 24 hrs like [20-23]
                if (currentHour < icmsConfigurationProperties.getStaleRequestDeletionStartUtcHour24H()  ||
                        currentHour > icmsConfigurationProperties.getStaleRequestDeletionEndUtcHour24H()) {
                    return false;
                }
            } else if (currentHour < icmsConfigurationProperties.getStaleRequestDeletionStartUtcHour24H() &&
                    currentHour > icmsConfigurationProperties.getStaleRequestDeletionEndUtcHour24H()) { // like [22-02]
                return false;
            }
        }

        return true;
    }
}

