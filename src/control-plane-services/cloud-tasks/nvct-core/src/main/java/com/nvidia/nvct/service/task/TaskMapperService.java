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

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvct.persistence.task.entity.HealthUdt;
import com.nvidia.nvct.persistence.task.entity.ModelUdt;
import com.nvidia.nvct.persistence.task.entity.ResourceUdt;
import com.nvidia.nvct.persistence.task.entity.ResultHandlingStrategy;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.persistence.task.entity.TelemetriesUdt;
import com.nvidia.nvct.rest.task.dto.ArtifactDto;
import com.nvidia.nvct.rest.task.dto.BasicTaskDto;
import com.nvidia.nvct.rest.task.dto.ContainerEnvironmentEntryDto;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.HealthDto;
import com.nvidia.nvct.rest.task.dto.InstanceDto;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.TaskDto;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.telemetry.dto.TelemetriesDto;
import java.net.URI;
import java.time.Duration;
import java.time.Instant;
import java.util.Base64;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import tools.jackson.core.JacksonException;
import tools.jackson.core.type.TypeReference;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RequiredArgsConstructor
public class TaskMapperService {

    public static final Duration DEFAULT_MAX_QUEUE_DURATION = Duration.parse("PT72H");
    public static final Duration DEFAULT_TERMINATION_GRACE_PERIOD_DURATION = Duration.parse("PT1H");
    private static final String MESG_DEFAULT_REGISTRY_MISSING =
            "Default '%s' registry is missing";

    private static final ListEnvTypeReference LIST_ENV_TYPE_REFERENCE = new ListEnvTypeReference();

    private static class ListEnvTypeReference extends
            TypeReference<List<ContainerEnvironmentEntryDto>> {
    }

    private final JsonMapper jsonMapper;
    private final GpuSpecificationMapperService gpuSpecificationMapperService;


    public TaskEntity toTaskEntity(String ncaId, CreateTaskRequest request) {
        var models = getModelUdts(request.models());
        var resources = getResourceUdts(request.resources());
        var maxQueuedDuration =
                Objects.requireNonNullElse(request.maxQueuedDuration(), DEFAULT_MAX_QUEUE_DURATION);
        var terminationGracePeriodDuration =
                Objects.requireNonNullElse(request.terminationGracePeriodDuration(),
                                           DEFAULT_TERMINATION_GRACE_PERIOD_DURATION);
        var resultStrategy = request.resultHandlingStrategy() == null ?
                ResultHandlingStrategy.UPLOAD :
                ResultHandlingStrategy.fromText(request.resultHandlingStrategy().toString());
        var serializedContainerEnvironment =
                serializeContainerEnvironment(request.containerEnvironment());
        var taskId = UUID.randomUUID();
        var createdAt = Instant.now();
        var containerImage = Objects.nonNull(request.containerImage()) ?
                request.containerImage().toString() : null;
        var helmChart = Objects.nonNull(request.helmChart()) ?
                request.helmChart().toString() : null;
        return TaskEntity.builder()
                .taskId(taskId)
                .ncaId(ncaId)
                .name(request.name())
                .status(TaskStatus.QUEUED)
                .description(request.description())
                .tags(request.tags())
                .helmChart(helmChart)
                .containerImage(containerImage)
                .containerArgs(request.containerArgs())
                .containerEnvironment(serializedContainerEnvironment.orElse(null))
                .models(models.orElse(null))
                .resources(resources.orElse(null))
                .gpuSpec(gpuSpecificationMapperService.toGpuSpecUdt(request.gpuSpecification()))
                .maxRuntimeDuration(request.maxRuntimeDuration())
                .maxQueuedDuration(maxQueuedDuration)
                .terminalGracePeriodDuration(terminationGracePeriodDuration)
                .resultHandlingStrategy(resultStrategy)
                .resultsLocation(request.resultsLocation())
                .createdAt(createdAt)
                .status(TaskStatus.QUEUED)
                .telemetries(toTelemetriesUdt(request.telemetries()).orElse(null))
                .build();
    }

    public TaskDto toTaskDto(TaskEntity entity) {
        return toTaskDto(entity, Optional.empty(), Optional.empty());
    }

