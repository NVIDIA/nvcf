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
package com.nvidia.icms.inbound.rest.model.byoc;


import com.fasterxml.jackson.annotation.JsonProperty;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Instance type")
@Builder
public class InstanceType {

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
            "Supported values -1, 1, 2, 4, 8 (Default value is 1)")
    int gpuCount = 1;

    @Valid
    @NotBlank(message = "Instance name must not blank")
    @JsonProperty("name")
    @Schema(description = "Name of the instance type")
    String name;

    @JsonProperty("description")
    @Schema(description = "Description of the instance type")
    String description;

    @JsonProperty("default")
    @Schema(description = "Indicates if its the default instance type for the GPU")
    Boolean isDefault;

    @Valid
    @NotBlank(message = "Instance value must not blank")
    @JsonProperty("value")
    @Schema(description = "Value of the instance type")
    String value;

}

