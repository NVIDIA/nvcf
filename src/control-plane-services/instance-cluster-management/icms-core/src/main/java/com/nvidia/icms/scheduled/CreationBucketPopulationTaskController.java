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
import com.nvidia.icms.service.scheduled.CreationBucketPopulationTask;
import com.nvidia.icms.service.scheduled.CreationBucketPopulationTask.PopulationResult;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
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
@AllArgsConstructor
@Profile(SCHEDULED_JOBS_PROFILES)
public class CreationBucketPopulationTaskController {
    public static final String CREATION_BUCKET_POPULATION_TASK_NAME =
            "CreationBucketPopulationTask";

    private final CreationBucketPopulationTask task;
    private final TelemetryEventClient telemetryEventClient;
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties configuration;

    @Scheduled(initialDelayString = "${icms.async-hourly-task-schedule-initial-delay}",
            fixedDelayString = "${icms.async-hourly-task-schedule-duration}")
    public void populateCreationBuckets() {
        if (!configuration.isCreationBucketPopulationTaskEnabled()) {
            return;
        }

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;
        PopulationResult result = new PopulationResult(0, 0, 0, 0);
        try {
            if (!lockProviderService.obtainLockWithTtl(
                    CREATION_BUCKET_POPULATION_TASK_NAME,
                    configuration.getCreationBucketPopulationTaskLockTtlInSeconds())) {
                return;
            }
            stopwatch.start();
            result = task.execute();
        } catch (Exception exception) {
            capturedError = exception.getMessage();
            log.error("{} job failed with error: {}",
                    CREATION_BUCKET_POPULATION_TASK_NAME, capturedError, exception);
        }

        Map<String, Object> metadata = new HashMap<>();
        metadata.put(EventMetaData.EXECUTION_TIME.getName(),
                stopwatch.elapsed(TimeUnit.SECONDS));
        metadata.put(EventMetaData.THREAD_NAME.getName(), Thread.currentThread().getName());
        metadata.put("requestsUpdated", result.requestsUpdated());
        metadata.put("requestsFailed", result.requestsFailed());
        metadata.put("instancesUpdated", result.instancesUpdated());
        metadata.put("instancesFailed", result.instancesFailed());

        telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                .withEventName(Events.CREATION_BUCKET_POPULATION_TASK.toString())
                .withError(capturedError)
                .withMetadata(metadata)));
    }
}
