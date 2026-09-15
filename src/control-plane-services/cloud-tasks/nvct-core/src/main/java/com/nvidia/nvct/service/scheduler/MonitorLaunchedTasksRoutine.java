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

import static com.nvidia.nvct.persistence.task.entity.TaskStatus.ERRORED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.LAUNCHED;
import static com.nvidia.nvct.service.event.EventService.STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR;
import static com.nvidia.nvct.service.scheduler.CommonRoutineService.getHealthDto;
import static com.nvidia.nvct.util.NvctConstants.BATCH_SIZE;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_NCA_ID;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_TASK_ID;
import static java.util.stream.Collectors.joining;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.service.event.EventService;
import com.nvidia.nvct.service.metrics.TaskErrorMetricsService;
import com.nvidia.nvct.service.icms.IcmsClient;
import com.nvidia.nvct.service.icms.IcmsStubService.Instance;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.tracing.Tracer;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RefreshScope
@ConditionalOnProperty(name = "nvct.scheduled-routines.enabled",
                       havingValue = "true",
                       matchIfMissing = true)
public class MonitorLaunchedTasksRoutine {

    private static final String MESG_NOT_UPDATED_RECENTLY =
            "Task id '{}': Status LAUNCHED - last updated more than 5 minutes ago at '{}'";
    private static final String MESG_TASK_HEALTH =
            "Task id '{}': {}";
    private static final String MESG_TASK_INFO =
            "Task id '{}': {}";
    private static final String MESG_ERROR_PROCESSING_TASK_SLICE =
            "Error processing Task slice: {}";
    private static final String MESG_FINISHED_ROUTINE =
            "Finished MonitorLaunchedTasksRoutine in {} ms. Processed {} Tasks";
    private static final String MESG_STARTED_ROUTINE =
            "Started MonitorLaunchedTasksRoutine";
    private static final String MESG_UNEXPECTED_EXCEPTION =
            "Unexpected exception: {}";

    private static final String MISSING_ICMS_INSTANCES =
            "No corresponding ICMS instances found";
    private static final String NO_HEALTH_INFORMATION_AVAILABLE =
            "No health information available";

    // If the current time is greater than timestamp in the lastUpdatedAt field of a Task with
    // LAUNCHED status by this duration, then the Task is transitioned to ERRORED status.
    private static final Duration MAX_DURATION = Duration.ofMinutes(5);

    private final EventService eventService;
    private final IcmsClient icmsClient;
    private final TaskService taskService;
    private final TaskErrorMetricsService taskErrorMetricsService;
    private final TasksRepository tasksRepository;
    private final Tracer tracer;

    // The setter can be used to enable/disable the routine during testing without
    // restarting the context and incurring delays.
    @VisibleForTesting
    @Setter
    private volatile boolean enabled;

    public MonitorLaunchedTasksRoutine(
            EventService eventService,
            IcmsClient icmsClient,
            TaskService taskService,
            TaskErrorMetricsService taskErrorMetricsService,
            TasksRepository tasksRepository,
            Tracer tracer,
            @Value("${nvct.scheduled-routines.monitor-launched-tasks-routine.enabled:true}")
            boolean enabled) {
        this.eventService = eventService;
        this.icmsClient = icmsClient;
        this.taskService = taskService;
        this.taskErrorMetricsService = taskErrorMetricsService;
        this.tasksRepository = tasksRepository;
        this.tracer = tracer;
        this.enabled = enabled;
    }

    public void run() {
        if (!enabled) {
            return;
        }

        runUnchecked(Instant.now());
    }

    @VisibleForTesting
    public void runUnchecked(Instant now) {
        var totalProcessed = 0;
        var start = System.currentTimeMillis();

        log.info(MESG_STARTED_ROUTINE);

        try {
            var slice = tasksRepository.findAllBy(Pageable.ofSize(BATCH_SIZE).first());
            do {
                totalProcessed += processSlice(slice, now);
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

    private int processSlice(Slice<TaskEntity> slice, Instant now) {
        var processed = new AtomicInteger(0);

        slice.stream()
                .filter(task -> task.getStatus() == LAUNCHED)
                .forEach(taskEntity -> {
                    try {
                        processLaunchedTask(taskEntity, now);
                    } catch (Exception ex) {
                        // Swallow exception, log, and continue.
                        log.error(MESG_ERROR_PROCESSING_TASK_SLICE, ex.getMessage(), ex);
                    }
                    processed.incrementAndGet();
                });
        return processed.get();
    }

    private void processLaunchedTask(TaskEntity taskEntity, Instant now) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var lastUpdatedAt = taskEntity.getLastUpdatedAt();

        NvctUtils.addTagsToChildSpan(tracer,
                                     Map.of(SPAN_TAG_NCA_ID, ncaId,
                                            SPAN_TAG_TASK_ID, taskId),
                                     "process-launched-task");

        if (lastUpdatedAt != null &&
                lastUpdatedAt.isBefore(now.minus(MAX_DURATION))) {
            log.info(MESG_NOT_UPDATED_RECENTLY, taskId, lastUpdatedAt);
            var instances = icmsClient.getAllInstancesByTaskId(ncaId, taskId);
            if (CollectionUtils.isEmpty(instances)) {
                transitionToErroredWhenNoIcmsRequestsFound(taskEntity);
                return;
            }

            transitionToErrored(taskEntity, getInstanceErrorMessage(instances));
        }
    }

    private void transitionToErrored(TaskEntity taskEntity, String errorMessage) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        log.info(MESG_TASK_HEALTH, taskId, errorMessage);

        taskService.updateTask(taskId,
                               TaskStatus.ERRORED,
                               getHealthDto(taskEntity.getGpuSpec(), errorMessage));
        taskErrorMetricsService.recordTaskError(ncaId);
        var mesg = STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR
                .formatted(LAUNCHED, ERRORED, errorMessage);
        log.info(MESG_TASK_INFO, taskId, mesg);
        eventService.insertEvent(ncaId, taskId, mesg);
    }

    private static String getInstanceErrorMessage(List<Instance> instances) {
        var errorMessage = instances.stream()
                .map(Instance::getHealthInfo)
                .filter(healthInfo -> healthInfo != null &&
                        isNotBlank(healthInfo.getErrorLog()))
                .map(healthInfo -> healthInfo.getErrorLog())
                .collect(joining("; "));
        return isNotBlank(errorMessage) ? errorMessage : NO_HEALTH_INFORMATION_AVAILABLE;
    }

    // Transitions the Task to ERRORED status when there are no corresponding ICMS Request Id(s).
    // This should not happen. We saw this when ICMS was not hooked up to NVCT. So, keeping this
    // for completeness.
    private void transitionToErroredWhenNoIcmsRequestsFound(TaskEntity task) {
        var gpuSpec = task.getGpuSpec();
        var taskId = task.getTaskId();
        var ncaId = task.getNcaId();
        var healthDto = getHealthDto(gpuSpec, MISSING_ICMS_INSTANCES);
        log.info(MESG_TASK_HEALTH, taskId, MISSING_ICMS_INSTANCES);

        taskService.updateTask(taskId, TaskStatus.ERRORED, healthDto);
        taskErrorMetricsService.recordTaskError(ncaId);
        var mesg = STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR
                        .formatted(LAUNCHED, ERRORED, MISSING_ICMS_INSTANCES);
        log.info(MESG_TASK_INFO, taskId, mesg);
        eventService.insertEvent(ncaId, taskId, mesg);
    }

}
