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
package com.nvidia.nvcf.rest.telemetry.dto;

import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.util.Set;
import lombok.Builder;
import lombok.NonNull;

@Builder
@Schema(description = "Data Transfer Object (DTO) representing a telemetry request")
public record TelemetryRequest(

        @Schema(description = "Telemetry endpoint URL")
        @NonNull @NotBlank String endpoint,

        @Schema(description = "Protocol used for communication")
        @NotNull TelemetryProtocolEnum protocol,

        @Schema(description = "Telemetry provider")
        @NotNull TelemetryProviderEnum provider,

        @Schema(description = "Set of telemetry data types")
        @NotNull @NotEmpty Set<TelemetryTypeEnum> types,

        @Schema(description = "Single secret associated with the telemetry configuration")
        @Valid @NotNull SecretDto secret) {
}
