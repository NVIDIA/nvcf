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
package com.nvidia.nvcf.rest.account;

import static com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.POLICY_RESULT_ATTRIBUTE;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
import com.nvidia.nvcf.rest.account.dto.AccountDetailsResponse;
import com.nvidia.nvcf.rest.account.dto.AccountResponse;
import com.nvidia.nvcf.rest.account.dto.AccountUpdateRequest;
import com.nvidia.nvcf.rest.account.dto.CreateAccountRequest;
import com.nvidia.nvcf.rest.account.dto.CreateAccountResponse;
import com.nvidia.nvcf.rest.account.dto.ListAccountResponse;
import com.nvidia.nvcf.rest.account.dto.PatchAccountRequest;
import com.nvidia.nvcf.service.account.AccountAuditService;
import com.nvidia.nvcf.service.account.AccountMapperService;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult;
import com.nvidia.nvcf.util.NvcfUtils;
import jakarta.servlet.http.HttpServletRequest;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.security.oauth2.core.DefaultOAuth2AuthenticatedPrincipal;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class AccountFacade {

    private final AccountAuditService accountAuditService;
    private final AccountService accountService;

    public CreateAccountResponse createCloudAccount(
            String ncaId,
            CreateAccountRequest createRequest,
            HttpServletRequest request,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(request, authentication);
        var accountDto = accountService.createCloudAccount(ncaId, createRequest, payloadBuilder);
        return CreateAccountResponse.builder().account(accountDto).build();
    }

    public AccountResponse updateCloudAccount(
            String ncaId,
            AccountUpdateRequest accountUpdateRequest,
            HttpServletRequest request,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(request, authentication);
        var accountDto = accountService.updateAccount(ncaId, accountUpdateRequest, payloadBuilder);
        return AccountResponse.builder().account(accountDto).build();
    }

    public ListAccountResponse getCloudAccounts(Authentication authentication) {
        // With ApiKey as auth token, the response only includes information about the account
        // specified in the token. With JWT, the response includes information about all
        // the accounts in the DB.
        var accountDtos = accountService.getAccounts()
                .stream()
                .filter(account -> accountMatch(authentication, account))
                .map(AccountMapperService::toAccountDto)
                .toList();
        return new ListAccountResponse(accountDtos);
    }

    public AccountResponse associateClient(
            String ncaId,
            PatchAccountRequest patchAccountRequest,
            HttpServletRequest request,
            Authentication authentication) {
        var adminClientId = patchAccountRequest.adminClientId();  // Will not be blank.
        var payloadBuilder = auditEventPayloadBuilder(request, authentication);
        var accountDto = accountService.associateClient(ncaId, adminClientId, payloadBuilder);
        return AccountResponse.builder().account(accountDto).build();
    }

    public AccountResponse disassociateClient(
            String ncaId,
            PatchAccountRequest patchAccountRequest,
            HttpServletRequest request,
            Authentication authentication) {
        var adminClientId = patchAccountRequest.adminClientId();  // Will not be blank.
        var payloadBuilder = auditEventPayloadBuilder(request, authentication);
        var accountDto = accountService.disassociateClient(ncaId, adminClientId, payloadBuilder);
        return new AccountResponse(accountDto);
    }

    public void deleteCloudAccount(
            String ncaId,
            HttpServletRequest request,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(request, authentication);
        accountService.deleteAccount(ncaId, payloadBuilder);
    }

    public AccountDetailsResponse getCloudAccountDetails(String ncaId) {
        return new AccountDetailsResponse(accountService.getAccountDetails(ncaId));
    }

    private AuditEventPayload.Builder auditEventPayloadBuilder(
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var customProperties = NvcfUtils.getCustomProperties(httpServletRequest);
        return accountAuditService.auditEventPayloadBuilder(authentication, customProperties);
    }

    private static boolean accountMatch(
            Authentication authentication,
            AccountEntity accountEntity) {
        if (authentication.getPrincipal() instanceof DefaultOAuth2AuthenticatedPrincipal principal
                && principal.getAttributes() != null
                && principal.getAttributes()
                .get(POLICY_RESULT_ATTRIBUTE) instanceof ApiKeyValidationResult policyResult) {
            return policyResult.ncaId().equals(accountEntity.getNcaId());
        }
        return true;  // No-op for JWT.
    }
}
