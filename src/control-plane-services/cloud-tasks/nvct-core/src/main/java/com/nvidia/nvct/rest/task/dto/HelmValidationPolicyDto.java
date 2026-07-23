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
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Size;
import java.util.List;
import lombok.Builder;

@Builder
@Schema(types = {"object"}, description = "Data Transfer Object(DTO) representing Helm validation policy")
public record HelmValidationPolicyDto (
        @Schema(description = "Helm validation policy name.")
        @NotNull
        ValidationPolicyNameEnum name,

        @Schema(description = """
                An API Group in Kubernetes is a collection of related functionality.
                When present, must contain at least one entry; each entry must be a valid
                KubernetesType (group, version, kind).
                """)
        @Valid @Nullable @Size(min = 1)
        List<@NotNull KubernetesType> extraKubernetesTypes) {

    @Builder
    @Schema(types = {"object"})
    public record KubernetesType(
            @Schema(description = "Name of API Group")
            @NotBlank
            String group,

            @Schema(description = "Version of API Group")
            @NotBlank
            String version,

            @Schema(description = "API Group resource or Kind")
            @NotBlank
            String kind) {
    }
}
