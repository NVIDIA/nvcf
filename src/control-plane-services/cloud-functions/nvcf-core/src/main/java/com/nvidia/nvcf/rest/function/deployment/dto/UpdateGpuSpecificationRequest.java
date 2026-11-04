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

import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto.ValidAutoscalingConfiguration;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import jakarta.validation.constraints.Positive;
import jakarta.validation.constraints.PositiveOrZero;
import lombok.Builder;

@Builder
@Schema(description = "Request to update single function deployment GPU Specification")
public record UpdateGpuSpecificationRequest(
        @Schema(description = "Maximum number of instances for the deployment")
        @Nullable @Positive Integer maxInstances,

        @Schema(description = "Minimum number of instances for the deployment")
        @Nullable @PositiveOrZero Integer minInstances,

        @Schema(description = "Customizable Autoscaling configuration")
        @Nullable @Valid @ValidAutoscalingConfiguration
        AutoscalingConfigurationDto autoscalingConfiguration,

        @Schema(description = "Autoscaling configuration policy. " +
                "CUSTOM_CONFIGURATION (default): use the provided autoscalingConfig. " +
                "PLATFORM_CONFIGURATION: remove custom config and use platform defaults.")
        @Nullable
        AutoscalingConfigurationPolicyEnum autoscalingConfigurationPolicy) {
}
