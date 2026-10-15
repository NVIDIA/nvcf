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
package com.nvidia.icms.scheduled.dbcache;

import static com.nvidia.icms.service.telemetry.TelemetryEventClient.POD_NAME_ENV_KEY;

import com.google.common.base.Stopwatch;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.byoc.WildCardAllowedClustersCacheService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.locks.ReentrantLock;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
/**
 * This task will cache commonly used WILDCARD NCA ID cluster information in each ICMS service pod
 * This task should run on each pod to have cached information available
 * We should not take global lock while executing this task as this task should run on every pod instance
 */
public class WildCardNcaIdCacheTaskController {
    public static final String WILD_CARD_NCA_ID_CACHE_TASK_NAME = "WildCardNcaIdCacheInfoCacheTask";

    private final WildCardAllowedClustersCacheService wildCardAllowedClustersCacheService;
    private final TelemetryEventClient telemetryEventClient;

    /*
    This task will update local cache per pod hence we don't need global lock
    next job schedule time = last job schedule time + fixedDelayString
    fixedDelayString will make sure that next job iteration will run after completing previous job and with delay of fixedDelayString

    max duration for cache will be stale = fixedDelayString
     */
    @Scheduled(initialDelayString = "${icms.async-wild-card-cache-update-task-schedule-initial-delay}",
            fixedDelayString = "${icms.async-wild-card-cache-update-task-schedule-duration}")
    public void updateWildCardAllowedClusterCache() {

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;

        try {
            stopwatch.start();
            wildCardAllowedClustersCacheService.refreshCache();

        } catch (Exception exception) {
            log.error("{} job failed with error: {} exception: ",
                      WILD_CARD_NCA_ID_CACHE_TASK_NAME,
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
                .withEventName(Events.WILD_CARD_NCA_ID_CACHE_TASK_NAME.toString());
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }
}
