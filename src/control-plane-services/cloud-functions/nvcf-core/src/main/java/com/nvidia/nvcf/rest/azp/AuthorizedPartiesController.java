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
package com.nvidia.nvcf.rest.azp;

import static com.nvidia.nvcf.rest.azp.AuthorizedPartiesFacade.WILDCARD;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesRequest;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesResponse;
import com.nvidia.nvcf.rest.azp.dto.ListAuthorizedPartiesResponse;
import com.nvidia.nvcf.rest.azp.dto.PatchAuthorizedPartyRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PatchMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(value = "/v2/nvcf/authorizations", produces = APPLICATION_JSON_VALUE)
@Tag(name = "Function Sharing",
        description = """
                Defines endpoints related to authorizing other accounts to invoke a function for
                 Account Admins. All the endpoints defined in this API require a bearer token with
                 'authorize_clients' scope in HTTP Authorization header."""
)
public class AuthorizedPartiesController {

    private static final String MESG_INVALID_AZPS_OPERATION =
            "Function id '%s': Cannot %s authorized parties for public function";
    private static final String MESG_INVALID_AZPS_OPER_VERSION =
            "Function id '%s', version '%s': Cannot %s authorized parties for public function";
    private static final String MESG_INVALID_PF_OPERATION =
            "Function id '%s': Only NVIDIA Super Admin can make a function public";
    private static final String MESG_INVALID_PF_OPERATION_VERSION =
            "Function id '%s', version '%s': Only NVIDIA Super Admin can make a function public";

    private static final String AUTH_DESCRIPTION = """
            Access to this functionality mandates the inclusion of a bearer token with the
             'authorize_clients' scope in the HTTP Authorization header
            """;

    private final AccountService accountService;
    private final AuthorizedPartiesFacade azpsFacade;
    private final AuthorizedPartiesService azpsService;
    private final Tracer tracer;

