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
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.RUNNING;
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
import java.util.UUID;
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
public class MonitorWorkerHeartbeatRoutine {
    private static final String MESG_NO_HEARTBEAT_RECEIVED =
            "Task id '{}': Status RUNNING; Last heartbeat received more than 4 minutes ago at '{}'";
    private static final String MESG_TASK_HEALTH =
            "Task id '{}': {}";
    private static final String MESG_TASK_INFO =
            "Task id '{}': {}";
    private static final String MESG_ERROR_PROCESSING_TASK_SLICE =
            "Error processing Task slice: {}";
    private static final String MESG_FINISHED_ROUTINE =
            "Finished MonitorWorkerHeartbeatRoutine in {} ms. Processed {} Tasks";
    private static final String MESG_STARTED_ROUTINE =
            "Started MonitorWorkerHeartbeatRoutine";
    private static final String MESG_UNEXPECTED_EXCEPTION =
            "Unexpected exception: {}";
    private static final String MESG_CHECKING_TASK_EVENTS =
            "Task id '{}': Checking task events for terminal status";
    private static final String MESG_TERMINAL_STATUS_IN_LATEST_EVENT =
            "Task id '{}': Updated with terminal status '{}' from the latest event";
    private static final String MESG_NO_TERMINAL_STATUS_IN_LATEST_EVENT =
            "Task id '{}': No terminal status found in latest event";

    private static final String MESG_MISSING_ICMS_REQUEST_IDS =
            "Task id '%s': No corresponding ICMS request-id(s) found";
    private static final String MESG_ICMS_INSTANCE_DETAILS =
            "ICMS Request Id %s: Instance Id %s; Instance State %s; %s";
    private static final String MESG_HEALTH_INFO_UNAVAILABLE =
            "Health info is not available";
    private static final String UNKNOWN = "UNKNOWN";

    // If the current time is greater than timestamp in the lastHeartbeatAt field of a Task with
    // RUNNING status by this duration, then the Task is transitioned to ERRORED status.
    private static final Duration MAX_DURATION = Duration.ofMinutes(4);

    private final EventService eventService;
    private final TaskService taskService;
    private final IcmsClient icmsClient;
    private final TaskErrorMetricsService tasksErrorMetricsService;
    private final TasksRepository tasksRepository;
    private final Tracer tracer;

    // The setter can be used to enable/disable the routine during testing without
    // restarting the context and incurring delays.
    @VisibleForTesting
    @Setter
    private volatile boolean enabled;

    public MonitorWorkerHeartbeatRoutine(
            EventService eventService,
            TaskService taskService,
            IcmsClient icmsClient,
            TaskErrorMetricsService tasksErrorMetricsService,
            TasksRepository tasksRepository,
            Tracer tracer,
            @Value("${nvct.scheduled-routines.monitor-worker-heartbeat-routine.enabled:true}")
            boolean enabled) {
        this.eventService = eventService;
        this.taskService = taskService;
        this.icmsClient = icmsClient;
        this.tasksErrorMetricsService = tasksErrorMetricsService;
        this.tasksRepository = tasksRepository;
        this.tracer = tracer;
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
                .filter(this::hasNoRecentHeartbeatForRunningTask)
                .forEach(taskEntity -> {
                    try {
                        processRunningTasksWithNoRecentHeartbeat(taskEntity);
                    } catch (Exception ex) {
                        // Swallow exception, log, and continue.
                        log.error(MESG_ERROR_PROCESSING_TASK_SLICE, ex.getMessage(), ex);
                    }
                    processed.incrementAndGet();
                });
        return processed.get();
    }

    private void processRunningTasksWithNoRecentHeartbeat(TaskEntity taskEntity) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var lastHeartbeatAt = taskEntity.getLastHeartbeatAt();

        NvctUtils.addTagsToChildSpan(tracer,
                                     Map.of(SPAN_TAG_NCA_ID, ncaId,
                                            SPAN_TAG_TASK_ID, taskId),
                                     "process-worker-heartbeat-task");

        log.info(MESG_NO_HEARTBEAT_RECEIVED, taskId, lastHeartbeatAt);
        var instances = icmsClient.getAllInstancesByTaskId(ncaId, taskId);
        if (CollectionUtils.isEmpty(instances)) {
            processTaskWithMissingIcmsInstances(taskId);
            return;
        }

        var errorMessage = getErrorMessage(instances);
        transitionTaskToErrored(taskEntity, errorMessage);
    }

    private void transitionTaskToErrored(TaskEntity taskEntity, String errorMessage) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var healthDto = getHealthDto(taskEntity.getGpuSpec(), errorMessage);
        log.info(MESG_TASK_HEALTH, taskId, healthDto.error());

        // There may be multiple ICMS Requests associated with a Task, but we must
        // transition the Task to ERRORED state just once and also insert just one
        // event.
        taskService.updateTask(taskId, TaskStatus.ERRORED, healthDto);
        tasksErrorMetricsService.recordTaskError(ncaId);

        var error = healthDto.error();
        var mesg = STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR.formatted(RUNNING, ERRORED, error);
        log.info(MESG_TASK_INFO, taskId, mesg);
        eventService.insertEvent(ncaId, taskId, mesg);
    }

    private void processTaskWithMissingIcmsInstances(UUID taskId) {
        var mesg = MESG_MISSING_ICMS_REQUEST_IDS.formatted(taskId);
        log.info(mesg);

        // Check the latest Task event to see if there's a terminal status recorded.
        log.info(MESG_CHECKING_TASK_EVENTS, taskId);
        eventService.getTerminalStatusFromLatestEvent(taskId)
                .ifPresentOrElse(
                        terminalStatus -> {
                            // Update Task with the terminal status found in the latest event.
                            // Note that we will not be inserting a new entry in events_by_tasks
                            // table for this as it was already done earlier.
                            taskService.updateTask(taskId, terminalStatus);
                            log.info(MESG_TERMINAL_STATUS_IN_LATEST_EVENT,
                                     taskId, terminalStatus);
                        },
                        () -> log.info(MESG_NO_TERMINAL_STATUS_IN_LATEST_EVENT, taskId));
    }

    private String getErrorMessage(List<Instance> instances) {
        return instances.stream()
                .map(instance -> {
                    var requestId = isNotBlank(instance.getLaunchRequestId()) ?
                            instance.getLaunchRequestId() : UNKNOWN;
                    var instanceId = isNotBlank(instance.getInstanceId()) ?
                            instance.getInstanceId() : UNKNOWN;
                    var instanceState = instance.getState() != null ?
                            instance.getState().getName() : UNKNOWN;
                    var healthInfo = instance.getHealthInfo();
                    var healthMessage = healthInfo != null &&
                            isNotBlank(healthInfo.getErrorLog()) ?
                            "Error Message - " + healthInfo.getErrorLog() :
                            MESG_HEALTH_INFO_UNAVAILABLE;
                    return MESG_ICMS_INSTANCE_DETAILS.formatted(
                            requestId, instanceId, instanceState, healthMessage);
                })
                .collect(joining("; "));
    }

    private boolean hasNoRecentHeartbeatForRunningTask(TaskEntity taskEntity) {
        var lastHeartbeatAt = taskEntity.getLastHeartbeatAt();
        return taskEntity.getStatus() == RUNNING
                && (lastHeartbeatAt == null ||
                        lastHeartbeatAt.isBefore(Instant.now().minus(MAX_DURATION)));
    }

}
