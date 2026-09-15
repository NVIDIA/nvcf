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

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.account.dto.AccountDetailsResponse;
import com.nvidia.nvcf.rest.account.dto.AccountResponse;
import com.nvidia.nvcf.rest.account.dto.AccountUpdateRequest;
import com.nvidia.nvcf.rest.account.dto.CreateAccountRequest;
import com.nvidia.nvcf.rest.account.dto.CreateAccountResponse;
import com.nvidia.nvcf.rest.account.dto.ListAccountResponse;
import com.nvidia.nvcf.rest.account.dto.PatchAccountRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Hidden;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PatchMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

/**
 * NGC CAS Role Mappings
 * Higher roles have all scopes from the lower roles.
 * For example Cloud Functions ACCOUNT ADMIN has all scopes from Cloud Functions USER.
 *
 * <p></p>
 * Cloud Functions SUPER ADMIN
 * ---
 * account_setup POST /v2/nvcf/accounts/{ncaId}
 *
 * <p></p>
 * Cloud Functions ACCOUNT ADMIN
 * ---
 * admin:deploy_function POST + DELETE + LIST
 * /v2/nvcf/accounts/{ncaId}/deployments/functions/{functionId}/versions/{versionId}
 * admin:register_function POST /v2/nvcf/accounts/{ncaId}/functions
 * admin:register_function POST /v2/nvcf/accounts/{ncaId}/functions/{functionId}/versions
 * admin:list_functions GET /v2/nvcf/accounts/{ncaId}/functions
 * admin:list_functions GET /v2/nvcf/accounts/{ncaId}/functions/{functionId}/versions
 * admin:list_functions GET /v2/nvcf/accounts/{ncaId}/functions/{functionId}/versions/{versionId}
 * admin:delete_function DELETE /v2/nvcf/accounts/{ncaId}/functions/{functionId}/versions/{versionId}
 * admin:authorize_clients POST /v2/nvcf/accounts/{ncaId}/authorizations
 *
 * <p></p>
 * Cloud Functions USER
 * ---
 * admin:list_functions GET /v2/nvcf/accounts/{ncaId}/functions
 * admin:list_functions GET /v2/nvcf/accounts/{ncaId}/functions/{functionId}/versions
 * admin:list_functions GET /v2/nvcf/accounts/{ncaId}/functions/{functionId}/versions/{versionId}
 */
@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(value = "/v2/nvcf/accounts", produces = APPLICATION_JSON_VALUE)
@Tag(name = "Account Management For NVIDIA Super Admins",
        description = """
                Defines Account Management endpoints. These endpoints can only be invoked by
                 NVIDIA Super Admins and require a bearer token in HTTP Authorization header with
                 'account_setup' scope.
                """
)
public class AccountController {

    private static final String NCA_ID_DESCRIPTION = "Id of the NVIDIA Cloud Account";
    private static final String AUTH_DESCRIPTION = """
            Requires a bearer token in the HTTP Authorization header with 'account_setup' scope.
             These endpoints are invoked by NVIDIA Super Admins working across accounts.
            """;

    private final AccountFacade accountFacade;
    private final AccountService accountService;
    private final Tracer tracer;

    @PostMapping(value = "{ncaId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Account Provisioning",
            description = """
                    Sets up NVIDIA Cloud Account within Cloud Functions with registry credentials.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('account_setup')")
    public CreateAccountResponse createCloudAccount(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Valid @RequestBody CreateAccountRequest createAccountRequest,
            HttpServletRequest request,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return accountFacade.createCloudAccount(ncaId, createAccountRequest,
                                                request, authentication);
    }

    @GetMapping
    @Operation(
            summary = "List all the NVIDIA Cloud Accounts onboarded with Cloud Functions",
            description = AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('account_setup', 'apikey:account_setup')")
    public ListAccountResponse getCloudAccounts(Authentication authentication) {
        return accountFacade.getCloudAccounts(authentication);
    }

    @PatchMapping(value = "{ncaId}", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Account Update",
            description = """
                    Updates the specified NVIDIA Cloud Account within Cloud Functions.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('account_setup', 'apikey:account_setup')")
    public AccountResponse updateCloudAccount(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Valid @RequestBody AccountUpdateRequest accountUpdateRequest,
            HttpServletRequest request,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return accountFacade
                .updateCloudAccount(ncaId, accountUpdateRequest, request, authentication);
    }

    @PatchMapping(value = "{ncaId}/clients/associate", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Associate Client",
            description = """
                    Associates Client Id specified in the payload with the NVIDIA Cloud Account
                     specified in the path. An entry is also added to the clients table for reverse
                     mapping. Multiple Client Ids can be associated with a NVIDIA Cloud Account.
                     But, a Client Id cannot be associated with multiple NVIDIA Cloud Accounts.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('account_setup', 'apikey:account_setup')")
    public AccountResponse associateClientWithCloudAccount(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Valid @RequestBody PatchAccountRequest patchAccountRequest,
            HttpServletRequest request,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return accountFacade.associateClient(ncaId, patchAccountRequest, request, authentication);
    }

    @PatchMapping(value = "{ncaId}/clients/disassociate", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Disassociate Client",
            description = """
                    Disassociates Client Id specified in the payload with the NVIDIA Cloud Account
                     specified in the path. The specified Client Id is removed from the set of
                     clients that are associated with the NVIDIA Cloud Account. The corresponding
                     entry from the clients table is also removed.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('account_setup', 'apikey:account_setup')")
    public AccountResponse disassociateClientFromCloudAccount(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Valid @RequestBody PatchAccountRequest patchAccountRequest,
            HttpServletRequest request,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return accountFacade.disassociateClient(ncaId, patchAccountRequest,
                                                request, authentication);
    }

    @DeleteMapping(value = "{ncaId}")
    @Operation(
            summary = "Delete NVIDIA Cloud Account",
            description = """
                    Deletes the NVIDIA Cloud Account specified in the path and all the associated
                     Clients.
                    """ + AUTH_DESCRIPTION,
            responses = @ApiResponse(responseCode = "204")
    )
    @PreAuthorize("hasAuthority('account_setup')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteCloudAccount(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            HttpServletRequest request,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        accountFacade.deleteCloudAccount(ncaId, request, authentication);
    }

    @Hidden  // No need to expose this endpoint in OpenAPI specs.
    @GetMapping(value = "{ncaId}")
    @Operation(
            summary = "Get NVIDIA Cloud Account details",
            description = "Gets details of the specified NVIDIA Cloud Account." + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('account_setup', 'apikey:account_setup')")
    public AccountDetailsResponse getCloudAccountDetails(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return accountFacade.getCloudAccountDetails(ncaId);
    }

}
