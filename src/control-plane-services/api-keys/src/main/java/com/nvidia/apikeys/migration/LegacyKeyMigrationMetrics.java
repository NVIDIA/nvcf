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

package com.nvidia.apikeys.migration;

import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import java.time.Duration;
import java.util.EnumMap;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;
import org.springframework.stereotype.Component;

@Component
public class LegacyKeyMigrationMetrics {

    static final String KEYS = "nvcf_api_keys_legacy_key_migration_keys";
    static final String RUNS = "nvcf_api_keys_legacy_key_migration_runs";
    static final String RUN_DURATION = "nvcf_api_keys_legacy_key_migration_run_duration";
    static final String COMPLETED = "nvcf_api_keys_legacy_key_migration_completed";
    static final String TAG_RESULT = "result";
    static final String TAG_OUTCOME = "outcome";

    private final Map<KeyResult, Counter> keyCounters = new EnumMap<>(KeyResult.class);
    private final Map<RunOutcome, Counter> runCounters = new EnumMap<>(RunOutcome.class);
    private final Timer runDuration;
    private final AtomicInteger completed = new AtomicInteger();

    public LegacyKeyMigrationMetrics(MeterRegistry registry) {
        for (var result : KeyResult.values()) {
            keyCounters.put(result, Counter.builder(KEYS)
                    .description("Legacy keys processed by the legacy key migration")
                    .tag(TAG_RESULT, tagValue(result))
                    .register(registry));
        }
        for (var outcome : RunOutcome.values()) {
            runCounters.put(outcome, Counter.builder(RUNS)
                    .description("Legacy key migration runs")
                    .tag(TAG_OUTCOME, tagValue(outcome))
                    .register(registry));
        }
        runDuration = Timer.builder(RUN_DURATION)
                .description("Duration of a legacy key migration run")
                .register(registry);
        Gauge.builder(COMPLETED, completed, AtomicInteger::get)
                .description("1 after a clean run stops the legacy key migration on this replica")
                .register(registry);
    }

    public void recordKey(KeyResult result) {
        keyCounters.get(result).increment();
    }

    public void recordRun(RunOutcome outcome, Duration elapsed) {
        runCounters.get(outcome).increment();
        runDuration.record(elapsed);
    }

    public void markCompleted() {
        completed.set(1);
    }

    static String tagValue(Enum<?> value) {
        return value.name().toLowerCase(Locale.ROOT);
    }
}
