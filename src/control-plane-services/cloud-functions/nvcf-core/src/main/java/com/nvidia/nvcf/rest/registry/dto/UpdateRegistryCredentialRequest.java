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
import java.util.Set;
import lombok.Builder;

@Builder
@Schema(description = "Request body to update registry credential")
public record UpdateRegistryCredentialRequest(
        @Schema(description = "Registry credential - secret value must be base64 encoded " +
                "string in username:password format")
        @Nullable SecretDto secret,

        @Schema(description = "Optional artifact types to be added to the existing ones")
        @Nullable Set<ArtifactTypeEnum> artifactTypeEnums) {
}
