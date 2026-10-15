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
package com.nvidia.nvct.grpc;

import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ENVIRONMENT;
import static com.nvidia.nvct.util.TestConstants.TEST_GFN_GPU_SPEC;
import static com.nvidia.nvct.util.TestConstants.TEST_MODELS;
import static com.nvidia.nvct.util.TestConstants.TEST_RESOURCES;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import com.nvidia.boot.registries.service.registry.model.ModelRegistry;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistry;
import com.nvidia.nvct.persistence.event.EventsByTaskRepository;
import com.nvidia.nvct.persistence.result.ResultsByTaskRepository;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.persistence.task.entity.ModelUdt;
import com.nvidia.nvct.persistence.task.entity.ResourceUdt;
import com.nvidia.nvct.persistence.task.entity.ResultHandlingStrategy;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.persistence.task.entity.TelemetriesUdt;
import com.nvidia.nvct.service.registry.RegistryArtifactService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.MockEssServer;
import java.time.Duration;
import java.time.Instant;
import java.util.Base64;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TestTaskService {
    @Autowired
    TasksRepository tasksRepository;
    @Autowired
    private EventsByTaskRepository eventsByTaskRepository;
    @Autowired
    private ResultsByTaskRepository resultsByTaskRepository;
    @Autowired
    private JsonMapper jsonMapper;
    @Autowired
    private TaskService taskService;
    @Autowired
    private RegistryArtifactService artifactService;
    @Autowired
    private List<ContainerRegistry> containerRegistries;
    @Autowired
    private List<HelmRegistry> helmRegistries;
    @Autowired
    private List<ModelRegistry> modelRegistries;
    @Autowired
    private List<ResourceRegistry> resourceRegistries;

    public void createTaskWithModel(String ncaId,
                                    UUID taskId,
                                    UUID icmsRequestId,
                                    Set<ModelUdt> models) {
        createTask(ncaId, taskId, icmsRequestId, ResultHandlingStrategy.UPLOAD,
                   models, null, null, null, null, null);

    }

    public void createTaskWithResource(String ncaId, UUID taskId, UUID icmsRequestId) {
        createTask(ncaId, taskId, icmsRequestId, ResultHandlingStrategy.UPLOAD,
                   null, TEST_RESOURCES, null, null, null, null);
    }

    public void createTaskWithTelemetries(String ncaId,
                                          UUID taskId,
                                          UUID icmsRequestId,
                                          TelemetriesUdt telemetriesUdt) {
        createTask(ncaId, taskId, icmsRequestId, ResultHandlingStrategy.UPLOAD,
                   null, TEST_RESOURCES, null, null, null, telemetriesUdt);

    }

    public void createTaskWithModelAndResources(String ncaId,
                                                UUID taskId,
                                                UUID icmsRequestId) {
        createTask(ncaId, taskId, icmsRequestId, ResultHandlingStrategy.UPLOAD,
                   TEST_MODELS, TEST_RESOURCES, null, null, null, null);
    }

    public void createTask(String ncaId,
                           UUID taskId,
                           UUID icmsRequestId) {
        createTask(ncaId, taskId, icmsRequestId, ResultHandlingStrategy.UPLOAD,
                   null, null, null, null, null, null);
    }

    public void createTask(String ncaId,
                           UUID taskId,
                           UUID icmsRequestId,
                           TaskStatus status,
                           GpuSpecUdt gpu) {
        createTask(ncaId, taskId, icmsRequestId, ResultHandlingStrategy.UPLOAD,
                   null, null, null, status, gpu, null);
    }

    public void createTask(String ncaId,
                           UUID taskId,
                           UUID icmsRequestId,
                           TaskStatus status) {
        createTask(ncaId, taskId, icmsRequestId, ResultHandlingStrategy.UPLOAD,
                   null, null, null, status, null, null);
    }

    public void createTask(String ncaId,
                           UUID taskId,
                           UUID icmsRequestId,
                           Instant createdAt) {
        createTask(ncaId, taskId, icmsRequestId, ResultHandlingStrategy.UPLOAD,
                   null, null, createdAt, null, null, null);
    }

    public void createTask(String ncaId,
                           UUID taskId,
                           UUID icmsRequestId,
                           ResultHandlingStrategy resultHandlingStrategy) {
        createTask(ncaId, taskId, icmsRequestId, resultHandlingStrategy,
                   null, null, null, null, null, null);
    }

    @SneakyThrows
    private void createTask(String ncaId,
                            UUID taskId,
                            UUID icmsRequestId,
                            ResultHandlingStrategy resultHandlingStrategy,
                            Set<ModelUdt> models,
                            Set<ResourceUdt> resources,
                            Instant createTime,
                            TaskStatus status,
                            GpuSpecUdt gpu,
                            TelemetriesUdt telemetries) {
        var createdAt = createTime != null ? createTime : Instant.now();
        var taskStatus = status == null ? TaskStatus.QUEUED : status;
        var gpuSpec = gpu == null ? TEST_GFN_GPU_SPEC : gpu;
        var name = "Task-" + taskId;
        var containerEnv = Base64.getEncoder()
                .encodeToString(jsonMapper
                                        .writeValueAsBytes(TEST_CONTAINER_ENVIRONMENT));
        var taskEntity = TaskEntity.builder()
                .taskId(taskId)
                .ncaId(ncaId)
                .name(name)
                .status(taskStatus)
                .description("My first task")
                .tags(Set.of("tag1", "tag2"))
                .containerImage("alpine:3.14")
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerEnvironment(containerEnv)
                .models(models)
                .resources(resources)
                .gpuSpec(gpuSpec)
                .maxRuntimeDuration(Duration.parse("PT1H"))
                .maxQueuedDuration(Duration.parse("PT1H"))
                .terminalGracePeriodDuration(Duration.parse("PT1H"))
                .resultHandlingStrategy(resultHandlingStrategy)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .telemetries(telemetries)
                .createdAt(createdAt)
                .status(taskStatus)
                .hasSecrets(null)  // Simulate old tasks before this field existed
                .build();
        tasksRepository.save(taskEntity);
    }

    public void clearAll() {
        tasksRepository.deleteAll();
        eventsByTaskRepository.deleteAll();
        resultsByTaskRepository.deleteAll();
        artifactService.invalidateCache();

        containerRegistries.forEach(ContainerRegistry::invalidateCache);
        helmRegistries.forEach(HelmRegistry::invalidateCache);
        modelRegistries.forEach(ModelRegistry::invalidateCache);
        resourceRegistries.forEach(ResourceRegistry::invalidateCache);

        MockEssServer.clearSecrets();
    }

    public int getEventCount(UUID taskId) {
        return (int)eventsByTaskRepository.findByKeyTaskId(taskId).count();
    }

    public Integer getPercentComplete(UUID taskId) {
        return taskService.fetchTask(taskId).getPercentComplete();
    }
}
