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
package com.nvidia.nvct.service.icms;

import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.task.dto.HealthDto;
import com.nvidia.nvct.service.registry.RegistryArtifactService;
import com.nvidia.nvct.service.task.TaskService;
import java.util.Objects;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class IcmsService {

    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";
    private static final String MESG_CANNOT_BE_NULL = "Parameter '%s' cannot be null";
    private static final String MESG_INVALID_INSTANCE_TYPE =
            "Task id '%s': Invalid instance-type '%s'";

    private static final String MESG_REQUESTED_INSTANCE =
            "Task id '{}', ICMS requestId '{}', GPU '{}': Requested '1' instance";
    private static final String MESG_TASK_ICMS_OPERATION =
            "Task id '{}': {}";

    private final IcmsClient icmsClient;
    private final boolean allocatorEnabled;
    private final RegistryArtifactService artifactService;
    private final TaskService taskService;

    public IcmsService(
            IcmsClient icmsClient,
            @Value("${nvct.icms.allocator.enabled:true}") boolean allocatorEnabled,
            RegistryArtifactService artifactService,
            TaskService taskService) {
        this.icmsClient = icmsClient;
        this.allocatorEnabled = allocatorEnabled;
        this.artifactService = artifactService;
        this.taskService = taskService;
    }

    public UUID scheduleInstance(TaskEntity task) {
        if (!allocatorEnabled) {
            return null;
        }
        Objects.requireNonNull(task, () -> MESG_CANNOT_BE_NULL.formatted("task"));

        var taskId = task.getTaskId();
        var gpuSpec = task.getGpuSpec();
        var gpu = gpuSpec.getGpu();
        var ncaId = task.getNcaId();
        var cacheSize = artifactService.getSize(ncaId, taskId);
        var cacheHandle = artifactService.getCacheHandle(ncaId, taskId);
        var requestId = icmsClient.createInstance(task, gpuSpec, cacheHandle,
                                                  cacheSize);
        log.info(MESG_REQUESTED_INSTANCE, taskId, requestId, gpu);
        return requestId;
    }

    public boolean terminateInstanceByTaskId(String ncaId, UUID taskId) {
        icmsClient.terminateInstanceByTaskId(ncaId, taskId);
        log.info(MESG_TASK_ICMS_OPERATION, taskId, "Terminated ICMS instances by task id.");
        return true;
    }

    public HealthDto getHealthDto(
            UUID taskId,
            String instanceType,
            String error) {
        if (StringUtils.isBlank(instanceType)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("instanceType");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }
        if (StringUtils.isBlank(error)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("error");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var task = taskService.fetchTask(taskId);
        var gpuSpec = task.getGpuSpec();
        if (!instanceType.equals(gpuSpec.getInstanceType())) {
            // ### Temporarily return health info even if the passed in instanceType does
            // not match the one in the Task definition. This is happening because NVCA is
            // sending an incorrect instanceType to Utils Container. Till NVCA fixes
            // the issue, NVCT API will just log a warning and NOT throw an exception if
            // instanceType does not match.
            var mesg = MESG_INVALID_INSTANCE_TYPE.formatted(taskId, instanceType);
            log.warn(mesg);
        }

        return HealthDto.builder()
                .gpu(gpuSpec.getGpu())
                .instanceType(instanceType)
                .backend(gpuSpec.getBackend())
                .error(error)
                .build();
    }
}
