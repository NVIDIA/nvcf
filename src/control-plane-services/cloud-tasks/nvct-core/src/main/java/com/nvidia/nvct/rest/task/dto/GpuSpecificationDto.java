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

import tools.jackson.databind.node.ObjectNode;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import java.util.Set;
import lombok.Builder;

@Builder
@Schema(description = "Data Transfer Object(DTO) representing GPU specification for a Task.")
public record GpuSpecificationDto(
        @Schema(description = "GPU name from the cluster")
        @NotBlank String gpu,

        @Schema(description = "Backend/CSP where the GPU powered instance will be launched")
        @Nullable String backend,

        @Schema(description = """
                Specific clusters within instance or worker node powered by the selected
                instance-type to launch the Task.
                """)
        @Nullable Set<String> clusters,

        @Schema(description = "Instance type, based on GPU, assigned to a Worker")
        @NotBlank String instanceType,

        @Schema(description = "Optional configuration field typically used with Helm Charts " +
                "to substitute placeholders in values.yaml")
        @Nullable ObjectNode configuration,

        @Schema(description = "Helm validation policy cluster attributes")
        @Nullable @Valid HelmValidationPolicyDto helmValidationPolicy) {
}
