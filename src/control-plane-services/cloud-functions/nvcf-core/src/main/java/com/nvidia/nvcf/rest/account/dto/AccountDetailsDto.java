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

import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.List;
import java.util.Set;
import lombok.Builder;

@Builder
@Schema(description = "Data Transfer Object(DTO) representing an account")
public record AccountDetailsDto(

        @Schema(description = "NVIDIA Cloud Account id")
        @NotNull String ncaId,

        @Schema(description = "Client Ids associated with the NVIDIA Cloud Account")
        @Nullable Set<String> clientIds,

        @Schema(description = "Account/customer name")
        @NotNull String name,

        @Schema(description = "Account Telemetry configuration")
        @Nullable List<TelemetryDto> telemetries,

        @Schema(description = "Registry credentials associated with the account")
        @Nullable List<RegistryCredentialDto> registryCredentials,

        @Schema(description = "Maximum number of functions allowed for Account")
        @NotNull Integer maxFunctionsAllowed,

        @Schema(description = "Maximum number of tasks allowed for Account")
        @NotNull Integer maxTasksAllowed,

        @Schema(description = "Maximum number of telemetries allowed for Account")
        @NotNull Integer maxTelemetriesAllowed,

        @Schema(description = "Maximum number of registry credentials allowed for Account")
        @NotNull Integer maxRegistryCredentialsAllowed,

        @Schema(description = "Current number of functions in Account")
        @Nullable Integer currentNumberFunctions,

        @Schema(description = "Last time the account was updated.")
        @Nullable Instant lastUpdatedAt) {
}
