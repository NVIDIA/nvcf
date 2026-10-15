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
package com.nvidia.icms.inbound.rest.model.cluster;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "GPU reservation information")
@Builder
public class GpuReservation {

    @NotNull(message = "reservationId must not be null")
    @Schema(description = "ID for  reservation")
    UUID reservationId;

    @NotNull(message = "ncaId must not be null")
    @Schema(description = "NCAID of reservation owner")
    String ncaId;

    @NotNull(message = "gpuType must not be null")
    @Schema(description = "Type of reserved GPU")
    String gpuType;

    @NotNull(message = "gpuReserved must not be null")
    @Schema(description = "Number of reserved GPUs")
    Integer gpuReserved;

    @NotNull(message = "gpuAvailable must not be null")
    @Schema(description = "Number of available reserved GPUs")
    Double gpuAvailable;

    @NotNull(message = "startTime must not be null")
    @Schema(description = "Reservation start time")
    Instant reservationStartsUtc;

    @NotNull(message = "endTime must not be null")
    @Schema(description = "Reservation end time")
    Instant reservationEndsUtc;

    @Nullable
    @Schema(description = "Name describing reservation")
    String name;

    @NotNull(message = "gpuUsageByInstanceType must not be null")
    @Schema(description = "Reserved GPU usage and availability per instance type")
    GpuUsageByInstanceType gpuUsageByInstanceType;
}
