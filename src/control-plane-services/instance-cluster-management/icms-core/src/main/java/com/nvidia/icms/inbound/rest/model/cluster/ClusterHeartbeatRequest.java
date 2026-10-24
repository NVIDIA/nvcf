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

import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import java.util.Map;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Cluster heartbeat request")
@Builder
public class ClusterHeartbeatRequest {

    @NotNull(message = "status must not be null")
    @Schema(description = "Status of the cluster")
    CloudHealthStatus status;

    @NotNull(message = "gpuUsage must not be null")
    @Schema(description = "Map of GPU name to GPU usage stats of the cluster")
    Map<String, ClusterCapacityStats> gpuUsage;

    @Nullable
    @Schema(description = "Reserved GPU usage and availability for the cluster", name = "reservations")
    List<GpuReservation> reservations;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Schema(description = "Cluster usage stats")
    @Builder
    public static class ClusterCapacityStats {

        @Schema(description = "GPUs currently allocated to workers in cluster")
        int allocated;

        @Schema(description = "GPUs currently available in cluster to be allocated")
        int available;
    }
}
