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
package com.nvidia.nvcf.rest.account.dto;

import static com.nvidia.nvcf.util.NvcfConstants.ACCOUNT_NAME_REGEX;

import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import com.nvidia.nvcf.util.NvcfConstants;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.util.List;
import lombok.Builder;

@Builder
@Schema(description = "Request payload to provision an account.")
public record CreateAccountRequest(
        @Schema(description = "Human readable account/customer name")
        @Pattern(regexp = ACCOUNT_NAME_REGEX,
                message = "Invalid account name: Must conform to regex " + ACCOUNT_NAME_REGEX)
        @Size(min = 4, max = 36, message = "Invalid account name: Must be 4 - 36 characters long")
        @NotBlank String name,

        @Schema(description = "Client Id")
        @Nullable String adminClientId,

        @Schema(description = "List of system provisioned registry credentials for the account")
        @Nullable
        List<@Valid RegistryCredentialDto> registryCredentials,

        @Schema(description = "Max number of functions allowed")
        @Nullable
        @Min(1)
        Integer maxFunctionsAllowed,

        @Schema(description = "Max number of tasks allowed")
        @Nullable
        @Min(1)
        Integer maxTasksAllowed,

        @Schema(description = "Max number of telemetries allowed")
        @Nullable
        @Min(1)
        @Max(NvcfConstants.DEFAULT_MAX_TELEMETRIES_ALLOWED)
        Integer maxTelemetriesAllowed,

        @Schema(description = "Max number of registry credentials allowed")
        @Nullable
        @Min(1)
        @Max(NvcfConstants.DEFAULT_MAX_REGISTRY_CREDENTIALS_ALLOWED)
        Integer maxRegistryCredentialsAllowed) {

}