    public TaskDto toTaskDto(
            TaskEntity entity,
            Optional<Set<String>> secrets,
            Optional<List<InstanceDto>> instances) {
        var resultHandlingStrategyRaw = entity.getResultHandlingStrategy().name();
        var deserializeContainerEnvironment =
                deserializeContainerEnvironment(entity.getContainerEnvironment());
        var resultHandlingStrategy = ResultHandlingStrategyEnum.fromText(resultHandlingStrategyRaw);
        var containerImage = StringUtils.isNotBlank(entity.getContainerImage()) ?
                URI.create(entity.getContainerImage()) : null;
        var helmChart = StringUtils.isNotBlank(entity.getHelmChart()) ?
                URI.create(entity.getHelmChart()) : null;
        var healthInfo = deserializeHealth(entity.getHealth())
                .or(() -> toHealthDto(entity.getLegacyHealthInfo()))
                .orElse(null);
        return TaskDto.builder()
                .id(entity.getTaskId())
                .ncaId(entity.getNcaId())
                .name(entity.getName())
                .description(entity.getDescription())
                .tags(entity.getTags())
                .helmChart(helmChart)
                .containerImage(containerImage)
                .containerArgs(entity.getContainerArgs())
                .containerEnvironment(deserializeContainerEnvironment.orElse(null))
                .models(getModelDtos(entity.getModels()).orElse(null))
                .resources(getResourceDtos(entity.getResources()).orElse(null))
                .gpuSpecification(
                        gpuSpecificationMapperService.toGpuSpecificationDto(entity.getGpuSpec()))
                .maxRuntimeDuration(entity.getMaxRuntimeDuration())
                .maxQueuedDuration(entity.getMaxQueuedDuration())
                .terminationGracePeriodDuration(entity.getTerminalGracePeriodDuration())
                .resultsLocation(entity.getResultsLocation())
                .resultHandlingStrategy(resultHandlingStrategy)
                .percentComplete(entity.getPercentComplete())
                .status(toTaskStatusEnum(entity.getStatus()))
                .healthInfo(healthInfo)
                .lastHeartbeatAt(entity.getLastHeartbeatAt())
                .lastUpdatedAt(entity.getLastUpdatedAt())
                .createdAt(entity.getCreatedAt())
                .secrets(secrets.orElse(null))
                .instances(instances.orElse(null))
                .telemetries(toTelemetriesDto(entity.getTelemetries()).orElse(null))
                .build();
    }

    public BasicTaskDto toBasicTaskDto(TaskEntity taskEntity) {
        return BasicTaskDto.builder()
                .id(taskEntity.getTaskId())
                .name(taskEntity.getName())
                .status(toTaskStatusEnum(taskEntity.getStatus()))
                .build();
    }

    @SneakyThrows
    private Optional<String> serializeContainerEnvironment(
            List<ContainerEnvironmentEntryDto> environment) {
        if (Objects.isNull(environment) || environment.isEmpty()) {
            return Optional.empty();
        }

        var json = jsonMapper.writeValueAsBytes(environment);
        return Optional.of(Base64.getEncoder().encodeToString(json));
    }

    @SneakyThrows
    private Optional<List<ContainerEnvironmentEntryDto>> deserializeContainerEnvironment(
            String env) {
        if (StringUtils.isBlank(env)) {
            return Optional.empty();
        }

        var json = Base64.getDecoder().decode(env);
        return Optional.of(jsonMapper.readValue(json, LIST_ENV_TYPE_REFERENCE));
    }

    private Optional<Set<ModelUdt>> getModelUdts(Set<ArtifactDto> models) {
        if (models == null) {
            return Optional.empty();
        }
        return Optional.of(models.stream()
                                   .map(this::toModelUdt)
                                   .collect(Collectors.toSet()));
    }

    @VisibleForTesting
    public Optional<Set<ArtifactDto>> getModelDtos(Set<ModelUdt> models) {
        if (models == null) {
            return Optional.empty();
        }
        return Optional.of(models.stream()
                                   .map(this::toArtifactDto)
                                   .collect(Collectors.toSet()));
    }

    private Optional<Set<ResourceUdt>> getResourceUdts(Set<ArtifactDto> resources) {
        if (resources == null) {
            return Optional.empty();
        }
        return Optional.of(resources.stream()
                                   .map(this::toResourceUdt)
                                   .collect(Collectors.toSet()));
    }

    @VisibleForTesting
    public Optional<Set<ArtifactDto>> getResourceDtos(Set<ResourceUdt> resources) {
        if (resources == null) {
            return Optional.empty();
        }
        return Optional.of(resources.stream()
                                   .map(this::toArtifactDto)
                                   .collect(Collectors.toSet()));
    }

    private ModelUdt toModelUdt(ArtifactDto artifactDto) {
        return ModelUdt.builder()
                .name(artifactDto.getName())
                .version(artifactDto.getVersion())
                .url(artifactDto.getUri().toString())
                .build();
    }

