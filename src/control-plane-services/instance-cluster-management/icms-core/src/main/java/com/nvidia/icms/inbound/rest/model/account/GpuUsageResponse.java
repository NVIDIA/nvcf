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
package com.nvidia.icms.inbound.rest.model.account;

import com.fasterxml.jackson.annotation.JsonProperty;
import io.swagger.v3.oas.annotations.media.Schema;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Builder
@Schema(description = "GPU usage response containing GPU instances and their status across regions")
public class GpuUsageResponse {

    @JsonProperty("Gpus")
    @Schema(description = "List of GPUs and their instances")
    private List<Gpu> gpus;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class Gpu {

        @Schema(description = "Name of the GPU")
        private String gpuName;

        @Schema(description = "List of instances for this GPU")
        private List<Instance> instances;
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class Instance {

        @Schema(description = "Name of the instance")
        private String instanceName;

        @Schema(description = "List of regions where this instance is available")
        private List<Region> regions;
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class Region {

        @Schema(description = "Name of the region")
        private String regionName;

        @Schema(description = "List of clusters in this region")
        private List<Cluster> clusters;
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class Cluster {

        @Schema(description = "Unique identifier of the cluster")
        private String clusterId;

        @Schema(description = "Cluster group name")
        private String clusterGroupName;

        @Schema(description = "Status of the cluster")
        private Status status;
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class Status {

        @Schema(description = "Number of active instances in the cluster")
        private Integer activeInstances;

        @Schema(description = "Number of pending instances in the cluster")
        private Integer pendingInstances;
    }
}
