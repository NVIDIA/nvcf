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

import com.nvidia.icms.inbound.rest.model.byoc.ClusterStatusEnum;
import com.nvidia.icms.inbound.rest.model.byoc.GpuRequestSchema;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

import jakarta.annotation.Nullable;
import java.util.Set;

@Data
@AllArgsConstructor
@NoArgsConstructor
@Schema(description = "Cluster GPU registration request")
@Builder
public class ClusterRegistrationRequest {

    @NotNull(message = "status must not be null")
    @Schema(description = "Status of cluster")
    private ClusterStatusEnum status;

    @Nullable
    @Schema(description = "Attributes related to the cluster")
    Set<String> attributes;

    @NotBlank(message = "region must be provided")
    @Schema(description = "Region of the cluster")
    String region;

    @Valid
    @Schema(description = "GPUs supported in cluster")
    private Set<GpuRequestSchema> gpus;

    @Nullable
    @Schema(description = "List of authorized NCA IDs that can create instances "
            + "in this zone. Use '*' to allow all organizations (default for NP zones). "
            + "For zones with restricted access, specify specific NCA IDs to restrict access "
            + "to only those organizations. "
            + "If '*' is specified, it must be the only entry in the list.")
    private Set<String> authorizedNCAIds;
}
