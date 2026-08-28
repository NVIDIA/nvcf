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

import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;

import org.springframework.context.annotation.Profile;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

import com.google.common.base.Stopwatch;
import static com.nvidia.icms.configuration.SchedulingConfiguration.SCHEDULED_JOBS_PROFILES;
import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.outbound.nats.NatsStreamManager;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;

import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@Service
@AllArgsConstructor
@Profile(SCHEDULED_JOBS_PROFILES)
public class GlobalNatsStreamValidationTaskController {
    public static final String GLOBAL_NATS_STREAM_VALIDATION_JOB_NAME = "globalNatsStreamValidation";

    private final TelemetryEventClient telemetryEventClient;
    private final LockProviderService lockProviderService;
    private final NatsConfigurationProperties natsConfigurationProperties;
    private final NatsStreamManager natsStreamManager;

    // This task will execute every 3 minute to validate global streams
    @Scheduled(initialDelayString = "${icms.nats.global-stream-validation-initial-delay:PT3M}",
               fixedDelayString = "${icms.nats.global-stream-validation-interval:PT3M}")
    public void validateGlobalStreams() {
        if (!natsConfigurationProperties.isGlobalStreamValidationTaskEnabled()) {
            log.info("Global nats stream validation task is not enabled");
            return;
        }

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;

        try {
            int lockTtlSeconds = Math.toIntExact(
                    natsConfigurationProperties.getGlobalStreamValidationTaskLockTtl().toSeconds());
            if (!lockProviderService.obtainLockWithTtl(GLOBAL_NATS_STREAM_VALIDATION_JOB_NAME,
                    lockTtlSeconds)) {
                return;
            }
            stopwatch.start();

            // Create streams if not exist
            natsStreamManager.validateNatsStreams();
            
        } catch (Exception exception) {
            log.error("{} job failed with error: {} exception: ", GLOBAL_NATS_STREAM_VALIDATION_JOB_NAME,
                    exception.getMessage(),
                    exception);

            capturedError = exception.getMessage();
        }

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withError(capturedError)
                .withMetadata(Map.of(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName(),
                                     stopwatch.elapsed(TimeUnit.SECONDS),
                                     EventMetaData.THREAD_NAME.getName(), Thread.currentThread().getName()))
                .withEventName(Events.GLOBAL_NATS_STREAM_VALIDATION_TASK.toString())));
    }
}
