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
package com.nvidia.nvcf.rest.function.management.dto;

import io.swagger.v3.oas.annotations.media.Schema;
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

    @Schema(description = "Function executing on the instance")
    private UUID functionId;

    @Schema(description = "Function version executing on the instance")
    private UUID functionVersionId;

    @Schema(description = "GPU instance-type powering the instance")
    private String instanceType;

    @Schema(description = "Instance status")
    private InstanceStatusEnum instanceStatus;

    @Schema(description = "ICMS request-id used to launch this instance")
    private UUID icmsRequestId;

    @Schema(description = "NVIDIA Cloud Account Id that owns the function running on the instance")
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
