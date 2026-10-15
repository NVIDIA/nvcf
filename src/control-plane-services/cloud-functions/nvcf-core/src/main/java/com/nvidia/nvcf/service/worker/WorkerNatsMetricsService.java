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
package com.nvidia.nvcf.service.worker;

import static com.nvidia.nvcf.util.NvcfConstants.TAG_FUNCTION_VERSION_ID;
import static java.time.Duration.ofMinutes;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.MeterRegistry;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicLong;
import org.springframework.stereotype.Service;

@Service
public class WorkerNatsMetricsService {

    public static final String QUEUE_DEPTH_AND_INFLIGHT_GAUGE_NAME =
            "nvcf.worker.nats.queue.depth.and.inflight";
    private static final String QUEUE_DEPTH_AND_INFLIGHT_GAUGE_DESCRIPTION =
            "Function version '%s': Queue depth + inflight messages";

    private final LoadingCache<UUID, QueueDepthAndInflightGauge> queueDepthAndInflightGauges;

    public WorkerNatsMetricsService(MeterRegistry meterRegistry) {
        queueDepthAndInflightGauges = Caffeine.newBuilder()
                .expireAfterAccess(ofMinutes(45))
                .scheduler(Scheduler.systemScheduler())
                .evictionListener((functionVersionId, gauge, cause) -> {
                    if (gauge instanceof QueueDepthAndInflightGauge queueDepthAndInflightGauge) {
                        meterRegistry.remove(queueDepthAndInflightGauge.meter());
                    }
                })
                .build(functionVersionId -> {
                    var value = new AtomicLong();
                    var meter = Gauge.builder(QUEUE_DEPTH_AND_INFLIGHT_GAUGE_NAME,
                                              value, AtomicLong::get)
                            .tag(TAG_FUNCTION_VERSION_ID, functionVersionId.toString())
                            .description(QUEUE_DEPTH_AND_INFLIGHT_GAUGE_DESCRIPTION
                                                 .formatted(functionVersionId))
                            .register(meterRegistry);
                    return new QueueDepthAndInflightGauge(value, meter);
                });
    }

    public void recordQueueDepthAndInflight(UUID functionVersionId, long queueDepthAndInflight) {
        queueDepthAndInflightGauges.get(functionVersionId).value().set(queueDepthAndInflight);
    }

    private record QueueDepthAndInflightGauge(AtomicLong value, Gauge meter) {
    }
}
