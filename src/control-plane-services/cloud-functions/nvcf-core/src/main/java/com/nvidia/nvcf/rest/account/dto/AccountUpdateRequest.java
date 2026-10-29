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

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.util.NvcfConstants;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.PositiveOrZero;
import jakarta.validation.constraints.Size;
import java.util.Objects;
import lombok.Builder;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import tools.jackson.databind.annotation.JsonDeserialize;
import tools.jackson.databind.util.StdConverter;

@Builder
@Schema(description = "DTO used to update Account")
@JsonDeserialize(converter = AccountUpdateRequest.AccountUpdateRequestValidator.class)
public record AccountUpdateRequest(
        @Schema(description = "Human readable account/customer name")
        @Pattern(regexp = ACCOUNT_NAME_REGEX,
                message = "Invalid account name: Must conform to regex " + ACCOUNT_NAME_REGEX)
        @Size(min = 4, max = 36, message = "Invalid account name: Must be 4 - 36 characters long")
        @Nullable String name,

        @Schema(description = "Number of max allowed functions. " +
                "Zero marks account not allowed to create functions.")
        @Valid
        @PositiveOrZero
        @Nullable Integer maxFunctionsAllowed,

        @Schema(description = "Number of max allowed tasks. " +
                "Zero marks account not allowed to create tasks.")
        @Valid
        @PositiveOrZero
        @Nullable Integer maxTasksAllowed,

        @Schema(description = "Number of max allowed telemetries. " +
                "Zero marks account not allowed to create telemetries.")
        @Valid
        @PositiveOrZero
        @Max(value = NvcfConstants.UPDATE_MAX_TELEMETRIES_ALLOWED,
                message = "Max telemetries allowed cannot exceed "
                        + NvcfConstants.UPDATE_MAX_TELEMETRIES_ALLOWED)
        @Nullable Integer maxTelemetriesAllowed,

        @Schema(description = "Number of max allowed registry credentials. " +
                "Zero marks account not allowed to create registry credentials.")
        @Valid
        @PositiveOrZero
        @Max(value = NvcfConstants.UPDATE_MAX_REGISTRY_CREDENTIALS_ALLOWED,
                message = "Max registry credentials allowed cannot exceed "
                        + NvcfConstants.UPDATE_MAX_REGISTRY_CREDENTIALS_ALLOWED)
        @Nullable Integer maxRegistryCredentialsAllowed) {

    @Slf4j
    static class AccountUpdateRequestValidator
            extends StdConverter<AccountUpdateRequest, AccountUpdateRequest> {

        private static final String MESG_REQUEST_VALIDATION_ERROR =
                "Invalid request: Either name, credential fields or limits should be specified";

        @Override
        public AccountUpdateRequest convert(AccountUpdateRequest request) {
            if (StringUtils.isBlank(request.name)
                    && Objects.isNull(request.maxFunctionsAllowed())
                    && Objects.isNull(request.maxTasksAllowed())
                    && Objects.isNull(request.maxTelemetriesAllowed())
                    && Objects.isNull(request.maxRegistryCredentialsAllowed())) {
                log.error(MESG_REQUEST_VALIDATION_ERROR);
                throw new BadRequestException(MESG_REQUEST_VALIDATION_ERROR);
            }

            return request;
        }
    }
}
