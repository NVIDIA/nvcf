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

import static com.nvidia.apikeys.TestData.TEST_TIME;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.MigrationProperties;
import com.nvidia.apikeys.config.NakProperties;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Map;
import java.util.concurrent.ScheduledFuture;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.scheduling.TaskScheduler;

@ExtendWith(MockitoExtension.class)
class LegacyKeyMigrationTaskTest {

    private static final RunResult CLEAN =
            new RunResult(RunOutcome.COMPLETED, Map.of(KeyResult.ALREADY_MIGRATED, 3));
    private static final RunResult MIGRATED =
            new RunResult(RunOutcome.COMPLETED, Map.of(KeyResult.MIGRATED, 1));

    @Mock
    private TaskScheduler taskScheduler;
    @Mock
    private LegacyKeyMigrationService migrationService;

    private final ScheduledFuture<?> future = mock(ScheduledFuture.class);
    private SimpleMeterRegistry registry;
    private MigrationProperties.LegacyKeys settings;
    private LegacyKeyMigrationTask task;

    @BeforeEach
    void setUp() {
        var migration = new MigrationProperties();
        settings = migration.getLegacyKeys();
        registry = new SimpleMeterRegistry();
        task = new LegacyKeyMigrationTask(
                taskScheduler, migrationService, new LegacyKeyMigrationMetrics(registry),
                NakProperties.builder().ncaId("legacy-nca").migration(migration).build(),
                Clock.fixed(TEST_TIME, ZoneOffset.UTC));
    }

    @Test
    void schedulesWithConfiguredDelays() {
        var runnable = start();

        assertThat(runnable).isNotNull();
        verify(taskScheduler).scheduleWithFixedDelay(
                any(Runnable.class), eq(TEST_TIME.plus(settings.getInitialDelay())),
                eq(settings.getFixedDelay()));
    }

    @Test
    void cleanRunCancelsTheSchedule() {
        var runnable = start();
        when(migrationService.run()).thenReturn(CLEAN);

        runnable.run();
        runnable.run();

        verify(future).cancel(false);
        verify(migrationService, times(1)).run();
        assertThat(registry.get(LegacyKeyMigrationMetrics.COMPLETED).gauge().value()).isEqualTo(1);
    }

    @Test
    void alreadyCompletedRunCancelsTheSchedule() {
        var runnable = start();
        when(migrationService.run()).thenReturn(new RunResult(RunOutcome.ALREADY_COMPLETED, Map.of()));

        runnable.run();

        verify(future).cancel(false);
        assertThat(registry.get(LegacyKeyMigrationMetrics.COMPLETED).gauge().value()).isEqualTo(1);
    }

    @Test
    void runWithWorkKeepsTheSchedule() {
        var runnable = start();
        when(migrationService.run()).thenReturn(MIGRATED);

        runnable.run();

        verify(future, never()).cancel(anyBoolean());
        assertThat(registry.get(LegacyKeyMigrationMetrics.COMPLETED).gauge().value()).isZero();
    }

    @Test
    void startIsIdempotent() {
        start();

        task.start();

        verify(taskScheduler, times(1))
                .scheduleWithFixedDelay(any(Runnable.class), any(Instant.class), any(Duration.class));
    }

    private Runnable start() {
        var captor = ArgumentCaptor.forClass(Runnable.class);
        doReturn(future).when(taskScheduler)
                .scheduleWithFixedDelay(captor.capture(), any(Instant.class), any(Duration.class));
        task.start();
        return captor.getValue();
    }
}
