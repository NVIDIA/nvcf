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
package com.nvidia.icms.service.metrics;

import static com.nvidia.icms.service.metrics.MetricsConstants.METER_TASK_ERROR;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_ERROR_SOURCE;
import static com.nvidia.icms.service.metrics.MetricsConstants.TAG_NCA_ID;
import static java.time.Duration.ofMinutes;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Expiry;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.Meter;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.observation.annotation.Observed;
import java.time.Duration;
import lombok.Builder;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import jakarta.validation.constraints.PositiveOrZero;
import org.springframework.stereotype.Service;

@Service
@Slf4j
public class InstanceErrorMetricsService {

    @Builder
    public record InstanceErrorMetricsKey(String ncaId, String errorSource) { }

    private final LoadingCache<InstanceErrorMetricsKey, Counter> taskErrorCounters;

    public InstanceErrorMetricsService(MeterRegistry meterRegistry) {
        this.taskErrorCounters = Caffeine.newBuilder()
                .expireAfter(new InstanceErrorMetricsExpirationPolicy(ofMinutes(5)))
                .scheduler(Scheduler.systemScheduler())
                .evictionListener((key, counter, cause) -> {
                    if (counter instanceof Meter m) {
                        meterRegistry.remove(m);
                    }
                })
                .build(instanceErrorMetricsKey -> Counter.builder(METER_TASK_ERROR)
                        .tag(TAG_NCA_ID, instanceErrorMetricsKey.ncaId())
                        .tag(TAG_ERROR_SOURCE, instanceErrorMetricsKey.errorSource())
                        .register(meterRegistry));
    }

    /**
     * Records count of task instances that errored.
     * e.g:
     *    nvct_task_error_total{env="local",error_source="test_error",
     *        hostDc="localhost",hostId="local-host-id",host_dc="localhost",
     *        host_id="local-host-id",key="dummy-key",nca_id="ncaId1",} 2.0
     * @param ncaId NVIDIA Cloud Account id
     * @param errorSource Error Source - can be ICMS or compute plane components
     */
    @Observed
    public void recordTaskError(
            String ncaId,
            String errorSource) {
        var instanceErrorMetricsKey = new InstanceErrorMetricsKey(ncaId, errorSource);
        taskErrorCounters.get(instanceErrorMetricsKey).increment();
    }

    @RequiredArgsConstructor
    private static class InstanceErrorMetricsExpirationPolicy
            implements Expiry<InstanceErrorMetricsKey, Counter> {

        @NonNull
        private final Duration timeToLive;

        @Override
        public long expireAfterCreate(
                InstanceErrorMetricsKey key, Counter value, long currentTime) {
            return timeToLive.toNanos();
        }

        @Override
        public long expireAfterUpdate(
                InstanceErrorMetricsKey key, Counter value, long currentTime,
                @PositiveOrZero long currentDuration) {
            return currentDuration;
        }

        @Override
        public long expireAfterRead(
                InstanceErrorMetricsKey key, Counter value, long currentTime,
                @PositiveOrZero long currentDuration) {
            return currentDuration;
        }
    }
}
