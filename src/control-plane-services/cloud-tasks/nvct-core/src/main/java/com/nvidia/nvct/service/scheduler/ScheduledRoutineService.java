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
package com.nvidia.nvct.service.scheduler;

import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nonnull;
import java.util.concurrent.CountDownLatch;
import lombok.extern.slf4j.Slf4j;
import net.javacrumbs.shedlock.core.LockAssert;
import net.javacrumbs.shedlock.spring.annotation.SchedulerLock;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.ApplicationListener;
import org.springframework.scheduling.annotation.EnableScheduling;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
@ConditionalOnProperty(name = "nvct.scheduled-routines.enabled",
        havingValue = "true",
        matchIfMissing = true)
@EnableScheduling
public class ScheduledRoutineService implements ApplicationListener<ApplicationReadyEvent> {

    private static final String CLEAN_TERMINAL_TASKS_ROUTINE =
            "clean-terminal-tasks-routine";
    private static final String MONITOR_LAUNCHED_TASKS_ROUTINE =
            "monitor-launched-tasks-routine";
    private static final String MONITOR_QUEUED_TASKS_ROUTINE =
            "monitor-queued-tasks-routine";
    private static final String MONITOR_WORKER_HEARTBEAT_ROUTINE =
            "monitor-worker-heartbeat-routine";
    private final CountDownLatch initialised;
    private final MonitorQueuedTasksRoutine monitorQueuedTasksRoutine;
    private final MonitorLaunchedTasksRoutine monitorLaunchedTasksRoutine;
    private final CleanTerminalTasksRoutine cleanTerminalTasksRoutine;
    private final MonitorWorkerHeartbeatRoutine monitorWorkerHeartbeatRoutine;

    public ScheduledRoutineService(
            MonitorQueuedTasksRoutine monitorQueuedTasksRoutine,
            MonitorLaunchedTasksRoutine monitorLaunchedTasksRoutine,
            CleanTerminalTasksRoutine cleanTerminalTasksRoutine,
            MonitorWorkerHeartbeatRoutine monitorWorkerHeartbeatRoutine) {
        this.monitorQueuedTasksRoutine = monitorQueuedTasksRoutine;
        this.monitorLaunchedTasksRoutine = monitorLaunchedTasksRoutine;
        this.cleanTerminalTasksRoutine = cleanTerminalTasksRoutine;
        this.monitorWorkerHeartbeatRoutine = monitorWorkerHeartbeatRoutine;
        this.initialised = new CountDownLatch(1);
    }

    @Override
    public void onApplicationEvent(@Nonnull ApplicationReadyEvent event) {
        initialised.countDown();
    }

    @SchedulerLock(name = MONITOR_QUEUED_TASKS_ROUTINE,
            lockAtLeastFor = "PT5M",
            lockAtMostFor = "PT5M")
    @Scheduled(fixedDelayString = "PT5M")
    @Observed(name = "monitor-queued-tasks")
    void monitorQueuedTasks() throws InterruptedException {
        initialised.await();
        LockAssert.assertLocked();
        log.debug("Begin Routine: " + MONITOR_QUEUED_TASKS_ROUTINE);
        monitorQueuedTasksRoutine.run();
        log.debug("End Routine: " + MONITOR_QUEUED_TASKS_ROUTINE);
    }

    @SchedulerLock(name = MONITOR_LAUNCHED_TASKS_ROUTINE,
            lockAtLeastFor = "PT8M",
            lockAtMostFor = "PT8M")
    @Scheduled(fixedDelayString = "PT8M")
    @Observed(name = "monitor-launched-tasks")
    void monitorLaunchedTasks() throws InterruptedException {
        initialised.await();
        LockAssert.assertLocked();
        log.debug("Begin Routine: " + MONITOR_LAUNCHED_TASKS_ROUTINE);
        monitorLaunchedTasksRoutine.run();
        log.debug("End Routine: " + MONITOR_LAUNCHED_TASKS_ROUTINE);
    }

    @SchedulerLock(name = CLEAN_TERMINAL_TASKS_ROUTINE,
            lockAtLeastFor = "PT14M",
            lockAtMostFor = "PT14M")
    @Scheduled(fixedDelayString = "PT14M")
    @Observed(name = "clean-up-terminal-tasks")
    void cleanUpTerminalTasks() throws InterruptedException {
        initialised.await();
        LockAssert.assertLocked();
        log.debug("Begin Routine: " + CLEAN_TERMINAL_TASKS_ROUTINE);
        cleanTerminalTasksRoutine.run();
        log.debug("End Routine: " + CLEAN_TERMINAL_TASKS_ROUTINE);
    }

    @SchedulerLock(name = MONITOR_WORKER_HEARTBEAT_ROUTINE,
            lockAtLeastFor = "PT3M",
            lockAtMostFor = "PT3M")
    @Scheduled(fixedDelayString = "PT3M")
    @Observed(name = "monitor-worker-heartbeat")
    void monitorWorkerHeartbeat() throws InterruptedException {
        initialised.await();
        LockAssert.assertLocked();
        log.debug("Begin Routine: " + MONITOR_WORKER_HEARTBEAT_ROUTINE);
        monitorWorkerHeartbeatRoutine.run();
        log.debug("End Routine: " + MONITOR_WORKER_HEARTBEAT_ROUTINE);
    }

}
