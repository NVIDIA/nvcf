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
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.EXCEEDED_MAX_QUEUED_DURATION;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.QUEUED;
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
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest;
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
public class MonitorQueuedTasksRoutine {

    private static final String MESG_ICMS_REQUESTS_CAN_PRODUCE =
            "Task id '{}': ICMS Requests can still produce instance so stays in QUEUED state";
    private static final String MESG_TASK_HEALTH =
            "Task id '{}': {}";
    private static final String MESG_TASK_INFO =
            "Task id '{}': {}";
    private static final String MESG_ERROR_PROCESSING_TASK_SLICE =
            "Error processing Task slice: {}";
    private static final String MESG_FINISHED_ROUTINE =
            "Finished MonitorQueuedTasksRoutine in {} ms. Processed {} Tasks";
    private static final String MESG_STARTED_ROUTINE =
            "Started MonitorQueuedTasksRoutine";
    private static final String MESG_UNEXPECTED_EXCEPTION =
            "Unexpected exception: {}";

    private static final String MISSING_ICMS_REQUEST_IDS =
            "No corresponding ICMS request-id(s) found";
    private static final String NO_HEALTH_INFORMATION_AVAILABLE =
            "No health information available";

    // Extra time given after maxQueuedDuration before transitioning the Task to
    // EXCEEDED_MAX_QUEUED_DURATION status. Extra time is being given to avoid race with ICMS as
    // it updates the health information that NVCT can then use to update its DB. This will help
    // users to see the error message percolating from the Worker -> ICMS -> NVCT.
    private static final Duration DELTA_DURATION = Duration.ofMinutes(3);

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

    public MonitorQueuedTasksRoutine(
            EventService eventService,
            IcmsClient icmsClient,
            TaskService taskService,
            TaskErrorMetricsService taskErrorMetricsService,
            TasksRepository tasksRepository,
            Tracer tracer,
            @Value("${nvct.scheduled-routines.monitor-queued-tasks-routine.enabled:true}")
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
                .filter(task -> task.getStatus() == QUEUED)
                .forEach(taskEntity -> {
                    try {
                        processQueuedTask(taskEntity);
                    } catch (Exception ex) {
                        // Swallow exception, log, and continue.
                        log.error(MESG_ERROR_PROCESSING_TASK_SLICE, ex.getMessage(), ex);
                    }
                    processed.incrementAndGet();
                });
        return processed.get();
    }

    private void processQueuedTask(TaskEntity taskEntity) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var createdAt = taskEntity.getCreatedAt();

        NvctUtils.addTagsToChildSpan(tracer,
                                     Map.of(SPAN_TAG_NCA_ID, ncaId,
                                            SPAN_TAG_TASK_ID, taskId),
                                     "process-queued-task");

        var instances = icmsClient.getAllInstancesByTaskId(ncaId, taskId);

        if (CollectionUtils.isEmpty(instances)) {
            if (Instant.now().isAfter(createdAt.plus(Duration.ofMinutes(2)))) {
                // If the Task is created more than two minutes ago and still there is no
                // corresponding ICMS instances, then transition to ERRORED.
                transitionToErroredWhenNoIcmsRequestsFound(taskEntity);
            }
            return;
        }

        // Transition to EXCEEDED_MAX_QUEUED_DURATION if the Task has been in QUEUED status
        // for more than the specified maxQueuedDuration. Use health info from ICMS if
        // available.
        var haveAllInstancesExceededMaxQueuedDuration = instances.stream()
                .allMatch(instance -> hasExceededMaxQueuedDurationPlusDelta(instance, taskEntity));
        if (haveAllInstancesExceededMaxQueuedDuration) {
            log.info("Task id '{}': Transition to EXCEEDED_MAX_QUEUED_DURATION", taskId);
            transitionToExceededMaxQueuedDuration(taskEntity, instances);
            return;
        }

        // Transition to ERRORED if all the instances are in terminal state and has health info.
        // This can happen in scenarios when there is no capacity or Task Container exited
        // ungracefully during startup. These things can happen even before maxQueuedDuration
        // has elapsed.
        if (areAllInstancesInTerminalStateByTaskId(instances)) {
            log.info("Task id '{}': Transition to ERRORED as all instances are in terminal state",
                     taskId);
            transitionToErrored(taskEntity, instances);
            return;
        }

