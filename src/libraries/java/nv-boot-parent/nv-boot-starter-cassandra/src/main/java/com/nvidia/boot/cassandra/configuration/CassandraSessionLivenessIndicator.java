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
package com.nvidia.boot.cassandra.configuration;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.function.BooleanSupplier;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.DisposableBean;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthIndicator;

/**
 * Liveness signal for a Cassandra session that cannot recover on its own.
 *
 * <p>After a full Cassandra outage the driver session can stay broken even though Cassandra is
 * back, and only a new session or process recovers it. Putting the regular {@code cassandra}
 * contributor in the liveness group handled that by restarting every replica during every
 * outage. This indicator reports DOWN only when the application session has kept failing for
 * the grace period while a newly built session succeeds. While Cassandra itself is unavailable
 * the new session fails too, so the indicator stays UP and nothing is restarted.
 *
 * <p>Checks run on a background thread so the liveness endpoint only reads the last result.
 */
@Slf4j
public class CassandraSessionLivenessIndicator implements HealthIndicator, DisposableBean {

    private final BooleanSupplier sessionCheck;
    private final BooleanSupplier freshSessionCheck;
    private final Duration gracePeriod;
    private final Clock clock;
    private final ScheduledExecutorService executor;

    private volatile Health health = Health.up().build();
    private Instant stuckSince;

    CassandraSessionLivenessIndicator(BooleanSupplier sessionCheck,
                                      BooleanSupplier freshSessionCheck,
                                      Duration gracePeriod, Clock clock) {
        this.sessionCheck = sessionCheck;
        this.freshSessionCheck = freshSessionCheck;
        this.gracePeriod = gracePeriod;
        this.clock = clock;
        this.executor = null;
    }

    CassandraSessionLivenessIndicator(BooleanSupplier sessionCheck,
                                      BooleanSupplier freshSessionCheck,
                                      Duration gracePeriod, Duration checkInterval) {
        this.sessionCheck = sessionCheck;
        this.freshSessionCheck = freshSessionCheck;
        this.gracePeriod = gracePeriod;
        this.clock = Clock.systemUTC();
        this.executor = Executors.newSingleThreadScheduledExecutor(runnable -> {
            var thread = new Thread(runnable, "cassandra-session-liveness");
            thread.setDaemon(true);
            return thread;
        });
        executor.scheduleWithFixedDelay(this::evaluateSafely, checkInterval.toMillis(),
                                        checkInterval.toMillis(), TimeUnit.MILLISECONDS);
    }

    @Override
    public Health health() {
        return health;
    }

    private void evaluateSafely() {
        try {
            evaluate();
        } catch (RuntimeException e) {
            log.warn("Cassandra session liveness check failed unexpectedly", e);
        }
    }

    void evaluate() {
        if (sessionCheck.getAsBoolean()) {
            if (stuckSince != null) {
                log.info("Cassandra session recovered without a restart");
            }
            stuckSince = null;
            health = Health.up().build();
            return;
        }
        if (!freshSessionCheck.getAsBoolean()) {
            // Cassandra itself is unavailable. A restart cannot help, so stay live.
            stuckSince = null;
            health = Health.up().withDetail("cassandra", "unavailable").build();
            return;
        }
        var now = clock.instant();
        if (stuckSince == null) {
            stuckSince = now;
            log.warn("Cassandra session is failing while a new session succeeds; "
                     + "restarting after {} if it does not recover", gracePeriod);
        }
        var stuckFor = Duration.between(stuckSince, now);
        if (stuckFor.compareTo(gracePeriod) >= 0) {
            log.error("Cassandra session has failed for {} while a new session succeeds; reporting "
                      + "liveness DOWN so the container restarts", stuckFor);
            health = Health.down().withDetail("stuckSince", stuckSince.toString()).build();
        } else {
            health = Health.up().withDetail("stuckSince", stuckSince.toString()).build();
        }
    }

    @Override
    public void destroy() {
        if (executor != null) {
            executor.shutdownNow();
        }
    }
}
