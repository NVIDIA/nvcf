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

import com.nvidia.icms.inbound.rest.model.byoc.ClusterProviderEnum;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.Set;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Cluster update request")
@Builder
public class ClusterUpdateRequest {

    @NotBlank(message = "clusterGroupName must be provided")
    @Schema(description = "Group to which the custer belongs")
    String clusterGroupName;

    @Schema(description = "Description of the cluster")
    String clusterDescription;

    @Nullable
    @Schema(description = "authorizedNCAIds for the cluster")
    Set<String> authorizedNCAIds;

    // Note: In BART flow, we have clusterProvider which corresponds to cloudProvider
    @NotNull(message = "cloudProvider must not be null")
    @Schema(description = "Cloud Provider for the cluster")
    ClusterProviderEnum cloudProvider;

    @Nullable
    @Schema(description = "Capabilities of the cluster")
    Set<String> capabilities;

    @Nullable
    @Schema(description = "Attributes related to the cluster")
    Set<String> attributes;

    @Nullable
    @Valid
    @Schema(description = "GPUs supported in cluster")
    Set<GpuRequestSchema> gpus;

    @NotBlank(message = "nvcaVersion must be provided")
    @Schema(description = "Nvca version")
    String nvcaVersion;

    @NotBlank(message = "region must be provided")
    @Schema(description = "Region of the cluster")
    String region;

    @Nullable
    @Schema(description = "Custom attributes related to the cluster")
    Set<String> customAttributes;

    @Nullable
    @Schema(description = "Cluster key id, each cluster gets a unique cluster key which has its cluster id")
    String clusterKeyId;

    @Nullable
    @Schema(description = "Cluster detects the cluster management type e.g: helm-managed, ngc-managed")
    String clusterSource;

    @Nullable
    @Schema(description = "Advanced BYOC cluster configurations as key-value string map")
    Map<String, String> clusterConfigurations;

    @Nullable
    @Schema(description = "Advanced BYOC cluster configuration files as base64-encoded string map")
    Map<String, String> clusterConfigurationFiles;
}


