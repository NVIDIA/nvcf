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

import com.nvidia.nvcf.rest.function.deployment.dto.ScalingStatusEnum;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.PositiveOrZero;
import lombok.Builder;

@Builder
@Schema(description = "Response body containing the result of autoscaler call")
public record AutoscalerResponse(
        @Schema(description = "Current number of active instances")
        @PositiveOrZero @NotNull Integer activeInstances,

        @Schema(description = "Current number of pending instances")
        @PositiveOrZero @NotNull Integer pendingInstances,

        @Schema(description = "Number of instances requested for allocation")
        @PositiveOrZero @NotNull Integer allocatingInstances,

        @Schema(description = "Number of instances requested for termination")
        @PositiveOrZero @NotNull Integer terminatingInstances,

        @Schema(description = "Function status")
        @NotNull FunctionStatusEnum functionStatus,

        @Schema(description = "Resulting status of scaling request" )
        @NotNull ScalingStatusEnum scalingStatus
) {
}
