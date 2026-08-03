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

import static com.nvidia.nvct.util.NvctConstants.HOSTNAME_REGEX;
import static com.nvidia.nvct.util.NvctConstants.MAX_HOSTNAME_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAGS_COUNT;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAG_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.TAG_REGEX;

import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.util.Set;
import lombok.Builder;
import org.hibernate.validator.constraints.Length;

@Builder(toBuilder = true)
@Schema(description = "DTO of a registry credential")
public record RegistryCredentialDto(
        @Schema(description = "Registry hostname")
        @Pattern(regexp = HOSTNAME_REGEX,
                message = "Invalid hostname: Must conform to regex " + HOSTNAME_REGEX)
        @Size(min = 1, max = MAX_HOSTNAME_LENGTH,
                message = "Invalid hostname: Must be 1 - " + MAX_HOSTNAME_LENGTH + " chars long")
        @NotBlank String registryHostname,

        @Schema(description = "Registry credential - secret value must be base64 encoded " +
                "string in username:password format")
        @NotNull SecretDto secret,

        @Schema(description = "Artifact types")
        @NotNull @NotEmpty Set<ArtifactTypeEnum> artifactTypes,

        @Nullable
        @Schema(description = "Optional set of tags")
        @Valid
        @Size(max = MAX_TAGS_COUNT,
              message = "Maximum number of tags of " + MAX_TAGS_COUNT + " is exceeded.")
        Set<@Length(max = MAX_TAG_LENGTH,
                    message = "Maximum tag length of " + MAX_TAG_LENGTH + " is exceeded.")
            @Pattern(regexp = TAG_REGEX) String> tags,

        @Nullable
        @Schema(description = "Optional registry credential description")
        String description) {
}
