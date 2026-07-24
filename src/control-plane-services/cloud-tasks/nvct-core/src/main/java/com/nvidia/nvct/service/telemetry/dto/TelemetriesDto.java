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
package com.nvidia.nvct.service.telemetry.dto;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import java.util.UUID;
import lombok.Builder;

@Builder
@Schema(types = {"object"}, description = "Telemetry configuration for logs, metrics, and traces.")
public record TelemetriesDto(
        @Nullable
        @Schema(description = "UUID representing the logs telemetry.")
        UUID logsTelemetryId,

        @Nullable
        @Schema(description = "UUID representing the metrics telemetry.")
        UUID metricsTelemetryId,

        @Nullable
        @Schema(description = "UUID representing the traces telemetry.")
        UUID tracesTelemetryId) {
}
