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

import static com.nvidia.nvct.util.NvctConstants.GRP_TYPE_TASK_MANAGEMENT;
import static com.nvidia.nvct.util.NvctConstants.NCA_ID;
import static com.nvidia.nvct.util.NvctConstants.OPER_CANCEL_TASK;
import static com.nvidia.nvct.util.NvctConstants.OPER_CREATE_TASK;
import static com.nvidia.nvct.util.NvctConstants.OPER_DELETE_TASK;
import static com.nvidia.nvct.util.NvctConstants.OPER_UPDATE_TASK;
import static com.nvidia.nvct.util.NvctConstants.STATE_CANCELED;
import static com.nvidia.nvct.util.NvctConstants.STATE_CREATED;
import static com.nvidia.nvct.util.NvctConstants.STATE_DELETED;
import static com.nvidia.nvct.util.NvctConstants.STATE_UPDATED;
import static com.nvidia.nvct.util.NvctConstants.SUMMARY_CANCEL_TASK;
import static com.nvidia.nvct.util.NvctConstants.SUMMARY_CREATE_TASK;
import static com.nvidia.nvct.util.NvctConstants.SUMMARY_DELETE_TASK;
import static com.nvidia.nvct.util.NvctConstants.SUMMARY_UPDATE_TASK;
import static com.nvidia.nvct.util.NvctConstants.TASK_ID;
import static com.nvidia.nvct.util.NvctConstants.TASK_OBJECT_LOCATION;
import static com.nvidia.nvct.util.NvctConstants.TASK_STATUS;
import static java.lang.String.format;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TaskAuditService {
    private final AuditService auditService;
    private final JsonMapper jsonMapper;

    public AuditEventPayload.Builder auditEventPayloadBuilder() {
        return auditService.auditEventPayloadBuilder();
    }

    public AuditEventPayload.Builder auditEventPayloadBuilder(
            Authentication authentication,
            Map<String, String> customProperties) {
        return auditService.auditEventPayloadBuilder(authentication, customProperties);
    }

    public void auditTaskCreate(
            AuditEventPayload.Builder payloadBuilder,
            TaskEntity taskEntity) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var status = taskEntity.getStatus();
        var summary = format(SUMMARY_CREATE_TASK, taskId, ncaId);
        payloadBuilder.operation(OPER_CREATE_TASK)
                .type(TaskEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_TASK_MANAGEMENT)
                .objectId(taskId.toString())
                .objectLocation(TASK_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.createObjectNode()) // empty
                .jsonAfter(jsonMapper.valueToTree(taskEntity))
                .state(STATE_CREATED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(TASK_ID, taskId)
                .custom(TASK_STATUS, status);
        auditService.audit(payloadBuilder);
    }

    public void auditTaskUpdate(
            AuditEventPayload.Builder payloadBuilder,
            JsonNode taskJsonBefore,
            TaskEntity taskEntity) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var status = taskEntity.getStatus();
        var summary = format(SUMMARY_UPDATE_TASK, taskId, status);
        payloadBuilder.operation(OPER_UPDATE_TASK)
                .type(TaskEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_TASK_MANAGEMENT)
                .objectId(taskId.toString())
                .objectLocation(TASK_OBJECT_LOCATION)
                .jsonBefore(taskJsonBefore)
                .jsonAfter(jsonMapper.valueToTree(taskEntity))
                .state(STATE_UPDATED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(TASK_ID, taskId)
                .custom(TASK_STATUS, status);
        auditService.audit(payloadBuilder);
    }

    public void auditTaskCancel(
            AuditEventPayload.Builder payloadBuilder,
            JsonNode taskJsonBefore,
            TaskEntity taskEntity) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var status = taskEntity.getStatus();
        var summary = format(SUMMARY_CANCEL_TASK, taskId);
        payloadBuilder.operation(OPER_CANCEL_TASK)
                .type(TaskEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_TASK_MANAGEMENT)
                .objectId(taskId.toString())
                .objectLocation(TASK_OBJECT_LOCATION)
                .jsonBefore(taskJsonBefore)
                .jsonAfter(jsonMapper.valueToTree(taskEntity))
                .state(STATE_CANCELED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(TASK_ID, taskId)
                .custom(TASK_STATUS, status);
        auditService.audit(payloadBuilder);
    }

    public void auditTaskDelete(
            AuditEventPayload.Builder payloadBuilder,
            TaskEntity taskEntity) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        var summary = format(SUMMARY_DELETE_TASK, taskId);
        payloadBuilder.operation(OPER_DELETE_TASK)
                .type(TaskEntity.class.getCanonicalName())
                .groupType(GRP_TYPE_TASK_MANAGEMENT)
                .objectId(taskId.toString())
                .objectLocation(TASK_OBJECT_LOCATION)
                .jsonBefore(jsonMapper.valueToTree(taskEntity))
                .jsonAfter(jsonMapper.createObjectNode()) // empty
                .state(STATE_DELETED)
                .summary(summary)
                .custom(NCA_ID, ncaId)
                .custom(TASK_ID, taskId);
        auditService.audit(payloadBuilder);
    }
}
