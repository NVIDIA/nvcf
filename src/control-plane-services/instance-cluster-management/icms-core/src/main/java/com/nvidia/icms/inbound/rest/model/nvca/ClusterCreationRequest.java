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

import com.fasterxml.jackson.annotation.JsonAlias;
import com.fasterxml.jackson.annotation.JsonProperty;
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
import org.apache.commons.lang3.StringUtils;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Cluster creation request")
@Builder
public class ClusterCreationRequest {

    @NotBlank(message = "clusterName must be provided")
    @Schema(description = "Name of the cluster")
    String clusterName;

    // FYI: In bart flow, we have clusterGroup which corresponds to clusterGroupName in NVCA flow
    @NotBlank(message = "clusterGroupName must be provided")
    @Schema(description = "Group to which the custer belongs")
    String clusterGroupName;

    // Based on UI clusterDescription is optional
    @Schema(description = "Description of the cluster")
    String clusterDescription;

    // Note: In SDD, we have mentioned ncaID (all ID / NCA are in caps)
    @NotBlank(message = "ncaId must be provided")
    @Schema(description = "NVIDIA Cloud Account ID")
    String ncaId;

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

    @Deprecated
    @Schema(description = "OAuth client ID used by cluster (deprecated, use oAuthClientId)")
    String ssaClientId;

    // Canonical inbound name is oAuthClientId. Accept the Jackson-2 legacy wire name
    // (oauthClientId) and the accidental Jackson-3 name (OAuthClientId) so no existing
    // caller breaks. lombok.config copies these onto the generated setter/builder.
    @Nullable
    @JsonProperty("oAuthClientId")
    @JsonAlias({"oauthClientId", "OAuthClientId"})
    @Schema(description = "OAuth client ID used by cluster")
    String oAuthClientId;

    public String getEffectiveOAuthClientId() {
        return StringUtils.isNotBlank(oAuthClientId) ? oAuthClientId : ssaClientId;
    }

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

    @Nullable
    @Schema(description = "JWKS JSON for OIDC/PSAT cluster identity verification")
    String jwks;

    @Nullable
    @Schema(description = "OIDC issuer URL for the cluster")
    String oidcIssuer;
}
