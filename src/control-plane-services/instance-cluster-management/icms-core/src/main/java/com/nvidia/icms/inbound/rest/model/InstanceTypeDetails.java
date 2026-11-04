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

import com.fasterxml.jackson.annotation.JsonProperty;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.inbound.rest.model.byoc.NodeTypeEnum;
import io.swagger.v3.oas.annotations.media.Schema;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Builder
public class InstanceTypeDetails {

    @JsonProperty("name")
    @Schema(description = "Name of the instance type")
    String name;

    @JsonProperty("value")
    @Schema(description = "Value of the instance type")
    String value;

    @JsonProperty("description")
    @Schema(description = "Description of the instance type")
    String description;

    @JsonProperty("cpuCores")
    @Schema(description = "Number of cpu cores for the instance type")
    int cpuCores;

    @JsonProperty("systemMemory")
    @Schema(description = "Memory of the System. e.g. 24G")
    String systemMemory;

    @JsonProperty("gpuMemory")
    @Schema(description = "Memory of the GPU. e.g. 24G")
    String gpuMemory;

    @JsonProperty("gpuCount")
    @Schema(description = "Number of GPUs to be attached to the worker pod. " +
            "Supported values 1, 2, 4, 8 (Default value is 1)")
    int gpuCount = 1;

    @JsonProperty("availableCapacity")
    @Schema(description = "Number of instances can be created with this instanceType")
    int availableCapacity = 0;

    @JsonProperty("clusters")
    @Schema(description = "Clusters where this instance type is available")
    Set<String> clusters;

    @JsonProperty("regions")
    @Schema(description = "Regions where this instance type is available")
    Set<String> regions;

    @JsonProperty("attributes")
    @Schema(description = "Attributes available for this instance type")
    Set<String> attributes;

    @JsonProperty("gpuName")
    @Schema(description = "GPU name e.g. A100")
    String gpuName;

    @JsonProperty("defaultable")
    @Schema(description = "Indicates if its the default instance type for the GPU in it's cluster")
    Boolean defaultable;

    @JsonProperty("cpuArch")
    @Schema(description = "Architecture details of the CPU")
    String cpuArch;

    @JsonProperty("os")
    @Schema(description = "Operating system details")
    String os;

    @JsonProperty("driverVersion")
    @Schema(description = "GPU driver version")
    String driverVersion;

    @JsonProperty("storage")
    @Schema(description = "The amount of available storage, e.g. 80G")
    String storage;

    @JsonProperty("nodeType")
    @Schema(description = "node type: SINGLE | MULTI")
    @NonNull
    NodeTypeEnum nodeType;
}
