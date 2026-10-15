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
package com.nvidia.nvcf.rest.function.management.dto;

import static com.nvidia.nvcf.util.NvcfConstants.MAX_TAGS_COUNT;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_TAG_LENGTH;

import com.nvidia.nvcf.rest.telemetry.dto.TelemetriesDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.net.URI;
import java.time.Instant;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import lombok.Builder;

@Builder(toBuilder = true)
@Schema(description = "Data Transfer Object (DTO) representing a function")
public record FunctionDto(
        @Schema(description = "Unique function id")
        @NotNull UUID id,

        @Schema(description = "NVIDIA Cloud Account Id")
        @NotNull String ncaId,

        @Schema(description = "Unique function version id")
        @NotNull UUID versionId,

        @Schema(description = "Function name")
        @NotNull String name,

        @Schema(description = "Function status")
        @NotNull FunctionStatusEnum status,

        @Schema(description = "Entrypoint for invoking the container to process requests")
        @Nullable URI inferenceUrl,

        @Schema(description = """
                Indicates whether the function is owned by another account. If the account
                 that is being used to lookup functions happens to be authorized to invoke/list
                 this function which is owned by a different account, then this field is set
                 to true and ncaId will contain the id of the account that owns the function.
                 Otherwise, this field is not set as it defaults to false.
                """)
        @Nullable Boolean ownedByDifferentAccount,

        @Schema(description = "Optional port number where the inference listener is running - " +
                "defaults to 8000 for Triton")
        @Nullable Integer inferencePort,

        @Schema(description = "Args used to launch the container")
        @Nullable String containerArgs,

        @Schema(description = "Environment settings used to launch the container")
        @Nullable List<ContainerEnvironmentEntryDto> containerEnvironment,

        @Schema(description = "Optional list of models")
        @Nullable List<FunctionModelDto> models,

        @Schema(description = "Optional custom container")
        @Nullable URI containerImage,

        @Schema(description = "Invocation request body format")
        @Nullable ApiBodyFormatEnum apiBodyFormat,

        @Schema(description = "Optional Helm Chart")
        @Nullable URI helmChart,

        @Schema(description = """
                Helm Chart Service Name specified only when helmChart property is specified
                """)
        @Nullable String helmChartServiceName,

        @Schema(description = "Health endpoint for the container or helmChart. " +
                "Deprecated, use health.uri instead.")
        @Deprecated
        @NotNull URI healthUri,

        @Schema(description = "Function creation timestamp")
        @NotNull Instant createdAt,

        @Schema(description = "List of active instances for this function.")
        @Nullable List<InstanceDto> activeInstances,

        @Schema(description = "Optional set of resources.")
        @Nullable Set<ArtifactDto> resources,

        @Schema(description = "Optional set of tags. Maximum allowed number of tags per " +
                "function is " + MAX_TAGS_COUNT + ". Maximum length of each tag is "
                + MAX_TAG_LENGTH + " chars.")
        @Nullable Set<String> tags,

        @Schema(description = "Function/version description")
        @Nullable String description,

        @Schema(description = "Function health configuration")
        @Nullable HealthDto health,

        @Schema(description = "Used to indicate a STREAMING function. Defaults to DEFAULT.")
        @NotNull FunctionTypeEnum functionType,

        @Schema(description = "Optional secret names")
        @Nullable Set<String> secrets,

        @Schema(description = "Optional rate limit policy")
        @Nullable RateLimitDto rateLimit,

        @Schema(description = "Optional function-level LLM invocation configuration.")
        @Nullable LlmInvocationConfigDto llmInvocationConfig,

        @Schema(description = "Optional telemetry configuration for the function")
        @Nullable TelemetriesDto telemetries) {
}
