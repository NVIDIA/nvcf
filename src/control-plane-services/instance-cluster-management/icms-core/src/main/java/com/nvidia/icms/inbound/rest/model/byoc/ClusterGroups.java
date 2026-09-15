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

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
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
public class ClusterGroups {

    @JsonProperty("id")
    @Schema(description = "Id of the cluster group")
    String id;

    @JsonProperty("name")
    @Schema(description = "Name of the cluster group")
    String name;

    @JsonProperty("ncaId")
    @Schema(description = "Nvidia Cloud Account Id used to create cluster group")
    String ncaId;

    @JsonProperty("authorizedNcaIds")
    @Schema(description = "AuthorizedNcaIds for the cluster cluster group")
    Set<String> authorizedNcaIds;

    @JsonProperty("gpus")
    @Schema(description = "Gpus present in cluster group")
    private Set<GpuResponse> gpus;

    @JsonProperty("clusters")
    @Schema(description = "Cluster present in cluster group")
    Set<ClustersResponse> clusters;

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class ClustersResponse {

        @JsonProperty("k8sVersion")
        @Schema(description = "K8s version on cluster")
        String k8sVersion;

        @JsonProperty("id")
        @Schema(description = "Id of the cluster")
        String id;

        @JsonProperty("name")
        @Schema(description = "Name of the cluster")
        String name;
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    public static class GpuResponse {

        @JsonProperty("name")
        @Schema(description = "Name of the Gpu")
        String name;

        @JsonProperty("instanceTypes")
        @Schema(description = "List of instance types for that Gpu")
        Set<InstanceTypeResponse> instanceTypes;
    }

    @Data
    @AllArgsConstructor
    @NoArgsConstructor
    @Builder
    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class InstanceTypeResponse {

        @JsonProperty("name")
        @Schema(description = "Name of the instance type")
        String name;

        @JsonProperty("value")
        @Schema(description = "Value of the instance type")
        String value;

        @JsonProperty("description")
        @Schema(description = "Description of the instance type")
        String description;

        @JsonProperty("default")
        @Schema(description = "Indicates if its the default instance type for the GPU")
        Boolean isDefault;

        @JsonProperty("nodeType")
        @Schema(description = "node type: SINGLE | MULTI")
        @NonNull
        NodeTypeEnum nodeType;
    }
}