    @PostMapping(value = "functions/{functionId}", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Authorize Accounts To Invoke Function",
            description = """
                   Authorizes additional NVIDIA Cloud Accounts to invoke any version of the
                    specified function. By default, a function belongs to the NVIDIA Cloud Account
                    that created it, and the credentials used for function invocation must
                    reference the same NVIDIA Cloud Account. Upon invocation of this endpoint, any
                    existing authorized accounts will be overwritten by the newly specified
                    authorized accounts.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse createAuthorizedPartiesForAllVersions(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Valid @RequestBody AuthorizedPartiesRequest request,
            Authentication authentication) {
        // Account Admin cannot create authorized parties for a public function.
        azpsService.verifyNotAPublicFunction(functionId,
                                          () -> MESG_INVALID_AZPS_OPERATION
                                                  .formatted(functionId, "create"));

        // Is Account Admin trying to make all versions of a function public?
        var wildcardAuthAccountSpecified = request.authorizedParties()
                                            .stream()
                                            .anyMatch(azp -> azp.ncaId().equals(WILDCARD));
        if (wildcardAuthAccountSpecified) {
            // Only NVIDIA Super Admin can make a function public.
            var mesg = MESG_INVALID_PF_OPERATION.formatted(functionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.createAuthorizedParties(ncaId, functionId, Optional.empty(),
                                                  authentication, request);
    }

    @GetMapping("functions/{functionId}")
    @Operation(
            summary = "List Account Authorizations For Function",
            description = """
                    Lists NVIDIA Cloud Account IDs that are authorized to invoke any version of the
                     specified function. The response includes an array showing authorized accounts
                     for each version. Individual versions of a function can have their own
                     authorized accounts. So, each object in the array can have different
                     authorized accounts listed.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public ListAuthorizedPartiesResponse getAuthorizedPartiesForAllVersions(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.getAuthorizedPartiesForAllVersions(ncaId, functionId, authentication);
    }

    @PatchMapping(value = "functions/{functionId}/add", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Authorize Another Account To Invoke, List, and Check Queue Details of Function",
            description = """
                    Adds the specified NVIDIA Cloud Account to the set of authorized accounts that
                     are can invoke all the versions of the specified function. If the specified
                     function does not have any existing inheritable authorized accounts, it results
                     in a response with status 404. If the specified account is already in the set
                     of existing inheritable authorized accounts, it results in a response with
                     status code 409. If a function is public, then Account Admin cannot perform
                     this operation. Note that response only includes authz accounts at the
                     function level.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse addAuthorizedPartyForAllVersions(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Valid @RequestBody PatchAuthorizedPartyRequest request,
            Authentication authentication) {
        // Account Admin cannot add authorized parties to a public function.
        azpsService.verifyNotAPublicFunction(functionId,
                                             () -> MESG_INVALID_AZPS_OPERATION
                                                    .formatted(functionId, "add"));

        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.addAuthorizedParty(ncaId, functionId, Optional.empty(),
                                             authentication, request);
    }

    @PatchMapping(value = "functions/{functionId}/remove", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Unauthorize Account From Invoking, Listing, and Checking Queue Details of Function",
            description = """
                    Removes the specified NVIDIA Cloud Account from the set of authorized accounts
                     that can invoke all the versions of the specified function. If the specified
                     function does not have any existing inheritable authorized parties, it results
                     in a response with status 404. Also, if the specified account is not in the
                     existing set of inheritable authorized accounts, it results in a response with
                     status 404. If the specified function is public, then Account Admin cannot
                     perform this operation. Note that response only includes the remaining
                     authorized accounts at the function level.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse removeAuthorizedPartyForAllVersions(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Valid @RequestBody PatchAuthorizedPartyRequest request,
            Authentication authentication) {
        // Account Admin cannot remove authorized parties from a public function.
        azpsService.verifyNotAPublicFunction(functionId,
                                             () -> MESG_INVALID_AZPS_OPERATION
                                                    .formatted(functionId, "remove"));

        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.removeAuthorizedParty(ncaId, functionId, Optional.empty(),
                                                authentication, request);
    }

    @DeleteMapping(value = "functions/{functionId}")
    @Operation(
            summary = "Delete All Authorizations At Function Level",
            description = """
                    Deletes authorizations at the function level. This impacts all versions of
                     the function. If a function versions has its own set of authorizations,
                     those are not deleted. If the specified function is public, then
                     Account Admin cannot perform this operation. Note that the response does
                     not include any authz accounts at version level.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse deleteAuthorizedPartiesForAllVersions(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            Authentication authentication) {
        // Account Admin cannot delete authorized parties from a public function.
        azpsService.verifyNotAPublicFunction(functionId,
                                             () -> MESG_INVALID_AZPS_OPERATION
                                                    .formatted(functionId, "delete"));

        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.deleteAuthorizedParties(ncaId, functionId,
                                                  Optional.empty(), authentication);
    }

    // Version-specific endpoints.

    @PostMapping(value = "functions/{functionId}/versions/{functionVersionId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Authorize Accounts To Invoke/List Function Version",
            description = """
                    Authorizes additional NVIDIA Cloud Accounts to invoke a specific function
                     version. By default, a function belongs to the NVIDIA Cloud Account that
                     created it, and the credentials used for function invocation must reference
                     the same NVIDIA Cloud Account. Upon invocation of this endpoint, any existing
                     authorized accounts will be overwritten by the newly specified authorized
                     accounts. Note that the response does NOT include inherited authorized accounts
                     that were added at the function level.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse createAuthorizedPartiesForSpecificVersion(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version id", required = true)
            @PathVariable UUID functionVersionId,
            @Valid @RequestBody AuthorizedPartiesRequest request,
            Authentication authentication) {
        // Account Admin cannot create authorized parties for a public function.
        azpsService.verifyNotAPublicFunction(functionId, functionVersionId,
                                             () -> MESG_INVALID_AZPS_OPER_VERSION.formatted(
                                                     functionId, functionVersionId, "create"));

        // Is Account Admin trying to make specific function version public?
        var wildcardAuthAccountSpecified = request.authorizedParties()
                                                .stream()
                                                .anyMatch(azp -> azp.ncaId().equals(WILDCARD));
        if (wildcardAuthAccountSpecified) {
            // Only NVIDIA Super Admin can make a function public.
            var mesg = MESG_INVALID_PF_OPERATION_VERSION.formatted(functionId, functionVersionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.createAuthorizedParties(ncaId, functionId, Optional.of(functionVersionId),
                                                  authentication, request);
    }

    @GetMapping("functions/{functionId}/versions/{functionVersionId}")
    @Operation(
            summary = "Get Account Authorizations For Function Version",
            description = """
                    Gets NVIDIA Cloud Account IDs that are authorized to invoke specified function
                     version. Response includes authorized accounts that were added specifically
                     at the version level and the authorized accounts that were inherited from
                     function level.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse getAuthorizedPartiesForSpecificVersion(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version", required = true)
            @PathVariable UUID functionVersionId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.getAuthorizedPartiesForSpecificVersion(ncaId, functionId,
                                                                 functionVersionId, authentication);
    }

    @PatchMapping(value = "functions/{functionId}/versions/{functionVersionId}/add",
                  consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Authorize Another Account To Invoke, List, and Check Queue Details of Function Version",
            description = """
                    Adds the specified NVIDIA Cloud Account to the set of authorized accounts that
                     can invoke the specified function version. If the specified function version
                     does not have any existing inheritable authorized accounts, it results in a
                     response with status 404. If the specified account is already in the set of
                     existing authorized accounts that are directly associated with the function
                     version, it results in a response wit status code 409. If a function is public,
                     then Account Admin cannot perform this operation. Note that the response
                     does not include inherited authorized accounts that were added at the function
                     level.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse addAuthorizedPartyForSpecificVersion(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version", required = true)
            @PathVariable UUID functionVersionId,
            @Valid @RequestBody PatchAuthorizedPartyRequest request,
            Authentication authentication) {
        // Account Admin cannot add authorized parties to a public function.
        azpsService.verifyNotAPublicFunction(functionId, functionVersionId,
                                             () -> MESG_INVALID_AZPS_OPER_VERSION
                                                  .formatted(functionId, functionVersionId, "add"));

        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.addAuthorizedParty(ncaId, functionId, Optional.of(functionVersionId),
                                             authentication, request);
    }

    @PatchMapping(value = "functions/{functionId}/versions/{functionVersionId}/remove",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Unauthorize Account From Invoking, Listing, and Checking Queue Details of Function Version",
            description = """
                    Removes the specified NVIDIA Cloud Account from the set of authorized accounts
                     that are directly associated with specified function version. If the specified
                     function version does not have any of its own(not inherited) authorized
                     accounts, it results in a response with status 404. Also, if the specified
                     authorized account is not in the set of existing authorized parties that are
                     directly associated with the specified function version, it results in a
                     response with status code 404. If the specified function version is public,
                     then Account Admin cannot perform this operation. Note that the response
                     does not include inherited authorized accounts that were added at the function
                     level.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse removeAuthorizedPartyForSpecificVersion(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version", required = true)
            @PathVariable UUID functionVersionId,
            @Valid @RequestBody PatchAuthorizedPartyRequest request,
            Authentication authentication) {
        // Account Admin cannot remove authorized parties from a public function.
        azpsService.verifyNotAPublicFunction(functionId, functionVersionId,
                                          () -> MESG_INVALID_AZPS_OPER_VERSION.formatted(
                                                  functionId, functionVersionId, "remove"));

        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.removeAuthorizedParty(ncaId, functionId, Optional.of(functionVersionId),
                                                authentication, request);
    }

    @DeleteMapping(value = "functions/{functionId}/versions/{functionVersionId}")
    @Operation(
            summary = "Delete All Authorizations At Version Level",
            description = """
                    Deletes all the authorized accounts that are directly associated with the
                     specified function version. Authorized parties that are inherited by the
                     function version are not deleted. If the specified function version is public,
                     then Account Admin cannot perform this operation. Note that the response
                     does not include inherited authorized accounts that were added at the function
                     level.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('authorize_clients', 'apikey:authorize_clients')")
    public AuthorizedPartiesResponse deleteAuthorizedPartiesForSpecificVersion(
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version", required = true)
            @PathVariable UUID functionVersionId,
            Authentication authentication) {
        // Account Admin cannot delete authorized parties from a public function.
        azpsService.verifyNotAPublicFunction(functionId, functionVersionId,
                                             () -> MESG_INVALID_AZPS_OPER_VERSION.formatted(
                                                     functionId, functionVersionId, "delete"));

        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return azpsFacade.deleteAuthorizedParties(ncaId, functionId,
                                                  Optional.of(functionVersionId), authentication);
    }

}
