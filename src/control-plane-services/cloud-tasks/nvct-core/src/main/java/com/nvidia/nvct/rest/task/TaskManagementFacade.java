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
package com.nvidia.nvct.rest.task;

import static com.nvidia.nvct.service.task.TaskPredicateUtils.taskAccessMatch;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvct.rest.task.dto.BulkTaskDetailsRequest;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.ListBasicTaskDetailsResponse;
import com.nvidia.nvct.rest.task.dto.ListTasksResponse;
import com.nvidia.nvct.rest.task.dto.TaskResponse;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.task.TaskAuditService;
import com.nvidia.nvct.service.task.TaskManagementService;
import com.nvidia.nvct.util.NvctUtils;
import jakarta.servlet.http.HttpServletRequest;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.ResponseEntity;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TaskManagementFacade {

    private final TaskAuditService taskAuditService;
    private final TaskManagementService taskManagementService;

    public TaskResponse createAndLaunchTask(
            String ncaId,
            CreateTaskRequest request,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var dto = taskManagementService
                .createAndLaunchTask(ncaId,
                                     request,
                                     () -> taskAccessMatch(authentication, Optional.empty()),
                                     payloadBuilder);
        return new TaskResponse(dto);
    }

    public ListBasicTaskDetailsResponse listBasicDetailsOfSpecificTasks(
            String ncaId,
            BulkTaskDetailsRequest listBasicTasksRequest,
            Authentication authentication) {
        var taskIds = listBasicTasksRequest.taskIds();
        var dtos = taskIds.stream()
                .map(taskId -> taskManagementService
                        .getBasicTaskDetails(ncaId,
                                             taskId,
                                             task -> taskAccessMatch(authentication,
                                                                     Optional.of(task.getTaskId()))))
                .toList();
        return new ListBasicTaskDetailsResponse(ncaId, dtos);
    }

    public ListTasksResponse listTasks(
            String ncaId,
            Authentication authentication,
            int limit,
            TaskStatusEnum status,
            String cursor) {
        var sliceContext = taskManagementService
                .listTasks(ncaId,
                           status,
                           limit,
                           cursor,
                           taskEntity -> taskAccessMatch(authentication,
                                                         Optional.of(taskEntity.getTaskId())));
        return new ListTasksResponse(sliceContext.tasks(),
                                     sliceContext.limit(),
                                     sliceContext.cursor());
    }

    public TaskResponse getTaskDetails(String ncaId,
                                       UUID taskId,
                                       Authentication authentication,
                                       boolean includeSecrets) {
        var dto = taskManagementService
                .getTaskDetails(ncaId,
                                taskId,
                                includeSecrets,
                                taskEntity -> taskAccessMatch(authentication,
                                                              Optional.of(taskEntity.getTaskId())));
        return new TaskResponse(dto);
    }

    public TaskResponse cancelTask(String ncaId,
                                   UUID taskId,
                                   HttpServletRequest httpServletRequest,
                                   Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var dto = taskManagementService
                .cancelTask(ncaId,
                            taskId,
                            taskEntity -> taskAccessMatch(authentication,
                                                          Optional.of(taskEntity.getTaskId())),
                            payloadBuilder);
        return new TaskResponse(dto);
    }

    public ResponseEntity<Void> deleteTask(String ncaId,
                                           UUID taskId,
                                           HttpServletRequest httpServletRequest,
                                           Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        taskManagementService
                .deleteTask(ncaId,
                            taskId,
                            taskEntity -> taskAccessMatch(authentication,
                                                          Optional.of(taskEntity.getTaskId())),
                            payloadBuilder);
        return ResponseEntity.noContent().build();    // Status 204.
    }

    private AuditEventPayload.Builder auditEventPayloadBuilder(
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var customProperties = NvctUtils.getCustomProperties(httpServletRequest);
        return taskAuditService.auditEventPayloadBuilder(authentication, customProperties);
    }
}
