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
package com.nvidia.nvct.service.metrics;

import static com.nvidia.nvct.util.NvctConstants.METER_TASK_RUNNING;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_ACCOUNT_NAME;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_NCA_ID;
import static com.nvidia.nvct.util.NvctConstants.TAG_NCA_ID;
import static com.nvidia.nvct.util.NvctConstants.TAG_ORG_NAME;
import static java.time.Duration.ofMinutes;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Expiry;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.Meter;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.observation.annotation.Observed;
import io.micrometer.tracing.Tracer;
import jakarta.validation.constraints.PositiveOrZero;
import java.time.Duration;
import java.util.Map;
import lombok.Builder;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
public class TaskRunningMetricsService {

    private final TaskService taskService;
    private final Tracer tracer;

    @Builder
    public record TaskRunningKey(String ncaId, String accountName) { }

    private final Cache<TaskRunningKey, Long> taskRunningValues = Caffeine.newBuilder()
            .expireAfterAccess(ofMinutes(45))
            .scheduler(Scheduler.systemScheduler())
            .build();

    private final LoadingCache<TaskRunningKey, Gauge> taskRunningGauges;

    public TaskRunningMetricsService(MeterRegistry meterRegistry, TaskService taskService, Tracer tracer) {
        this.taskService = taskService;
        this.tracer = tracer;
        this.taskRunningGauges = Caffeine.newBuilder()
                .expireAfter(new TaskRunningExpirationPolicy(ofMinutes(45)))
                .scheduler(Scheduler.systemScheduler())
                .evictionListener((key, gauge, cause) -> {
                    if (gauge instanceof Meter m) {
                        meterRegistry.remove(m);
                    }
                })
                .build(key -> Gauge.builder(METER_TASK_RUNNING,
                                            () -> taskRunningValues.getIfPresent(key))
                        .tag(TAG_NCA_ID, key.ncaId())
                        .tag(TAG_ORG_NAME, key.accountName())
                        .register(meterRegistry));
    }

    /**
     * Records count of tasks with RUNNING status for the specified account.
     * <p>
     * Micrometer Gauge has async nature. On building the Gauge meter, we have to provide a
     * lambda/supplier to return actual values to be recorded. We have a cache to store real
     * values. The meter will get the value from the cache. We store the meter in another
     * async loading cache. The meter is created on the first request to async loading cache.
     * Therefore, we need to call meter cache on each invocation to make sure the metric is
     * created/populated.
     * </p>
     * @param ncaId NVIDIA Cloud Account id
     * @param accountName Account name -- Can be NGC Org Name
     */
    @Observed(name = "task.running", contextualName = "record-running-task")
    public void recordRunningTask(String ncaId, String accountName) {
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId,
                                                      SPAN_TAG_ACCOUNT_NAME, accountName));
        var taskRunningKey = new TaskRunningKey(ncaId, accountName);
        updateValuesCacheWithTaskCountByNcaId(taskRunningKey);
        taskRunningGauges.get(taskRunningKey);
    }

    private void updateValuesCacheWithTaskCountByNcaId(TaskRunningKey key) {
        var count = taskRunningValues.getIfPresent(key);
        if (count == null) {
            // Get the count from the DB if the cache doesn't have it.
            count = taskService.countByNcaId(key.ncaId());
            taskRunningValues.put(key, count);
            return;
        }
        count++;
        taskRunningValues.put(key, count);
    }

    @RequiredArgsConstructor
    private static class TaskRunningExpirationPolicy
            implements Expiry<TaskRunningKey, Gauge> {

        @NonNull
        private final Duration timeToLive;

        @Override
        public long expireAfterCreate(
                TaskRunningKey key, Gauge value, long currentTime) {
            return timeToLive.toNanos();
        }

        @Override
        public long expireAfterUpdate(
                TaskRunningKey key, Gauge value, long currentTime,
                @PositiveOrZero long currentDuration) {
            return currentDuration;
        }

        @Override
        public long expireAfterRead(
                TaskRunningKey key, Gauge value, long currentTime,
                @PositiveOrZero long currentDuration) {
            return currentDuration;
        }
    }
}
