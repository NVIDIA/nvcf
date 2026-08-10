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

import static com.nvidia.nvct.util.NvctConstants.METER_TASK_ERROR;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_NCA_ID;
import static com.nvidia.nvct.util.NvctConstants.TAG_ERROR_SOURCE;
import static com.nvidia.nvct.util.NvctConstants.TAG_NCA_ID;
import static java.time.Duration.ofMinutes;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.Meter;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.observation.annotation.Observed;
import io.micrometer.tracing.Tracer;
import java.util.Map;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
public class TaskErrorMetricsService {

    private static final String NVCT_API_ERROR_SOURCE = "nvct-api";

    private final LoadingCache<String, Counter> taskErrorCounters;
    private final Tracer tracer;

    public TaskErrorMetricsService(MeterRegistry meterRegistry, Tracer tracer) {
        this.tracer = tracer;
        this.taskErrorCounters = Caffeine.newBuilder()
                .expireAfter(new TaskMetricsExpirationPolicy(ofMinutes(45)))
                .scheduler(Scheduler.systemScheduler())
                .evictionListener((key, counter, cause) -> {
                    if (counter instanceof Meter m) {
                        meterRegistry.remove(m);
                    }
                })
                .build(ncaId -> Counter.builder(METER_TASK_ERROR)
                        .tag(TAG_NCA_ID, ncaId)
                        .tag(TAG_ERROR_SOURCE, NVCT_API_ERROR_SOURCE)
                        .register(meterRegistry));
    }

    // Invoked everytime a Task(belonging to the specified account) errors.
    @Observed(name = "task.error", contextualName = "record-task-error")
    public void recordTaskError(String ncaId) {
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        taskErrorCounters.get(ncaId).increment();
    }

}
