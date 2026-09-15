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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.Test;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.health.contributor.Status;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.context.ConfigurableApplicationContext;

class BootWarmupBaseTest {

    @Test
    void healthIsUpWhenNotBlockingUntilComplete() {
        var warmup = new TestWarmup(false, List.of());

        assertThat(warmup.health().getStatus()).isEqualTo(Status.UP);
    }

    @Test
    void healthIsDownWhenBlockingAndWarmupNotStarted() {
        var warmup = new TestWarmup(true, List.of());

        assertThat(warmup.health().getStatus()).isEqualTo(Status.DOWN);
    }

    @Test
    void healthBecomesUpAfterApplicationReadyRunsWarmup() {
        var warmup = new TestWarmup(true, List.of());
        warmup.onApplicationEvent(applicationReadyEvent());

        assertThat(warmup.health().getStatus()).isEqualTo(Status.UP);
        assertThat(warmup.isWarmingComplete()).isTrue();
    }

    @Test
    void laterOrderRunsAfterEarlierOrderCompletes() {
        var phase0 = new AtomicInteger(0);
        var tasks = List.of(
                new BootWarmupBase.WarmupRunnable("p0-a", 0, () -> phase0.incrementAndGet()),
                new BootWarmupBase.WarmupRunnable("p0-b", 0, () -> phase0.incrementAndGet()),
                new BootWarmupBase.WarmupRunnable(
                        "p1", 1, () -> assertThat(phase0.get()).isEqualTo(2)));

        var warmup = new TestWarmup(false, tasks);
        warmup.onApplicationEvent(applicationReadyEvent());

        assertThat(phase0.get()).isEqualTo(2);
    }

    @Test
    void failedTaskStillCompletesWarmup() {
        var tasks = List.of(new BootWarmupBase.WarmupRunnable("fails", 0, () -> {
            throw new IllegalStateException("boom");
        }));
        var warmup = new TestWarmup(true, tasks);
        warmup.onApplicationEvent(applicationReadyEvent());

        assertThat(warmup.isWarmingComplete()).isTrue();
        assertThat(warmup.health().getStatus()).isEqualTo(Status.UP);
    }

    @Test
    void healthRevertsToUpAfterTimeoutWhenBlockingAndWarmupNeverFinishes() throws Exception {
        var warmup = new NeverCompletesWarmup(true);
        Thread.sleep(20);

        assertThat(warmup.health().getStatus()).isEqualTo(Status.UP);
    }

    @Test
    void warmupTaskSeesCallerContextClassLoader() {
        var customClassLoader = new ClassLoader(getClass().getClassLoader()) {};
        var observed = new AtomicReference<ClassLoader>();

        var tasks = List.of(new BootWarmupBase.WarmupRunnable(
                "cl-check", 0, () -> observed.set(Thread.currentThread().getContextClassLoader())));
        var warmup = new TestWarmup(false, tasks);

        Thread.currentThread().setContextClassLoader(customClassLoader);
        try {
            warmup.onApplicationEvent(applicationReadyEvent());
        } finally {
            Thread.currentThread().setContextClassLoader(getClass().getClassLoader());
        }

        assertThat(observed.get())
                .as("ForkJoinPool worker should see the caller's context classloader")
                .isSameAs(customClassLoader);
    }

    @Test
    void warmupRunnableDelegatesToRunnable() {
        var ran = new boolean[] {false};
        var wr = new BootWarmupBase.WarmupRunnable("x", 0, () -> ran[0] = true);
        assertThat(wr.getName()).isEqualTo("x");
        assertThat(wr.getOrder()).isZero();
        wr.run();
        assertThat(ran[0]).isTrue();
    }

    private static ApplicationReadyEvent applicationReadyEvent() {
        SpringApplication app = mock(SpringApplication.class);
        ConfigurableApplicationContext ctx = mock(ConfigurableApplicationContext.class);
        return new ApplicationReadyEvent(app, new String[0], ctx, Duration.ZERO);
    }

    private static final class TestWarmup extends BootWarmupBase {
        private final List<BootWarmupBase.WarmupRunnable> tasks;

        TestWarmup(boolean block, List<BootWarmupBase.WarmupRunnable> tasks) {
            super(block);
            this.tasks = tasks;
        }

        @Override
        public List<BootWarmupBase.WarmupRunnable> createWarmupTasks() {
            return tasks;
        }
    }

    /** Blocks readiness until timeout path in {@link BootWarmupBase#health()} runs. */
    private static final class NeverCompletesWarmup extends BootWarmupBase {
        NeverCompletesWarmup(boolean block) {
            super(block, Duration.ofMillis(1));
        }

        @Override
        public List<BootWarmupBase.WarmupRunnable> createWarmupTasks() {
            return new ArrayList<>();
        }

        @Override
        public void onApplicationEvent(ApplicationReadyEvent event) {
            // skip — simulates warmup never running / never completing
        }
    }
}
