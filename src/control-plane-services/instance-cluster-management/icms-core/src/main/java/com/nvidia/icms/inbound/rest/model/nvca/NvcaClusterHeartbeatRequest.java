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
package com.nvidia.icms.inbound.rest.model.nvca;

import com.nvidia.icms.inbound.rest.model.CloudHealthStatus;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotNull;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

import java.util.Map;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Cluster heartbeat request")
@Builder
public class NvcaClusterHeartbeatRequest {

    @NotNull(message = "status must not be null")
    @Schema(description = "Status of the cluster")
    CloudHealthStatus status;

    @Schema(description = "Upgrade status of the cluster")
    String upgradeStatus;

    @NotNull(message = "gpuUsage must not be null")
    @Schema(description = "Map of GPU name to GPU usage stats of the cluster")
    Map<String, NvcaClusterCapacityStats> gpuUsage;

    @Nullable
    @Schema(description = "NCAID of cluster owner")
    String clusterOwnerNcaId;

    @Nullable
    @Schema(description = "NVCA agent version")
    String nvcaAgentVersion;

    @Nullable
    @Schema(description = "NVCA operator version")
    String nvcaOperatorVersion;

    @Nullable
    @Schema(description = "cluster name")
    String clusterName;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Schema(description = "Cluster usage stats")
    @Builder
    public static class NvcaClusterCapacityStats {

        @Schema(description = "Total GPUs in the cluster")
        int capacity;

        @Schema(description = "GPUs currently allocated to workers in cluster")
        int allocated;

        @Schema(description = "GPUs currently available in cluster to be allocated")
        int available;
    }
}
