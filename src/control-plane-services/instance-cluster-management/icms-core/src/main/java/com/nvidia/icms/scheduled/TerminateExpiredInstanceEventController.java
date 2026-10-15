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
import com.nvidia.icms.service.scheduled.request.TerminateInstanceEventService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.context.annotation.Profile;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;

@Slf4j
@Service
@AllArgsConstructor
@Profile(SCHEDULED_JOBS_PROFILES)
public class TerminateExpiredInstanceEventController {
    public static final String TERMINATE_EXPIRED_INSTANCES_JOB_NAME = "TerminateExpiredInstances";

    private final TerminateInstanceEventService terminateInstanceEventService;
    private final TelemetryEventClient telemetryEventClient;
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    // This task will execute after every "24 hours" and will terminate expired instances.
    // Instance lifetime is configurable and is currently set to 90 days.
    @Scheduled(initialDelayString = "${icms.async-long-task-schedule-initial-delay}",
               fixedDelayString = "${icms.async-long-task-schedule-duration}")
    public void terminateExpiredInstances() {

        if (!icmsConfigurationProperties.isTerminateExpiredInstancesEnabled()) {
            log.debug("Terminate expired instances task is not enabled");
            return;
        }

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;

        try {
            if (!lockProviderService.obtainLockWithTtl(TERMINATE_EXPIRED_INSTANCES_JOB_NAME,
                                                        icmsConfigurationProperties.getTerminateExpiredInstancesTaskLockTtlInSeconds())) {
                return;
            }
            stopwatch.start();
            terminateInstanceEventService.execute();
        } catch (Exception exception) {
            log.error("{} job failed with error: {} exception: ",
                      TERMINATE_EXPIRED_INSTANCES_JOB_NAME,
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
                .withEventName(Events.EXPIRED_INSTANCE_TERMINATION_TASK.toString());
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }
}
