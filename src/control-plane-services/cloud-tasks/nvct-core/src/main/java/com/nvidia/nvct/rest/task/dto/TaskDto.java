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
package com.nvidia.nvct.rest.task.dto;

import static com.fasterxml.jackson.annotation.JsonFormat.Shape.STRING;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAGS_COUNT;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAG_LENGTH;

import com.fasterxml.jackson.annotation.JsonFormat;
import com.nvidia.nvct.service.telemetry.dto.TelemetriesDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.net.URI;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import lombok.Builder;

@Builder(toBuilder = true)
@Schema(description = "Data Transfer Object(DTO) representing a Task")
public record TaskDto(
        @Schema(description = "Unique Task id")
        @NotNull UUID id,

        @Schema(description = "NVIDIA Cloud Account Id")
        @NotNull String ncaId,

        @Schema(description = "Task name")
        @NotNull String name,

        @Schema(description = "Task status")
        @NotNull TaskStatusEnum status,

        @Schema(description = "Task GPU Specification")
        @NotNull GpuSpecificationDto gpuSpecification,

        @Schema(description = "Task container")
        @NotNull URI containerImage,

        @Schema(description = "Args used to launch the container")
        @Nullable String containerArgs,

        @Schema(description = "Environment settings used to launch the container")
        @Nullable
        List<ContainerEnvironmentEntryDto> containerEnvironment,

        @Schema(description = "Set of models")
        @Nullable
        Set<ArtifactDto> models,

        @Schema(description = "Set of resources")
        @Nullable Set<ArtifactDto> resources,

        @Schema(description = "Set of tags. Maximum allowed number of tags per " +
                "Task is " + MAX_TAGS_COUNT + ". Maximum length of each tag is "
                + MAX_TAG_LENGTH + " chars.")
        @Nullable Set<String> tags,

        @Schema(description = "Task description")
        @Nullable String description,

        @Schema(description = "Results handling strategy")
        @Nullable ResultHandlingStrategyEnum resultHandlingStrategy,

        @Schema(description = "Results location")
        @Nullable String resultsLocation,

        @Schema(description = "Maximum runtime duration",
                type = "string",
                format = "duration",
                example = "PT12H30M")
        @JsonFormat(shape = STRING)
        @Nullable Duration maxRuntimeDuration,

        @Schema(description = "Maximum queued duration",
                defaultValue = "PT72H",
                type = "string",
                format = "duration",
                example = "PT4H30M45S")
        @JsonFormat(shape = STRING)
        @NotNull Duration maxQueuedDuration,

        @Schema(description = "Termination grace period duration",
                defaultValue = "PT1H",
                type = "string",
                format = "duration",
                example = "PT1H30M20S")
        @JsonFormat(shape = STRING)
        @NotNull Duration terminationGracePeriodDuration,

        @Schema(description = "Optional Helm Chart")
        @Nullable URI helmChart,

        @Schema(description = "Task health")
        @Nullable HealthDto healthInfo,

        @Schema(description = "Task secret keys")
        @Nullable Set<String> secrets,

        @Schema(description = "Percentage complete")
        @Nullable Integer percentComplete,

        @Schema(description = "Last heartbeat received timestamp")
        @Nullable Instant lastHeartbeatAt,

        @Schema(description = "Last updated timestamp")
        @Nullable Instant lastUpdatedAt,

        @Schema(description = "Optional telemetry configuration")
        @Nullable TelemetriesDto telemetries,

        @Schema(description = "Task creation timestamp")
        @NotNull Instant createdAt,

        @Schema(description = "List of instances")
        @Nullable List<InstanceDto> instances) {
}
