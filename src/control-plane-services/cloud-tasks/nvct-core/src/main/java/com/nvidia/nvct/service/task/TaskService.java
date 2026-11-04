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
package com.nvidia.nvct.service.task;

import static com.datastax.oss.driver.api.core.data.ByteUtils.fromHexString;
import static com.datastax.oss.driver.api.core.data.ByteUtils.toHexString;
import static com.nvidia.nvct.service.task.TaskMapperService.toTaskStatus;
import static com.nvidia.nvct.util.NvctConstants.MESG_INVALID_CURSOR;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_NCA_ID;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_TASK_ID;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_TASK_STATUS;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.task.dto.HealthDto;
import com.nvidia.nvct.rest.task.dto.TaskDto;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.observation.annotation.Observed;
import io.micrometer.tracing.Tracer;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Predicate;
import java.util.stream.Stream;
import lombok.Builder;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TaskService {
    private static final String MESG_TASK_OPERATION =
            "Task id '{}', status '{}': {}";
    private static final String MESG_TASK_NOT_FOUND =
            "Task id '%s': Not found";
    private static final String MESG_TASK_NOT_IN_ACCOUNT =
            "Task id '%s': Not found in account '%s'";
    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";
    private static final String MESG_CANNOT_BE_NULL = "Parameter '%s' cannot be null";

    private final AccountService accountService;
    private final TasksRepository tasksRepository;
    private final TaskAuditService taskAuditService;
    private final TaskMapperService taskMapperService;
    private final JsonMapper jsonMapper;
    private final Tracer tracer;

    public long countByNcaId(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return tasksRepository.countByNcaId(ncaId);
    }

    public Optional<TaskEntity> lookupTask(UUID taskId) {
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_TASK_ID, taskId));
        return tasksRepository.getByTaskId(taskId);
    }

    @NotNull
    public TaskEntity fetchTask(UUID taskId) {
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_TASK_ID, taskId));
        return tasksRepository.getByTaskId(taskId)
                .orElseThrow(() -> {
                    var mesg = MESG_TASK_NOT_FOUND.formatted(taskId);
                    log.error(mesg);
                    return new NotFoundException(mesg);
                });
    }

    @Builder
    public record TasksSliceContext(List<TaskDto> tasks, String cursor, Integer limit) {
    }

    public TasksSliceContext fetchTasksByAccount(
            String ncaId,
            int limit,
            TaskStatusEnum status,
            String cursor,
            Predicate<TaskEntity> taskFilter) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        Slice<TaskEntity> pagedResult;
        try {
            var byteBuffer = cursor == null ? null : fromHexString(cursor);
            var pageable = PageRequest.of(0, limit);
            var pageRequest = CassandraPageRequest.of(pageable, byteBuffer);
            pagedResult = tasksRepository.findByNcaId(ncaId, pageRequest);
        } catch (Exception e) {
            var mesg = MESG_INVALID_CURSOR.formatted(cursor);
            log.error(mesg);
            throw new BadRequestException(mesg, e);
        }

        var taskStatus = status == null ? null : toTaskStatus(status);
        var dtos = pagedResult.getContent().stream()
                .filter(task -> status == null || task.getStatus() == taskStatus)
                .filter(taskFilter)
                .map(taskMapperService::toTaskDto)
                .toList();
        var builder = TasksSliceContext.builder().tasks(dtos);
        if (pagedResult.hasNext()) {
            var pagingState = ((CassandraPageRequest) pagedResult.getPageable()).getPagingState();
            builder.cursor(toHexString(pagingState));
            builder.limit(limit);
        }
        return builder.build();
    }

    public Stream<TaskEntity> fetchTasksByAccount(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return tasksRepository.findByNcaId(ncaId);
    }

    @Observed(name = "task.save", contextualName = "save-task")
    public TaskEntity saveTask(TaskEntity entity) {
        var taskId = entity.getTaskId();

        setSpanAttributes(entity);
        tasksRepository.save(entity);
        log.info(MESG_TASK_OPERATION, taskId, entity.getStatus(), "Saved task");
        return entity;
    }

    @Observed(name = "task.delete", contextualName = "delete-task")
    public void deleteTask(TaskEntity entity) {
        var taskId = entity.getTaskId();
        setSpanAttributes(entity);
        tasksRepository.delete(entity);
        log.info(MESG_TASK_OPERATION, taskId, entity.getStatus(), "Deleted task");
    }

    public void deleteTask(UUID taskId) {
        deleteTask(fetchTask(taskId));
    }

    @Observed(name = "task.update", contextualName = "update-task-by-id")
    public TaskEntity updateTask(UUID taskId) {
        Objects.requireNonNull(taskId, () -> MESG_CANNOT_BE_NULL.formatted("taskId"));
        var taskEntity = fetchTask(taskId);
        return updateTask(taskEntity);
    }

    @Observed(name = "task.update", contextualName = "update-task")
    public TaskEntity updateTask(TaskEntity entity) {
        return updateTask(entity, true);
    }

    @Observed(name = "task.update", contextualName = "update-task-with-audit")
    public TaskEntity updateTask(TaskEntity entity, boolean shouldAudit) {
        var jsonBefore = jsonMapper.valueToTree(entity);

        setSpanAttributes(entity);
        entity.setLastUpdatedAt(Instant.now());
        entity = tasksRepository.insert(entity);

        final var finalEntity = entity;
        var optPayloadBuilder = shouldAudit ?
                Optional.of(taskAuditService.auditEventPayloadBuilder()) :
                Optional.<AuditEventPayload.Builder>empty();
        optPayloadBuilder
                .ifPresent(builder -> taskAuditService.auditTaskUpdate(builder, jsonBefore,
                                                                       finalEntity));

        var taskId = entity.getTaskId();
        var status = entity.getStatus();
        log.info(MESG_TASK_OPERATION, taskId, status, "Updated last_updated_at");
        return entity;
    }

    @Observed(name = "task.update", contextualName = "update-task-status")
    public TaskEntity updateTask(UUID taskId, TaskStatus status) {
        var entity = fetchTask(taskId);
        var jsonBefore = jsonMapper.valueToTree(entity);

        log.info(MESG_TASK_OPERATION, taskId, entity.getStatus(), "Updating status to " + status);
        setSpanAttributes(entity);
        entity.setStatus(status);
        entity.setLastUpdatedAt(Instant.now());
        entity = tasksRepository.insert(entity);

        var payloadBuilder = taskAuditService.auditEventPayloadBuilder();
        taskAuditService.auditTaskUpdate(payloadBuilder, jsonBefore, entity);
        log.info(MESG_TASK_OPERATION, taskId, entity.getStatus(), "Updated status");
        return entity;
    }

    public TaskEntity updateTask(UUID taskId, TaskStatus status, Instant lastHeartbeatAt) {
        return updateTask(taskId, status, lastHeartbeatAt, Optional.empty());
    }

    @Observed(name = "task.update", contextualName = "update-task-status-heartbeat")
    public TaskEntity updateTask(
            UUID taskId,
            TaskStatus status,
            Instant lastHeartbeatAt,
            Optional<Integer> percentComplete) {
        var entity = fetchTask(taskId);
        var payloadBuilder = taskAuditService.auditEventPayloadBuilder();
        var jsonBefore = jsonMapper.valueToTree(entity);

        setSpanAttributes(entity);
        entity.setStatus(status);
        entity.setLastHeartbeatAt(lastHeartbeatAt);
        if (percentComplete.isPresent()) {
            entity.setPercentComplete(percentComplete.get());
        }
        entity.setLastUpdatedAt(Instant.now());
        entity = tasksRepository.insert(entity);

        taskAuditService.auditTaskUpdate(payloadBuilder, jsonBefore, entity);
        log.info(MESG_TASK_OPERATION,
                 taskId, entity.getStatus(),
                 "Updated status, last_heartbeat_at, and maybe percent_complete");
        return entity;
    }

    @Observed(name = "task.update", contextualName = "update-task-heartbeat")
    public TaskEntity updateTask(UUID taskId, Instant lastHeartbeatAt) {
        var entity = fetchTask(taskId);
        var payloadBuilder = taskAuditService.auditEventPayloadBuilder();
        var jsonBefore = jsonMapper.valueToTree(entity);

        setSpanAttributes(entity);
        entity.setLastHeartbeatAt(lastHeartbeatAt);
        entity.setLastUpdatedAt(Instant.now());
        entity = tasksRepository.insert(entity);

        taskAuditService.auditTaskUpdate(payloadBuilder, jsonBefore, entity);
        log.info(MESG_TASK_OPERATION, taskId, entity.getStatus(), "Updated last_heartbeat_at");
        return entity;
    }

    @Observed(name = "task.update", contextualName = "update-task-status-health")
    public TaskEntity updateTask(UUID taskId, TaskStatus status, HealthDto healthInfo) {
        var entity = fetchTask(taskId);
        var payloadBuilder = taskAuditService.auditEventPayloadBuilder();
        var jsonBefore = jsonMapper.valueToTree(entity);

        setSpanAttributes(entity);
        entity.setStatus(status);
        entity.setHealth(taskMapperService.serializeHealth(healthInfo).orElse(null));
        entity.setLastUpdatedAt(Instant.now());
        entity = tasksRepository.insert(entity);

        taskAuditService.auditTaskUpdate(payloadBuilder, jsonBefore, entity);
        log.info(MESG_TASK_OPERATION, taskId, entity.getStatus(), "Updated status and health");
        return entity;
    }

    public TaskEntity validateAccount(String ncaId, UUID taskId) {
        Objects.requireNonNull(taskId, () -> MESG_CANNOT_BE_NULL.formatted("taskId"));
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        // Validate account exists.
        accountService.lookupAccountUsingNcaIdOrThrow(ncaId);

        // Ensure ncaId in Task definition matches the one that is passed in.
        var taskEntity = fetchTask(taskId);
        if (!taskEntity.getNcaId().equals(ncaId)) {
            var mesg = MESG_TASK_NOT_IN_ACCOUNT.formatted(taskId, ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }

        return taskEntity;
    }

    public void validateAccount(String ncaId, TaskEntity taskEntity) {
        Objects.requireNonNull(taskEntity, () -> MESG_CANNOT_BE_NULL.formatted("taskEntity"));
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        // Validate account exists.
        accountService.lookupAccountUsingNcaIdOrThrow(ncaId);

        // Ensure ncaId in Task definition matches the one that is passed in.
        if (!taskEntity.getNcaId().equals(ncaId)) {
            var mesg = MESG_TASK_NOT_IN_ACCOUNT.formatted(taskEntity.getTaskId(), ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }
    }

    private void setSpanAttributes(TaskEntity entity) {
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, entity.getNcaId(),
                                                      SPAN_TAG_TASK_ID, entity.getTaskId(),
                                                      SPAN_TAG_TASK_STATUS, entity.getStatus()));
    }
}
