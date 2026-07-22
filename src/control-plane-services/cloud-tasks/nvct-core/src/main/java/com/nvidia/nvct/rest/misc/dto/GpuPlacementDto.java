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
package com.nvidia.nvct.rest.misc.dto;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.PositiveOrZero;
import lombok.Builder;

@Builder
@Schema(description = "Data Transfer Object(DTO) representing GPU location, i.e. " +
        "cluster, cluster group, region")
public record GpuPlacementDto(
        @Schema(description = "Unique cluster id. Note: cluster name may not be unique.")
        @NotBlank
        String clusterId,

        @Schema(description = "Cluster name")
        @NotBlank
        String cluster,

        @Schema(description = "Unique cluster group id.")
        @NotBlank
        String clusterGroupId,

        @Schema(description = "Cluster group name")
        @NotBlank
        String clusterGroup,

        @Schema(description = "Cluster provider")
        @NotBlank
        String cloudProvider,

        @Schema(description = "Cluster region where gpu is located")
        @NotBlank
        String region,

        @Schema(description = "Max usage of gpu in this cluster")
        @PositiveOrZero
        int currentMaxUsage,

        @Schema(description = "Min usage of gpu in this cluster")
        @PositiveOrZero
        int currentMinUsage) {
}
