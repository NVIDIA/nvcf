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

import com.nvidia.apikeys.config.NakProperties;
import jakarta.annotation.PreDestroy;
import java.time.Clock;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.atomic.AtomicReference;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.context.event.EventListener;
import org.springframework.scheduling.TaskScheduler;
import org.springframework.scheduling.concurrent.ThreadPoolTaskScheduler;
import org.springframework.stereotype.Component;

/**
 * Schedules {@link LegacyKeyMigrationService} with a fixed delay between runs. Stops scheduling
 * on this replica after a clean run.
 *
 * <p>Runs on its own single thread. Shutdown interrupts a run in progress, which releases the
 * maintenance lease before the replica exits.
 */
@Slf4j
@Component
@ConditionalOnProperty(prefix = "apikeys.migration.legacy-keys", name = "enabled", havingValue = "true")
public class LegacyKeyMigrationTask {

    private final TaskScheduler taskScheduler;
    private final LegacyKeyMigrationService migrationService;
    private final LegacyKeyMigrationMetrics metrics;
    private final NakProperties nakProperties;
    private final Clock clock;
    private final AtomicReference<ScheduledFuture<?>> schedule = new AtomicReference<>();
    private volatile boolean stopped;

    @Autowired
    public LegacyKeyMigrationTask(
            LegacyKeyMigrationService migrationService, LegacyKeyMigrationMetrics metrics,
            NakProperties nakProperties, Clock clock) {
        this(newScheduler(), migrationService, metrics, nakProperties, clock);
    }

    LegacyKeyMigrationTask(
            TaskScheduler taskScheduler, LegacyKeyMigrationService migrationService,
            LegacyKeyMigrationMetrics metrics, NakProperties nakProperties, Clock clock) {
        this.taskScheduler = taskScheduler;
        this.migrationService = migrationService;
        this.metrics = metrics;
        this.nakProperties = nakProperties;
        this.clock = clock;
    }

    @EventListener(ApplicationReadyEvent.class)
    public void start() {
        if (schedule.get() != null) {
            return;
        }
        var settings = nakProperties.getMigration().getLegacyKeys();
        schedule.set(taskScheduler.scheduleWithFixedDelay(
                this::runOnce, clock.instant().plus(settings.getInitialDelay()),
                settings.getFixedDelay()));
        log.info("Legacy key migration scheduled, ncaId={}, initialDelay={}, fixedDelay={}",
                 nakProperties.getNcaId(), settings.getInitialDelay(), settings.getFixedDelay());
    }

    @PreDestroy
    public void stop() {
        if (taskScheduler instanceof ThreadPoolTaskScheduler scheduler) {
            scheduler.shutdown();
        }
    }

    void runOnce() {
        if (stopped) {
            return;
        }
        var result = migrationService.run();
        if (result.isClean()) {
            stopped = true;
            var future = schedule.get();
            if (future != null) {
                future.cancel(false);
            }
            metrics.markCompleted();
            log.info("Legacy key migration found nothing left to copy and stopped, ncaId={}",
                     nakProperties.getNcaId());
        }
    }

    private static ThreadPoolTaskScheduler newScheduler() {
        var scheduler = new ThreadPoolTaskScheduler();
        scheduler.setPoolSize(1);
        scheduler.setThreadNamePrefix("legacy-key-migration-");
        scheduler.setWaitForTasksToCompleteOnShutdown(false);
        scheduler.initialize();
        return scheduler;
    }
}
