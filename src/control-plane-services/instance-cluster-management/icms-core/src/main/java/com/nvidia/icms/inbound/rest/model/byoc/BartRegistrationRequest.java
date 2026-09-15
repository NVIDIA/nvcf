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
import jakarta.validation.constraints.NotNull;
import java.util.Set;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Backend Registration request schema")
public class BartRegistrationRequest {

    @NotBlank
    @JsonProperty("ncaId")
    @Schema(description = "NVIDIA Cloud Account ID")
    String ncaId;

    @NotBlank
    @JsonProperty("clusterName")
    @Schema(description = "Name of the cluster")
    String clusterName;

    @NotBlank
    @JsonProperty("clusterDescription")
    @Schema(description = "Description of the cluster")
    String clusterDescription;

    @JsonProperty("clusterGroup")
    @Schema(description = "Group to which the custer belongs")
    String clusterGroup;

    @Nullable
    @JsonProperty("authorizedNcaIds")
    @Schema(description = "AuthorizedNcaIds for the cluster")
    Set<String> authorizedNcaIds;

    @NotNull
    @JsonProperty("clusterProvider")
    @Schema(description = "Cluster Provider")
    ClusterProviderEnum clusterProvider;

    @NotNull
    @JsonProperty("status")
    @Schema(description = "Cluster Status")
    ClusterStatusEnum status;

    @Valid
    @NotNull
    @JsonProperty("gpus")
    @Schema(description = "Gpus for the backend")
    Set<Gpu> gpus;

    @JsonProperty("k8sVersion")
    @Schema(description = "K8s version on cluster")
    @NotBlank
    String k8sVersion;
}
