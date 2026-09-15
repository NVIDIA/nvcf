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

import io.swagger.v3.oas.annotations.media.Schema;

/**
 * Policy for autoscaling configuration.
 */
@Schema(description = "Policy for autoscaling configuration")
public enum AutoscalingConfigurationPolicyEnum {
    
    @Schema(description = "Use custom autoscaling configuration provided by the user")
    CUSTOM_CONFIGURATION,
    
    @Schema(description = "Use platform-managed autoscaling configuration - removes custom config)")
    PLATFORM_CONFIGURATION
}
