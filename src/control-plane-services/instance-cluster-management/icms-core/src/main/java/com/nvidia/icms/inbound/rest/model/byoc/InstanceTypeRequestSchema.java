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
import java.util.Objects;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Builder
@Schema(description = "Instance type request schema")
public class InstanceTypeRequestSchema {

    @Schema(description = "Number of cpu cores for the instance type")
    int cpuCores;

    @Schema(description = "Memory of the System. e.g. 24G")
    String systemMemory;

    @Schema(description = "Memory of the GPU. e.g. 24G")
    String gpuMemory;

    @Schema(description = "Number of GPUs to be attached to the worker pod. " +
            "Supported values 1, 2, 4, 8 (Default value is 1)")
    int gpuCount = 1;

    @Valid
    @NotBlank(message = "Instance name must not blank")
    @Schema(description = "Name of the instance type")
    String name;

    @Schema(description = "Description of the instance type")
    String description;

    @JsonProperty("default")
    @Schema(description = "Indicates if its the default instance type for the GPU")
    Boolean isDefault;

    @Valid
    @NotBlank(message = "Instance value must not blank")
    @Schema(description = "Value of the instance type")
    String value;

    @Schema(description = "Architecture details of the CPU")
    String cpuArch;

    @Schema(description = "Operating system details")
    String os;

    @Schema(description = "GPU driver version")
    String driverVersion;

    @Schema(description = "The amount of available storage, e.g. 80G")
    String storage;

    @Schema(description = "Type of the node expected values SINGLE | MULTI default value: SINGLE")
    @Nullable
    NodeTypeEnum nodeType;

    /**
     * @return default value of {@link NodeTypeEnum} if not provided by cluster agent
     */
    public NodeTypeEnum getNodeType() {
        if (this.nodeType == null) {
            return NodeTypeEnum.SINGLE;
        }
        return this.nodeType;
    }
}
