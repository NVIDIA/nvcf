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

import static com.nvidia.nvct.util.NvctConstants.BATCH_SIZE;
import static com.nvidia.nvct.util.NvctConstants.TERMINAL_TASK_STATUSES;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.service.ess.EssService;
import com.nvidia.nvct.service.event.EventService;
import com.nvidia.nvct.service.result.ResultService;
import com.nvidia.nvct.service.icms.IcmsService;
import com.nvidia.nvct.service.task.TaskService;
import java.time.Duration;
import java.time.Instant;
import java.util.concurrent.atomic.AtomicInteger;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
@ConditionalOnProperty(name = "nvct.scheduled-routines.enabled",
                       havingValue = "true",
                       matchIfMissing = true)
public class CleanTerminalTasksRoutine {

    private static final String MESG_TASK_CLEANING =
            "Task id '{}', status '{}', last heartbeat at '{}': Cleaning related events, result" +
                    "metadata, ICMS requests, secrets, and definition";
    private static final String MESG_TASK_CLEANED =
            "Task id '{}', status '{}', last heartbeat '{}': Completed cleaning";
    private static final String MESG_ERROR_PROCESSING_TASK_SLICE =
            "Error processing Task slice: {}";
    private static final String MESG_FINISHED_ROUTINE =
            "Finished CleanTerminalTasksRoutine in {} ms. Processed {} Tasks";
    private static final String MESG_STARTED_ROUTINE =
            "Started CleanTerminalTasksRoutine";
    private static final String MESG_UNEXPECTED_EXCEPTION =
            "Unexpected exception: {}";

    private static final String CONFIG_TERMINAL_TASK_RETENTION_DURATION =
            "nvct.scheduled-routines.clean-terminal-tasks-routine.retention-duration";
    private static final String CONFIG_ROUTINE_ENABLED =
            "nvct.scheduled-routines.clean-terminal-tasks-routine.enabled";

    private final IcmsService icmsService;
    private final EventService eventService;
    private final ResultService resultService;
    private final TaskService taskService;
    private final EssService essService;
    private final Duration retentionDuration;
    private final TasksRepository tasksRepository;

    // The setter can be used to enable/disable the routine during testing without
    // restarting the context and incurring delays.
    @VisibleForTesting
    @Setter
    private volatile boolean enabled;

    public CleanTerminalTasksRoutine(
            EssService essService,
            IcmsService icmsService,
            EventService eventService,
            ResultService resultService,
            TaskService taskService,
            TasksRepository tasksRepository,
            @Value("${" + CONFIG_TERMINAL_TASK_RETENTION_DURATION + ":P7D}")
            Duration retentionDuration,
            @Value("${" + CONFIG_ROUTINE_ENABLED + ":true}")
            boolean enabled) {
        this.essService = essService;
        this.icmsService = icmsService;
        this.eventService = eventService;
        this.resultService = resultService;
        this.taskService = taskService;
        this.tasksRepository = tasksRepository;
        this.retentionDuration = retentionDuration;
        this.enabled = enabled;
    }

    public void run() {
        if (!enabled) {
            return;
        }

        runUnchecked();
    }

    @VisibleForTesting
    public void runUnchecked() {
        var totalProcessed = 0;
        var start = System.currentTimeMillis();

        log.info(MESG_STARTED_ROUTINE);

        try {
            var slice = tasksRepository.findAllBy(Pageable.ofSize(BATCH_SIZE).first());
            do {
                totalProcessed += processSlice(slice);
                if (!slice.hasNext()) {
                    break;
                }
                slice = tasksRepository.findAllBy(slice.nextPageable());
            } while (true);
        } catch (Exception e) {
            // Swallow and move on as async routines should process all the Tasks.
            log.error(MESG_UNEXPECTED_EXCEPTION, e.getMessage(), e);
        }

        log.info(MESG_FINISHED_ROUTINE, System.currentTimeMillis() - start, totalProcessed);
    }

    private int processSlice(Slice<TaskEntity> slice) {
        var processed = new AtomicInteger(0);

        slice.stream()
                .filter(CleanTerminalTasksRoutine::hasTerminalStatus)
                .filter(this::hasRetentionPeriodElapsed)
                .forEach(taskEntity -> {
                    try {
                        cleanUpTask(taskEntity);
                    } catch (Exception ex) {
                        // Swallow exception, log, and continue.
                        log.error(MESG_ERROR_PROCESSING_TASK_SLICE, ex.getMessage(), ex);
                    }
                    processed.incrementAndGet();
                });
        return processed.get();
    }

    private static boolean hasTerminalStatus(TaskEntity taskEntity) {
        return TERMINAL_TASK_STATUSES.contains(taskEntity.getStatus());
    }

    private boolean hasRetentionPeriodElapsed(TaskEntity taskEntity) {
        var lastHeartbeat = taskEntity.getLastHeartbeatAt();
        var instant = lastHeartbeat;

        if (lastHeartbeat == null) {
            // If lastHeartbeatAt is null, then fall back to lastUpdatedAt. If lastUpdatedAt
            // is null, then use createdAt as the last option.
            instant = taskEntity.getLastUpdatedAt() != null ? taskEntity.getLastUpdatedAt() :
                    taskEntity.getCreatedAt();
        }
        return instant.isBefore(Instant.now().minus(retentionDuration));
    }

    private void cleanUpTask(TaskEntity taskEntity) {
        var taskId = taskEntity.getTaskId();
        var status = taskEntity.getStatus();
        var lastHeartbeatAt = taskEntity.getLastHeartbeatAt();

        log.info(MESG_TASK_CLEANING, taskId, status, lastHeartbeatAt);
        eventService.cleanEvents(taskId);
        resultService.cleanResults(taskId);
        icmsService.terminateInstanceByTaskId(taskEntity.getNcaId(), taskId);
        taskService.deleteTask(taskId);
        essService.deleteSecretsPath(taskId);
        log.info(MESG_TASK_CLEANED, taskId, status, lastHeartbeatAt);
    }
}
