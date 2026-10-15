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
import com.nvidia.icms.service.scheduled.request.CancelRequestEventService;
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
public class CancelRequestEventController {
    public static final String CANCEL_LINGERING_REQUEST_JOB_NAME = "cancelLingeringRequests";

    private final CancelRequestEventService cancelRequestEventService;
    private final TelemetryEventClient telemetryEventClient;
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    // This task will execute after every "5 min" and will cancel lingering instance requests.
    // requests which are 30 mins(configurable) days older and don't have update
    @Scheduled(initialDelayString = "${icms.async-short-task-schedule-initial-delay}",
               fixedDelayString = "${icms.async-short-task-schedule-duration}")
    public void cancelLingeringRequests() {

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;

        try {
            if (!lockProviderService.obtainLockWithTtl(CANCEL_LINGERING_REQUEST_JOB_NAME,
                                                        icmsConfigurationProperties.getCancelLingeringRequestsTaskLockTtlInSeconds())) {
                return;
            }
            stopwatch.start();
            cancelRequestEventService.execute();
        } catch (Exception exception) {
            log.error("{} job failed with error: {} exception: ", CANCEL_LINGERING_REQUEST_JOB_NAME,
                      exception.getMessage(),
                      exception);

            capturedError = exception.getMessage();
        }

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                           .withError(capturedError)
                                           .withMetadata(Map.of(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName(),
                                                                stopwatch.elapsed(TimeUnit.SECONDS),
                                                                EventMetaData.THREAD_NAME.getName(), Thread.currentThread().getName()))
                                           .withEventName(Events.CANCEL_REQUEST_TASK.toString())));
    }
}
