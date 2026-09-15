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
package com.nvidia.nvct.rest.result.dto;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.UUID;
import lombok.Builder;
import tools.jackson.databind.node.ObjectNode;

@Builder(toBuilder = true)
@Schema(types = {"object"}, description = "Data Transfer Object(DTO) representing a Result")
public record ResultDto(
        @Schema(description = "Result id")
        @NotNull UUID resultId,

        @Schema(description = "Task id")
        @NotNull UUID taskId,

        @Schema(description = "NVIDIA Cloud Account Id")
        @NotNull String ncaId,

        @Schema(description = "Result name")
        @NotNull String name,

        @Schema(description = "Result metadata",
                types = {"object"},
                implementation = Object.class,
                additionalProperties = Schema.AdditionalPropertiesValue.TRUE)
        @NotNull ObjectNode metadata,

        @Schema(description = "Result creation timestamp")
        @NotNull Instant createdAt) {
}
