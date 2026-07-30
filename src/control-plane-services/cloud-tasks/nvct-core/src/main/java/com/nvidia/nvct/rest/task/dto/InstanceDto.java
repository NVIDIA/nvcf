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
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;

@Builder
@Data
@NoArgsConstructor
@AllArgsConstructor
@Schema(description = "Data Transfer Object(DTO) representing an instance")
public class InstanceDto {

    @Schema(description = "Unique id of the instance")
    private String instanceId;

    @Schema(description = "Task executing on the instance")
    private UUID taskId;

    @Schema(description = "GPU instance-type powering the instance")
    private String instanceType;

    @Schema(description = "Instance state")
    private InstanceStateEnum instanceState;

    @Schema(description = "ICMS request-id used to launch this instance")
    private UUID icmsRequestId;

    @Schema(description = "NVIDIA Cloud Account Id that owns the Task running on the instance")
    private String ncaId;

    @Schema(description = "GPU name powering the instance")
    private String gpu;

    @Schema(description = "Backend where the instance is running")
    private String backend;

    @Schema(description = "Location such as zone name or region where the instance is running")
    private String location;

    @Schema(description = "Instance creation timestamp")
    private Instant instanceCreatedAt;

    @Schema(description = "Instance's last updated timestamp")
    private Instant instanceUpdatedAt;

}