    private ArtifactDto toArtifactDto(ModelUdt modelUdt) {
        return ArtifactDto.builder()
                .name(modelUdt.getName())
                .version(modelUdt.getVersion())
                .uri(URI.create(modelUdt.getUrl()))
                .build();
    }

    private ArtifactDto toArtifactDto(ResourceUdt resourceUdt) {
        return ArtifactDto.builder()
                .name(resourceUdt.getName())
                .version(resourceUdt.getVersion())
                .uri(URI.create(resourceUdt.getUrl()))
                .build();
    }

    private ResourceUdt toResourceUdt(ArtifactDto artifactDto) {
        return ResourceUdt.builder()
                .name(artifactDto.getName())
                .version(artifactDto.getVersion())
                .url(artifactDto.getUri().toString())
                .build();
    }

    @SneakyThrows
    public Optional<String> serializeHealth(HealthDto healthDto) {
        if (healthDto == null) {
            return Optional.empty();
        }

        return Optional.of(jsonMapper.writeValueAsString(healthDto));
    }

    @VisibleForTesting
    public Optional<HealthDto> deserializeHealth(String json) {
        if (StringUtils.isBlank(json)) {
            return Optional.empty();
        }

        try {
            return Optional.of(jsonMapper.readValue(json, HealthDto.class));
        } catch (JacksonException e) {
            log.error("Failed to deserialize task health: {}", e.getMessage(), e);
            return Optional.empty();
        }
    }

    public static Optional<HealthDto> toHealthDto(HealthUdt udt) {
        if (udt == null) {
            return Optional.empty();
        }

        return Optional.of(HealthDto.builder()
                                   .backend(udt.getBackend())
                                   .gpu(udt.getGpu())
                                   .instanceType(udt.getInstanceType())
                                   .error(udt.getError())
                                   .build());
    }

    private static Optional<TelemetriesUdt> toTelemetriesUdt(TelemetriesDto telemetriesDto) {
        if (telemetriesDto == null) {
            return Optional.empty();
        }

        return Optional.of(TelemetriesUdt.builder()
                                   .logsTelemetryId(telemetriesDto.logsTelemetryId())
                                   .metricsTelemetryId(telemetriesDto.metricsTelemetryId())
                                   .tracesTelemetryId(telemetriesDto.tracesTelemetryId())
                                   .build());
    }

    private static Optional<TelemetriesDto> toTelemetriesDto(TelemetriesUdt telemetriesUdt) {
        if (telemetriesUdt == null) {
            return Optional.empty();
        }

        return Optional.of(TelemetriesDto.builder()
                                   .logsTelemetryId(telemetriesUdt.getLogsTelemetryId())
                                   .metricsTelemetryId(telemetriesUdt.getMetricsTelemetryId())
                                   .tracesTelemetryId(telemetriesUdt.getTracesTelemetryId())
                                   .build());
    }

    public static TaskStatus toTaskStatus(TaskStatusEnum taskStatusEnum) {
        return switch (taskStatusEnum) {
            case QUEUED -> TaskStatus.QUEUED;
            case LAUNCHED -> TaskStatus.LAUNCHED;
            case RUNNING -> TaskStatus.RUNNING;
            case ERRORED -> TaskStatus.ERRORED;
            case CANCELED -> TaskStatus.CANCELED;
            case EXCEEDED_MAX_RUNTIME_DURATION -> TaskStatus.EXCEEDED_MAX_RUNTIME_DURATION;
            case EXCEEDED_MAX_QUEUED_DURATION -> TaskStatus.EXCEEDED_MAX_QUEUED_DURATION;
            case COMPLETED -> TaskStatus.COMPLETED;
        };
    }

    private static TaskStatusEnum toTaskStatusEnum(TaskStatus taskStatus) {
        return switch (taskStatus) {
            case QUEUED -> TaskStatusEnum.QUEUED;
            case LAUNCHED -> TaskStatusEnum.LAUNCHED;
            case RUNNING -> TaskStatusEnum.RUNNING;
            case ERRORED -> TaskStatusEnum.ERRORED;
            case CANCELED -> TaskStatusEnum.CANCELED;
            case EXCEEDED_MAX_RUNTIME_DURATION -> TaskStatusEnum.EXCEEDED_MAX_RUNTIME_DURATION;
            case EXCEEDED_MAX_QUEUED_DURATION -> TaskStatusEnum.EXCEEDED_MAX_QUEUED_DURATION;
            case COMPLETED -> TaskStatusEnum.COMPLETED;
        };
    }
}
