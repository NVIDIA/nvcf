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

import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import java.util.Set;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Cluster GPU registration request")
@Builder
public class NvcaRegistrationRequest {

    @NotNull(message = "status must not be null")
    @Schema(description = "Status of cluster")
    private ClusterStatusEnum status;

    @NotBlank(message = "k8sVersion must be provided")
    @Schema(description = "K8s version of cluster")
    private String k8sVersion;

    @Nullable
    @Valid
    @Schema(description = "GPUs supported in cluster")
    private Set<GpuRequestSchema> gpus;

    @Nullable
    @Schema(description = "Flag showing the cluster could be selected by targeting request")
    private Boolean allowClusterTargeting;

    @Nullable
    @Schema(description = "Flag to enable tasks specific cluster creation queues")
    private Boolean allowTaskClusterCreationQueues;

    @Nullable
    @Schema(description = "Version of the NVCA cluster")
    private String nvcaVersion;

    @Nullable
    @Schema(description = "JWKS (JSON Web Key Set) for cluster OIDC identity verification")
    private String jwks;

    @Nullable
    @Schema(description = "OIDC issuer URL for the cluster API server")
    private String oidcIssuer;
}
