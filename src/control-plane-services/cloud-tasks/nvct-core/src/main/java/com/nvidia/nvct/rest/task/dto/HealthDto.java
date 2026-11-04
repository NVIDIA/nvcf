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

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotBlank;
import lombok.Builder;

@Builder
@Schema(types = {"object"}, description = "Data Transfer Object(DTO) representing instance health")
public record HealthDto(
        @Schema(description = "GPU Type as per SDD")
        @NotBlank String gpu,

        @Schema(description = "Backend/CSP where the GPU powered instance will be launched")
        @NotBlank String backend,

        @Schema(description = "Instance type")
        @Nullable String instanceType,

        @Schema(description = "Deployment error")
        @NotBlank String error) {
}
