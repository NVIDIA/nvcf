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
package com.nvidia.nvct.service.account;

import static com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.POLICY_RESULT_ATTRIBUTE;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnprocessableEntityException;
import com.nvidia.nvct.service.account.dto.AccountDto;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult;
import com.nvidia.nvct.service.nvcf.NvcfClient;
import com.nvidia.nvct.service.telemetry.dto.TelemetryDto;
import java.util.Collections;
import java.util.Map;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.core.Authentication;
import org.springframework.security.oauth2.core.DefaultOAuth2AuthenticatedPrincipal;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import org.springframework.web.reactive.function.client.WebClientResponseException;

@Slf4j
@Service
@RequiredArgsConstructor
public class AccountService {

    private static final String MESG_UNSUPPORTED_AUTHENTICATION_TYPE =
            "Unsupported Authentication class type '%s' or PolicyResult object was not found.";
    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";
    private static final String MESG_ACCOUNT_NOT_FOUND =
            "Account '%s': Does not exist";
    private static final String MESG_ACCOUNT_ID_MISMATCH =
            "Account id mismatch. Expected: '%s', Got: '%s'";

    private final NvcfClient nvcfClient;

    public String getNcaId(Authentication authentication) {
        // JWT
        if (authentication instanceof JwtAuthenticationToken) {
            var clientId = authentication.getName();
            var dto = nvcfClient.getClient(clientId);
            return dto.ncaId();
        }

        // Api-Key
        if (authentication.getPrincipal() instanceof DefaultOAuth2AuthenticatedPrincipal principal
                && principal.getAttributes() != null
                && principal.getAttributes()
                .get(POLICY_RESULT_ATTRIBUTE) instanceof ApiKeyValidationResult policyResult) {
            return policyResult.ncaId();
        }

        throw new UnprocessableEntityException(MESG_UNSUPPORTED_AUTHENTICATION_TYPE
                                                 .formatted(authentication.getClass()));
    }

    public AccountDto getAccount(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return nvcfClient.getAccount(ncaId);
    }

    public String getAccountName(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var account = getAccount(ncaId);
        return account.name();
    }

    public AccountDto lookupAccountUsingNcaIdOrThrow(String ncaId) {
        try {
            return getAccount(ncaId);
        } catch (WebClientResponseException.NotFound ex) {
            var mesg = MESG_ACCOUNT_NOT_FOUND.formatted(ncaId) + " - " + ex.getMessage();
            log.error(mesg);
            throw new NotFoundException(mesg, ex);
        }
    }

    public Map<UUID, TelemetryDto> getAccountTelemetryMap(String ncaId) {
        var accountDto = getAccount(ncaId);

        if (CollectionUtils.isEmpty(accountDto.telemetries())) {
            return Collections.emptyMap();
        }

        return accountDto.telemetries().stream()
                .collect(Collectors.toMap(TelemetryDto::telemetryId, Function.identity()));
    }

    public void invalidateCacheForSpecificAccount(String ncaId) {
        nvcfClient.invalidateCacheForSpecificAccount(ncaId);
    }

    public void assertAccountExistsOrThrow(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        // Verify account exists by trying to fetch it from NVCF
        try {
            nvcfClient.getAccount(ncaId);
        } catch (WebClientResponseException.NotFound ex) {
            var mesg = MESG_ACCOUNT_NOT_FOUND.formatted(ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg, ex);
        }
    }

    // Used only for super admin endpoints to ensure that the NCA Id specified in the path
    // matches the corresponding property in the auth token. We do not allow api-keys with admin:
    // scopes as api-keys are locked to the account and cannot be used across accounts. This is
    // different from JWTs with admin: scopes which can be used across different accounts. The
    // reason we chose to lock down api-keys to a specific account is because they are long-lived
    // when compared to JWTs which are ephemeral and short-lived.
    public void assertAccountIdFromPathMatches(
            String ncaId,  // Value of the path variable in super admin endpoints
            Authentication authentication) {
        // Api-key
        if (authentication.getPrincipal() instanceof DefaultOAuth2AuthenticatedPrincipal principal
                && principal.getAttributes() != null
                && principal.getAttributes()
                .get(POLICY_RESULT_ATTRIBUTE) instanceof ApiKeyValidationResult policyResult) {
            // Check if the nca_id in the api-key matches the value from the path variable.
            if (!policyResult.ncaId().equals(ncaId)) {
                var mesg = MESG_ACCOUNT_ID_MISMATCH.formatted(policyResult.ncaId(), ncaId);
                log.error(mesg);
                throw new ForbiddenException(mesg);
            }
            return;
        }

        // JWT: No-op
        if (authentication instanceof JwtAuthenticationToken) {
            // No need to further check for any match for JWTs for super admin endpoints.
            return;
        }

        throw new UnprocessableEntityException(MESG_UNSUPPORTED_AUTHENTICATION_TYPE
                                                 .formatted(authentication.getClass()));
    }

    @VisibleForTesting
    public void invalidateCache() {
        nvcfClient.invalidateCache();
    }
}
