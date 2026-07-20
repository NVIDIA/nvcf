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

package com.nvidia.boot.core.warmup;

import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.concurrent.CompletableFuture;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.AccessLevel;
import lombok.Getter;
import lombok.RequiredArgsConstructor;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthIndicator;
import org.springframework.context.ApplicationListener;

/**
 * Base class for cache and dependency warmup at application start. Implement {@link
 * #createWarmupTasks()} in a concrete type annotated with {@code @Component}; the health indicator
 * reports {@link Health#up()} after warmup completes when blocking is enabled.
 */
@Slf4j
public abstract class BootWarmupBase
        implements HealthIndicator, ApplicationListener<ApplicationReadyEvent> {

    // Cap for default timeout when maxTimeout is null or non-positive.
    private static final long MAX_TIMEOUT_MINUTES = 5;

    @Getter
    @Setter(AccessLevel.PRIVATE)
    private boolean warmingComplete;

    @Getter
    @Setter(AccessLevel.PRIVATE)
    private boolean blockHealthStatusUntilComplete;

    // Latest instant after which health() stops reporting DOWN due to incomplete warmup (default
    // cap MAX_TIMEOUT_MINUTES when maxTimeout is not set).
    @Getter
    @Setter(AccessLevel.PRIVATE)
    private Instant timeoutExceeded;

    // blockHealthStatusUntilComplete: if true, health stays DOWN until warmup finishes (or timeout).
    // Warmup still runs when false.
    protected BootWarmupBase(boolean blockHealthStatusUntilComplete) {
        this(blockHealthStatusUntilComplete, null);
    }

    // blockHealthStatusUntilComplete: if true, health stays DOWN until warmup finishes (or timeout).
    // maxTimeout: max wait for createWarmupTasks() before health reverts; null or non-positive uses
    // MAX_TIMEOUT_MINUTES minutes.
    protected BootWarmupBase(boolean blockHealthStatusUntilComplete, Duration maxTimeout) {
        this.setBlockHealthStatusUntilComplete(blockHealthStatusUntilComplete);
        if (maxTimeout != null && !maxTimeout.isZero() && !maxTimeout.isNegative()) {
            this.setTimeoutExceeded(Instant.now().plus(maxTimeout));
        } else {
            this.setTimeoutExceeded(Instant.now().plus(Duration.ofMinutes(MAX_TIMEOUT_MINUTES)));
        }
    }

    @Override
    public Health health() {
        String key = this.getClass().getName() + ".CacheStatus";

        if (this.isBlockHealthStatusUntilComplete()
                && !this.isWarmingComplete()
                && timeoutExceeded.isBefore(Instant.now())) {
            log.info("Warmup did not complete in time. Re-enabling health check");
            this.setWarmingComplete(true);
        }

        if (!this.isBlockHealthStatusUntilComplete() || this.isWarmingComplete()) {
            return Health.up().withDetail(key, "Warming Complete").build();
        }

        return Health.down().withDetail(key, "Warming Incomplete").build();
    }

    @Override
    public void onApplicationEvent(ApplicationReadyEvent event) {
        var start = Instant.now();
        int totalSuccesses = 0;
        int total = 0;
        var runnables = this.createWarmupTasks();
        var map = runnables.stream().collect(Collectors.groupingBy(WarmupRunnable::getOrder));

        var ordering = map.keySet().stream().sorted().toList();

        // In Java 9+, ForkJoinPool.commonPool() (in CompletableFuture.supplyAsync()) threads use the system AppClassLoader
        // However, AppClassLoader cannot load classes (libs) that Spring needs (it uses LaunchedURLClassLoader).
        // Capture the caller's classloader so we can propagate into each worker thread.
        // 
        // https://stackoverflow.com/questions/49113207/completablefuture-forkjoinpool-set-class-loader
        // https://github.com/spring-projects/spring-boot/issues/39843
        var callerClassLoader = Thread.currentThread().getContextClassLoader();

        for (var order : ordering) {
            var innerStart = Instant.now();
            var futures = runnables.stream()
                    .filter(runnable -> runnable.getOrder() == order)
                    .map(runnable -> CompletableFuture.supplyAsync(() -> {
                        var originalClassLoader = Thread.currentThread().getContextClassLoader();
                        Thread.currentThread().setContextClassLoader(callerClassLoader);
                        try {
                            runnable.run();
                            log.info("Warmup operation {} succeeded.", runnable.getName());
                            return true;
                        } catch (Exception ex) {
                            // Swallow the exception.
                            log.error("Warmup operation {} failed.", runnable.getName(), ex);
                            return false;
                        } finally {
                            Thread.currentThread().setContextClassLoader(originalClassLoader);
                        }
                    }))
                    .toArray(CompletableFuture<?>[]::new);
            CompletableFuture.allOf(futures).join();

            var successCount = Stream.of(futures)
                    .map(future -> (Boolean) future.join())
                    .filter(b -> b)
                    .count();
            totalSuccesses += successCount;
            total += futures.length;

            var innerElapsed = Duration.between(innerStart, Instant.now());
            log.info(
                    "Order #{} warmup completed with {} / {} tasks succeeding in {}",
                    order,
                    successCount,
                    futures.length,
                    innerElapsed);
        }

        this.setWarmingComplete(true);

        var elapsed = Duration.between(start, Instant.now());

        log.info(
                "{} warmup completed {} / {} tasks in {}",
                getClass().getName(),
                totalSuccesses,
                total,
                elapsed);
    }

    // Implement in the concrete class. Exceptions are caught per parallel task.
    public abstract List<WarmupRunnable> createWarmupTasks();

    @RequiredArgsConstructor
    public static class WarmupRunnable implements Runnable {
        @Getter
        private final String name;

        @Getter
        private final int order;

        private final Runnable runnable;

        @Override
        public void run() {
            this.runnable.run();
        }
    }
}
