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
package com.nvidia.icms.inbound.rest.model;

import com.nvidia.icms.inbound.rest.model.nvct.ResultHandlingStrategy;
import io.swagger.v3.oas.annotations.media.Schema;
import java.time.Duration;
import java.util.Set;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.ToString;

@Builder(toBuilder = true)
@NoArgsConstructor
@AllArgsConstructor
@Data
@ToString(onlyExplicitlyIncluded = true)

public class CreateSpotInstanceLaunchSpecificationApiModel {

    @Schema(hidden = true)
    String instanceType;

    @Schema(hidden = true)
    String containerImage;

    @Schema(hidden = true)
    String environment;

    @Schema(hidden = true)
    CreateSpotInstanceLaunchSpecificationPlacementApiModel placement;

    @Schema(hidden = true)
    String backend;

    @Schema(hidden = true)
    String gpu;

    @Schema(hidden = true)
    String ncaId;

    @Schema(hidden = true)
    String helmChart;

    @Schema(hidden = true)
    String configuration;

    @Schema(hidden = true)
    String models;

    @Schema(hidden = true)
    Boolean cacheArtifacts;

    @Schema(hidden = true)
    String cacheHandle;

    @Schema(hidden = true)
    Long cacheSize;

    @Schema(hidden = true)
    Set<String> clusters;

    @Schema(hidden = true)
    Set<String> regions;

    @Schema(hidden = true)
    Set<String> attributes;

    @Schema(hidden = true)
    Duration maxRuntimeDuration;

    @Schema(hidden = true)
    Duration maxQueuedDuration;

    @Schema(hidden = true)
    Duration terminationGracePeriodDuration;

    @Schema(hidden = true)
    ResultHandlingStrategy resultHandlingStrategy;

    @Schema(hidden = true)
    String telemetries;

    @Schema(hidden = true)
    UUID deploymentId;

    @Schema(hidden = true)
    UUID gpuSpecificationId;

}
