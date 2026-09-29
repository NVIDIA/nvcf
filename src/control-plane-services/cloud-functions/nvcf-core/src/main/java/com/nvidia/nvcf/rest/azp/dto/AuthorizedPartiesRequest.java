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
package com.nvidia.nvcf.rest.azp.dto;

import static com.google.common.collect.ImmutableBiMap.toImmutableBiMap;
import static java.lang.annotation.ElementType.FIELD;
import static java.lang.annotation.RetentionPolicy.RUNTIME;
import static org.apache.commons.lang3.StringUtils.isNotBlank;
import static org.apache.commons.lang3.StringUtils.isWhitespace;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.Constraint;
import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;
import jakarta.validation.Payload;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import java.lang.annotation.Documented;
import java.lang.annotation.Retention;
import java.lang.annotation.Target;
import java.util.List;
import java.util.stream.Collectors;
import lombok.Builder;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@Builder
@Schema(description = "Request to associated authorized parties for a specific version or all" +
                        "versions of a function")
public record AuthorizedPartiesRequest(
        @Schema(description = "Parties authorized to invoke function")
        @NotEmpty @NotNull @ValidClientAccountMapping @ValidAuthorizedAccounts @ValidAuthorizedWildcardAccount
        List<@Valid AuthorizedPartyDto> authorizedParties) {

    private static final String MESG_INVALID_MAPPING =
            "Invalid request: Issues with the request payload";
    private static final String MESG_DUPLICATE_AUTH_ACCOUNTS =
            "Invalid request: Duplicate authorized accounts specified";
    private static final String MESG_CLIENT_ID_ONLY_WHITESPACES =
            "Invalid request: clientId has only whitespaces";
    private static final String MESG_INVALID_WILDCARD_ACCOUNT_REQUEST =
            "Invalid request: Multiple authz parties or clientId specified with wildcard account";
    private static final String MESG_MULTIPLE_AUTH_ACCOUNTS =
            "Invalid request: Multiple authorized accounts specified with wildcard account";
    private static final String MESG_CLIENT_WITH_WILDCARD_ACCOUNT =
            "Invalid request: clientId should not specified with wildcard account";

    @Documented
    @Target(FIELD)
    @Retention(RUNTIME)
    @Constraint(validatedBy = ClientAccountMappingValidator.class)
    @interface ValidClientAccountMapping {
        String message() default MESG_INVALID_MAPPING;

        Class<?>[] groups() default {};

        Class<? extends Payload>[] payload() default {};
    }

    // Validate one-to-one mapping between Client Ids and NCA IDs in the list of
    // authorized parties. No two Client IDs should be mapped to the same NCA ID.
    // Similarly, a Client ID should not be mapped to two or more NCA IDs.
    private static class ClientAccountMappingValidator
            implements ConstraintValidator<ValidClientAccountMapping, List<AuthorizedPartyDto>> {

        @Override
        public boolean isValid(
                List<AuthorizedPartyDto> authorizedParties,
                ConstraintValidatorContext constraintValidatorContext) {
            var clientIdOnlyWhitespace  = authorizedParties.stream()
                                                .anyMatch(azp -> isWhitespace(azp.clientId()));
            if (clientIdOnlyWhitespace) {
                log.error(MESG_CLIENT_ID_ONLY_WHITESPACES);
                return false;
            }

            try {
                authorizedParties.stream()
                        .filter(azp -> isNotBlank(azp.clientId()))
                        .collect(toImmutableBiMap(AuthorizedPartyDto::clientId,
                                                  AuthorizedPartyDto::ncaId));
            } catch (Exception ex) {
                log.error(MESG_INVALID_MAPPING);
                return false;
            }

            return true;
        }
    }

    @Documented
    @Target(FIELD)
    @Retention(RUNTIME)
    @Constraint(validatedBy = AuthorizedAccountsValidator.class)
    @interface ValidAuthorizedAccounts {
        String message() default MESG_DUPLICATE_AUTH_ACCOUNTS;

        Class<?>[] groups() default {};

        Class<? extends Payload>[] payload() default {};
    }

    // Authorized accounts(ncaIds) in the request payload must be unique.
    private static class AuthorizedAccountsValidator
            implements ConstraintValidator<ValidAuthorizedAccounts, List<AuthorizedPartyDto>> {

        @Override
        public boolean isValid(
                List<AuthorizedPartyDto> authorizedParties,
                ConstraintValidatorContext constraintValidatorContext) {
            var numAzAccounts = authorizedParties.stream()
                    .map(AuthorizedPartyDto::ncaId)
                    .collect(Collectors.toSet())
                    .size();

            if (numAzAccounts != authorizedParties.size()) {
                log.error(MESG_DUPLICATE_AUTH_ACCOUNTS);
                return false;
            }

            return true;
        }
    }

    @Documented
    @Target(FIELD)
    @Retention(RUNTIME)
    @Constraint(validatedBy = AuthorizedWildcardAccountValidator.class)
    @interface ValidAuthorizedWildcardAccount {
        String message() default MESG_INVALID_WILDCARD_ACCOUNT_REQUEST;

        Class<?>[] groups() default {};

        Class<? extends Payload>[] payload() default {};
    }

    // If wildcard auth account is specified in the request, then other authorized parties
    // should not be specified. Also, there should not any clientId associated with a
    // wildcard account.
    private static class AuthorizedWildcardAccountValidator
            implements ConstraintValidator<ValidAuthorizedWildcardAccount, List<AuthorizedPartyDto>> {

        @Override
        public boolean isValid(
                List<AuthorizedPartyDto> authorizedParties,
                ConstraintValidatorContext constraintValidatorContext) {
            var wildcardAuthAccountSpecified = authorizedParties.stream()
                                                .filter(dto -> isNotBlank(dto.ncaId()))
                                                .anyMatch(dto -> dto.ncaId().equals("*"));
            if (wildcardAuthAccountSpecified) {
                if (authorizedParties.size() > 1) {
                    log.error(MESG_MULTIPLE_AUTH_ACCOUNTS);
                    return false;
                }
                if (isNotBlank(authorizedParties.getFirst().clientId())) {
                    log.error(MESG_CLIENT_WITH_WILDCARD_ACCOUNT);
                    return false;
                }
            }
            return true;
        }
    }
}
