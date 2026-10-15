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
package com.nvidia.nvcf.rest.function.deployment.dto;

import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
import lombok.Builder;

@Builder
@Schema(types = {"object"}, description = "Function deployment response")
public record FunctionDeploymentDto(
        @Schema(description = "Function id")
        @NotNull UUID functionId,

        @Schema(description = "Function version id")
        @NotNull UUID functionVersionId,

        @Schema(description = "Last deployment id")
        @NotNull UUID deploymentId,

        @Schema(description = "Function name")
        @NotBlank String functionName,

        @Schema(description = "NVIDIA Cloud Account Id")
        @NotBlank String ncaId,

        @Schema(description = "Function status")
        @NotNull FunctionStatusEnum functionStatus,

        @Schema(description = """
                Health info for a deployment specification is included only if there are any
                 issues/errors.
                """)
        @Nullable List<DeploymentHealthDto> healthInfo,

        @Schema(description = "Function deployment details")
        @NotNull List<GpuSpecificationDto> deploymentSpecifications,

        @Schema(description = "Function deployment creation timestamp")
        @NotNull Instant createdAt,

        @Schema(description = "Function deployment modification timestamp")
        @NotNull Instant lastUpdatedAt) {
}
