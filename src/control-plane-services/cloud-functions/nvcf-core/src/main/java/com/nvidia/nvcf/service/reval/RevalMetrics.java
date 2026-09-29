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
package com.nvidia.nvcf.service.reval;

import static com.nvidia.nvcf.util.NvcfConstants.TAG_NCA_ID;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.Meter;
import io.micrometer.core.instrument.MeterRegistry;
import jakarta.validation.constraints.NotNull;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatusCode;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class RevalMetrics {
    public record AccountKey(
            @NotNull String ncaId,
            @NotNull HttpStatusCode statusCode
    ) {}

    private static final String REVAL_REQUEST_COUNTER_NAME = "nvcf.service.reval.counter";
    private static final String REVAL_REQUEST_COUNTER_DESC = "Counts requests to the ReVal service";
    private static final String REVAL_REQUEST_COUNTER_UNIT = "requests";
    private static final String TAG_STATUS_CODE = "status";

    private final LoadingCache<AccountKey, Counter> revalRequestCounter;

    public RevalMetrics(MeterRegistry meterRegistry) {
        revalRequestCounter = Caffeine.newBuilder()
                .expireAfterAccess(24, TimeUnit.DAYS)
                .scheduler(Scheduler.systemScheduler())
                .evictionListener((key, counter, cause) -> {
                    if (counter instanceof Meter m) {
                        meterRegistry.remove(m);
                    }
                })
                .build(key -> Counter.builder(REVAL_REQUEST_COUNTER_NAME)
                        .tag(TAG_NCA_ID, key.ncaId())
                        .tag(TAG_STATUS_CODE, Integer.toString(key.statusCode().value()))
                        .baseUnit(REVAL_REQUEST_COUNTER_UNIT)
                        .description(REVAL_REQUEST_COUNTER_DESC)
                        .register(meterRegistry));
    }

    public void incrementRequestCounter(String ncaId, HttpStatusCode statusCode) {
        revalRequestCounter.get(new AccountKey(ncaId, statusCode)).increment();
    }
}
