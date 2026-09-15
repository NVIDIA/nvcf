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
package com.nvidia.nvct.service.account.dto;

import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.Set;
import java.util.UUID;
import lombok.Builder;

// Mirrors cloud-functions TempRegistryCredentialDetailsDto so both sides of the Get Account
// Details contract stay in sync. The only intentional divergence is secret, which is nullable
// here because NVCT no longer reads it from the response and instead resolves it from ESS by
// registryCredentialId.
@Builder(toBuilder = true)
@Schema(description = "DTO of a registry credential")
public record RegistryCredentialDetailsDto(
        @Schema(description = "Registry Credential Id")
        @NotNull UUID registryCredentialId,

        @Schema(description = "NVIDIA Cloud Account Id owning the Registry Credential")
        @NotBlank String ncaId,

        @Schema(description = "Registry Credential name")
        @NotBlank String registryCredentialName,

        @Schema(description = "Recognized registry name")
        @NotBlank String registryName,

        @Schema(description = "Registry hostname")
        @NotBlank String registryHostname,

        @Schema(description = "Registry type")
        @NotNull @NotEmpty Set<ArtifactTypeEnum> artifactTypes,

        @Schema(description = "Optional set of tags")
        @Nullable Set<String> tags,

        @Schema(description = "Registry credential description")
        @Nullable String description,

        @Schema(description = "Registry credential provisioned by system or user")
        @NotNull ProvisionedByEnum provisionedBy,

        @Schema(description = "Optional registry credential key type")
        @Nullable String keyType,

        @Schema(description = "Timestamp for last registry credential update")
        @NotNull Instant lastUpdatedAt,

        @Schema(description = "Timestamp for registry credential creation")
        @NotNull Instant createdAt,

        @Nullable
        @Schema(description = "Registry credential secret fetched from ESS by id; not populated " +
                "from the account details response")
        SecretDto secret) {
}
