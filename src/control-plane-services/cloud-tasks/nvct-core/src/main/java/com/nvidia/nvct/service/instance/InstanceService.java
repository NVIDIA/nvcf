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
package com.nvidia.nvct.service.instance;

import static com.nvidia.nvct.util.NvctConstants.TERMINAL_TASK_STATUSES;

import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.task.dto.InstanceDto;
import com.nvidia.nvct.rest.task.dto.InstanceStateEnum;
import com.nvidia.nvct.service.icms.IcmsClient;
import com.nvidia.nvct.service.icms.IcmsStubService.Instance;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.InstanceState;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class InstanceService {

    private static final String MESG_INSTANCE_BY_ACC_TASK_STATUS =
            "{} instances for account '{}' and task '{}'";
    private static final String MESG_UNKNOWN_INSTANCE_STATE =
            "ICMS Request Id '{}': Unknown InstanceState '{}'";
    private static final String MESG_NULL_INSTANCE_STATE =
            "ICMS Request Id '{}': Null InstanceState";
    private static final String MESG_BLANK_INSTANCE_STATE_NAME =
            "ICMS Request Id '{}': InstanceState name is blank";
    private static final String MESG_TERMINAL_STATUS_AND_INSTANCE_STATE =
            "Task id '{}': Terminal status '{}' and Instance state '{}'";

    private final IcmsClient icmsClient;

    public Optional<List<InstanceDto>> getInstances(TaskEntity taskEntity) {
        return getInstancesByTaskId(taskEntity);
    }

    public Optional<List<InstanceDto>> getInstancesByTaskId(TaskEntity taskEntity) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();

        log.debug(MESG_INSTANCE_BY_ACC_TASK_STATUS, "Retrieving", ncaId, taskId);

        var instances = icmsClient.getInstancesByTaskId(ncaId, taskId);

        log.debug(MESG_INSTANCE_BY_ACC_TASK_STATUS, "Retrieved", ncaId, taskId);
        var instancesWithIds = instances.stream()
                .filter(instance -> StringUtils.isNotBlank(instance.getInstanceId()))
                .toList();
        if (instancesWithIds.isEmpty()) {
            return Optional.empty();
        }

        return Optional.of(instancesWithIds.stream()
                .map(instance -> InstanceDto.builder()
                        .instanceId(instance.getInstanceId())
                        .taskId(taskId)
                        .instanceType(instance.getInstanceType())
                        .instanceState(getInstanceState(instance, taskEntity))
                        .icmsRequestId(UUID.fromString(instance.getLaunchRequestId()))
                        .ncaId(ncaId)
                        .gpu(taskEntity.getGpuSpec().getGpu())
                        .backend(instance.getCloudProvider())
                        .location(instance.getPlacement().getAvailabilityZone())
                        .instanceCreatedAt(instance.getCreateTime())
                        // TODO right now ICMS instance object does not have updatedAt. At the
                        // legacy flow we populated instanceUpdatedAt in DTO with a value when
                        // request was updated last time, not instance. ICMS should provide actual
                        // value related to instance, not to request.
                        .instanceUpdatedAt(instance.getCreateTime())
                        .build())
                .toList());
    }

    private InstanceStateEnum getInstanceState(
            Instance instance,
            TaskEntity taskEntity) {
        return getInstanceState(
                instance.getState(), instance.getLaunchRequestId(), taskEntity);
    }

    private InstanceStateEnum getInstanceState(
            InstanceState instanceState,
            Object icmsRequestId,
            TaskEntity taskEntity) {
        InstanceStateEnum state;

        if (instanceState == null) {
            log.info(MESG_NULL_INSTANCE_STATE, icmsRequestId);
            state = null;
        } else {
            var instanceStateName = instanceState.getName();
            if (StringUtils.isBlank(instanceStateName)) {
                log.info(MESG_BLANK_INSTANCE_STATE_NAME, icmsRequestId);
                state = null;
            } else {
                state = switch (instanceStateName.toUpperCase()) {
                    case "STARTING" -> InstanceStateEnum.STARTING;
                    case "RUNNING" -> InstanceStateEnum.RUNNING;
                    case "TERMINATED" -> InstanceStateEnum.TERMINATED;
                    default -> {
                        log.info(MESG_UNKNOWN_INSTANCE_STATE, icmsRequestId, instanceStateName);
                        yield null;
                    }
                };
            }
        }

        var status = taskEntity.getStatus();
        if (state != null && TERMINAL_TASK_STATUSES.contains(status)) {
            // Return null as instance state when the Task has reached terminal status to
            // avoid confusion in the response. Log the actual instance state.
            log.info(MESG_TERMINAL_STATUS_AND_INSTANCE_STATE,
                     taskEntity.getTaskId(), status, state);
            state = null;
        }
        return state;
    }

}
