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
package com.nvidia.nvcf.rest.misc.dto;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import java.util.Map;
import lombok.Builder;

@Builder
@Schema(description = "Sidecars configuration")
public record SidecarsDto(
        @Schema(description = "Inference container image")
        @NotBlank String inferenceContainer,

        @Schema(description = "Init container image")
        @NotBlank String initContainer,

        @Schema(description = "Utils container images")
        @NotEmpty Map<String, String> utilsContainerImage,

        @Schema(description = "OTEL container image")
        @NotBlank String otelContainer,

        @Schema(description = "NICLLS container image")
        @NotBlank String nicllsContainer,

        @Schema(description = "ESS agent container image")
        @NotBlank String essAgentContainer,

        @Schema(description = "OTEL collector container image")
        @NotBlank String otelCollectorContainer) {
}