        // The ICMS still can produce active instances and maxQueuedDuration has not elapsed.
        log.info(MESG_ICMS_REQUESTS_CAN_PRODUCE, taskId);
    }

    // Transitions the Task to ERRORED status when there are no corresponding ICMS Request Id(s).
    // This should not happen. We saw this when ICMS was not hooked up to NVCT. So, keeping this
    // for completeness.
    private void transitionToErroredWhenNoIcmsRequestsFound(TaskEntity taskEntity) {
        var taskId = taskEntity.getTaskId();
        var ncaId = taskEntity.getNcaId();
        var gpuSpec = taskEntity.getGpuSpec();
        var healthDto = getHealthDto(gpuSpec, MISSING_ICMS_REQUEST_IDS);
        log.info(MESG_TASK_HEALTH, taskId, MISSING_ICMS_REQUEST_IDS);

        taskService.updateTask(taskId, TaskStatus.ERRORED, healthDto);
        taskErrorMetricsService.recordTaskError(ncaId);
        var mesg = STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR
                        .formatted(QUEUED, ERRORED, MISSING_ICMS_REQUEST_IDS);
        log.info(MESG_TASK_INFO, taskId, mesg);
        eventService.insertEvent(ncaId, taskId, mesg);
    }

    private void transitionToExceededMaxQueuedDuration(
            TaskEntity taskEntity,
            List<Instance> instances) {
        var taskId = taskEntity.getTaskId();
        var errorMessage = getInstanceErrorMessage(instances);
        var healthDto = getHealthDto(taskEntity.getGpuSpec(), errorMessage);
        log.info(MESG_TASK_HEALTH, taskId, healthDto.error());

        var ncaId = taskEntity.getNcaId();
        taskService.updateTask(taskId, TaskStatus.EXCEEDED_MAX_QUEUED_DURATION, healthDto);
        taskErrorMetricsService.recordTaskError(ncaId);
        var mesg = STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR
                .formatted(QUEUED, EXCEEDED_MAX_QUEUED_DURATION, errorMessage);
        log.info(MESG_TASK_INFO, taskId, mesg);
        eventService.insertEvent(ncaId, taskId, mesg);
    }

    private void transitionToErrored(
            TaskEntity taskEntity,
            List<Instance> instances) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var errorMessage = getInstanceErrorMessage(instances);
        log.info(MESG_TASK_HEALTH, taskId, errorMessage);

        taskService.updateTask(taskId,
                               TaskStatus.ERRORED,
                               getHealthDto(taskEntity.getGpuSpec(), errorMessage));
        taskErrorMetricsService.recordTaskError(ncaId);
        var mesg = STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR
                .formatted(QUEUED, ERRORED, errorMessage);
        log.info(MESG_TASK_INFO, taskId, mesg);
        eventService.insertEvent(ncaId, taskId, mesg);
    }

    private static boolean areAllInstancesInTerminalStateByTaskId(List<Instance> instances) {
        return instances.stream()
                .map(Instance::getState)
                .allMatch(state -> state != null &&
                        "terminated".equalsIgnoreCase(state.getName()));
    }

    private static String getInstanceErrorMessage(List<Instance> instances) {
        var errorMessage = instances.stream()
                .map(Instance::getHealthInfo)
                .filter(healthInfo -> healthInfo != null &&
                        isNotBlank(healthInfo.getErrorLog()))
                .map(InstanceRequest.HealthInfo::getErrorLog)
                .collect(joining("; "));
        return isNotBlank(errorMessage) ? errorMessage : NO_HEALTH_INFORMATION_AVAILABLE;
    }

    private static boolean hasExceededMaxQueuedDurationPlusDelta(
            Instance instance,
            TaskEntity taskEntity) {
        var maxQueuedDuration = taskEntity.getMaxQueuedDuration();
        var createdAt = instance.getCreateTime();
        return Instant.now().minus(maxQueuedDuration.plus(DELTA_DURATION)).isAfter(createdAt);
    }

}
