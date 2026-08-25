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
package com.nvidia.nvcf.rest.registry.dto;

import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.util.Set;
import java.util.UUID;
import lombok.Builder;

/**
 * Registry credential returned in the account details response. Unlike {@link RegistryCredentialDto}
 * it carries the {@code registryCredentialId} and is used only in the hidden account details
 * endpoint consumed by Cloud Tasks. Keeping it separate avoids exposing the id on the account
 * provisioning request that reuses {@link RegistryCredentialDto}.
 */
@Builder
@Schema(description = "Registry credential returned in account details, including its id")
public record RegistryCredentialDtoWithID(
        @Schema(description = "Registry Credential Id")
        @NotNull UUID registryCredentialId,

        @Schema(description = "Registry hostname")
        @NotBlank String registryHostname,

        @Schema(description = "Registry credential - secret value must be base64 encoded " +
                "string in username:password format")
        @NotNull SecretDto secret,

        @Schema(description = "Artifact types")
        @NotNull @NotEmpty Set<ArtifactTypeEnum> artifactTypes,

        @Nullable
        @Schema(description = "Optional set of tags")
        Set<String> tags,

        @Nullable
        @Schema(description = "Optional registry credential description")
        String description) {
}
