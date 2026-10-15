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

import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.privateFunctionMatch;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.client.entity.ClientEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesRequest;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesResponse;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.azp.dto.ListAuthorizedPartiesResponse;
import com.nvidia.nvcf.rest.azp.dto.PatchAuthorizedPartyRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.client.ClientService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Predicate;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class AuthorizedPartiesFacade {
    public static final String WILDCARD = "*";

    private static final String MESG_FUNCTION_NOT_FOUND =
            "Function id '%s': Not found in account '%s'";
    private static final String MESG_FUNCTION_VERSION_NOT_FOUND =
            "Function id '%s', version '%s': Not found in account '%s'";
    private static final String MESG_INVALID_AUTHORIZED_PARTY =
            "Invalid request: Authorized party mapped to account '%s' is invalid";
    private static final String MESG_AUTH_PARTIES_ALL_VERSIONS =
            "Function id '%s': %s authorized parties for all versions";
    private static final String MESG_AUTH_PARTIES_SPECIFIC_VERSION =
            "Function id '%s', version '%s': %s authorized parties";
    private static final String MESG_AUTH_PARTY_ALL_VERSIONS =
            "Function id '%s': %s authorized party for all versions";
    private static final String MESG_AUTH_PARTY_SPECIFIC_VERSION =
            "Function id '%s', version '%s': %s authorized party";
    private static final String MESG_INVALID_AUTH_PARTY =
            "Invalid request: Account '%s' and client '%s' are not setup with each other";
    private static final String MESG_FORBIDDEN_TO_OPERATE =
            "Function id '%s', version '%s': Forbidden to %s authorized party";
    private static final String MESG_CLIENT_ID_SPECIFIED_WITH_WILDCARD_AZP =
            "Invalid request: clientId should not be specified with wildcard(*) authz party";
    private static final String MESG_EXTRA_AZPS_WITH_WILDCARD_AZP =
            "Invalid request: Other authz parties not allowed with wildcard(*) authorized party";
    private static final String MESG_INVALID_AUTHZ_ACCOUNT_FUNCTION =
            "Function id '%s': Invalid authz account '%s' specified for %s operation";
    private static final String MESG_INVALID_AUTHZ_ACCOUNT_VERSION =
            "Function id '%s', version '%s': Invalid authz account '%s' specified for %s operation";

    private final AccountService accountService;
    private final AuthorizedPartiesService authorizedPartiesService;
    private final FunctionLookupService functionLookupService;
    private final ClientService clientService;

    public AuthorizedPartiesResponse createAuthorizedParties(
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId,
            Authentication authentication,
            AuthorizedPartiesRequest authPartiesRequest) {
        var function = getCandidateFunction(ncaId, functionId, optVersionId,
                                            fun -> privateFunctionMatch(ncaId, authentication, fun),
                                            "create");
        validateCreateAuthorizedPartyRequest(ncaId, authPartiesRequest);
        validateNcaId(function, ncaId, functionId, optVersionId);

        var authorizedParties = authPartiesRequest.authorizedParties();
        var dto = authorizedPartiesService.createAuthorizedParties(functionId, optVersionId,
                                                                   ncaId,
                                                                   authorizedParties);
        var mesg = optVersionId
                .map(versionId -> MESG_AUTH_PARTY_SPECIFIC_VERSION.formatted(functionId, versionId,
                                                                             "Created"))
                .orElseGet(() -> MESG_AUTH_PARTY_ALL_VERSIONS.formatted(functionId, "Created"));
        log.info(mesg);
        return new AuthorizedPartiesResponse(dto);
    }

    // Returns all the versions of the function with associated authorized parties. Authorized
    // parties registered at the function level are inherited by all the versions of the
    // function. If there are no authorized parties for any function version, then the response
    // includes an empty list.
    public ListAuthorizedPartiesResponse getAuthorizedPartiesForAllVersions(
            String ncaId,
            UUID functionId,
            Authentication authentication) {
        var functions = functionLookupService
                .lookupUsingAccountIdAndFunctionIdOrThrow(ncaId, functionId);
        var azpDtos = functions.stream()
                .filter(function -> privateFunctionMatch(ncaId, authentication, function))
                .map(function -> {
                    validateNcaId(function, ncaId, functionId, Optional.empty());
                    return authorizedPartiesService.getAuthorizedParties(function);
                })
                .toList();
        log.info(MESG_AUTH_PARTIES_ALL_VERSIONS.formatted(functionId, "Retrieved"));
        return new ListAuthorizedPartiesResponse(azpDtos);
    }

    // Returns authorized parties associated with a specific version of function. It will
    // include the authorized parties both at the function level and the version level.
    // If there are no authorized parties for the function version, then the response DTO
    // includes an empty list.
    public AuthorizedPartiesResponse getAuthorizedPartiesForSpecificVersion(
            String ncaId,
            UUID functionId,
            UUID versionId,
            Authentication authentication) {
        var function = getCandidateFunction(ncaId, functionId, Optional.of(versionId),
                                            fun -> privateFunctionMatch(ncaId, authentication, fun),
                                            "get");
        validateNcaId(function, ncaId, functionId, Optional.of(versionId));

        var dto = authorizedPartiesService.getAuthorizedParties(function);
        log.info(MESG_AUTH_PARTIES_SPECIFIC_VERSION.formatted(functionId, versionId, "Retrieved"));
        return new AuthorizedPartiesResponse(dto);
    }

    public AuthorizedPartiesResponse addAuthorizedParty(
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId,
            Authentication authentication,
            PatchAuthorizedPartyRequest request) {
        var function = getCandidateFunction(ncaId, functionId, optVersionId,
                                            fun -> privateFunctionMatch(ncaId, authentication, fun),
                                            "add");
        validatePatchAuthorizedPartyRequest(ncaId, functionId, optVersionId, request, "add");
        validateNcaId(function, ncaId, functionId, optVersionId);
        var dto = authorizedPartiesService.addAuthorizedParty(functionId, optVersionId,
                                                              request.authorizedParty());
        var mesg = optVersionId
                    .map(versionId -> MESG_AUTH_PARTY_SPECIFIC_VERSION
                                        .formatted(functionId, versionId, "Added"))
                    .orElseGet(() -> MESG_AUTH_PARTY_ALL_VERSIONS.formatted(functionId, "Added"));
        log.info(mesg);
        return new AuthorizedPartiesResponse(dto);
    }

    public AuthorizedPartiesResponse removeAuthorizedParty(
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId,
            Authentication authentication,
            PatchAuthorizedPartyRequest request) {
        var function = getCandidateFunction(ncaId, functionId, optVersionId,
                                            fun -> privateFunctionMatch(ncaId, authentication, fun),
                                            "remove");
        validatePatchAuthorizedPartyRequest(ncaId, functionId, optVersionId, request, "remove");
        validateNcaId(function, ncaId, functionId, optVersionId);
        var dto = authorizedPartiesService.removeAuthorizedParty(functionId, optVersionId,
                                                                 request.authorizedParty());
        var mesg = optVersionId
                    .map(versionId -> MESG_AUTH_PARTY_SPECIFIC_VERSION
                                        .formatted(functionId, versionId, "Removed"))
                    .orElseGet(() -> MESG_AUTH_PARTY_ALL_VERSIONS.formatted(functionId, "Removed"));
        log.info(mesg);
        return new AuthorizedPartiesResponse(dto);
    }

    public AuthorizedPartiesResponse deleteAuthorizedParties(
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId,
            Authentication authentication) {
        var function = getCandidateFunction(ncaId, functionId, optVersionId,
                                            fun -> privateFunctionMatch(ncaId, authentication, fun),
                                            "delete");
        validateNcaId(function, ncaId, functionId, optVersionId);
        var dto = authorizedPartiesService.deleteAuthorizedParties(functionId, optVersionId);
        var mesg = optVersionId
                    .map(versionId -> MESG_AUTH_PARTY_SPECIFIC_VERSION
                                        .formatted(functionId, versionId, "Deleted"))
                    .orElseGet(() -> MESG_AUTH_PARTY_ALL_VERSIONS.formatted(functionId, "Deleted"));
        log.info(mesg);
        return new AuthorizedPartiesResponse(dto);
    }

    private void validateCreateAuthorizedPartyRequest(
            String ncaId,
            AuthorizedPartiesRequest request) {
        var wildcardMatch = request.authorizedParties()
                .stream()
                .anyMatch(authorizedPartyDto -> authorizedPartyDto.ncaId().equals(WILDCARD));
        if (wildcardMatch && request.authorizedParties().size() > 1) {
            // When wildcard NCA Id is specified as an authz account, there should not be other
            // authz parties in the request.
            var mesg = MESG_EXTRA_AZPS_WITH_WILDCARD_AZP;
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if (wildcardMatch
                && StringUtils.isNotBlank(request.authorizedParties().getFirst().clientId())) {
            // When wildcard NCA Id is specified as an authz account, the clientId
            // should not be specified in the request.
            var mesg = MESG_CLIENT_ID_SPECIFIED_WITH_WILDCARD_AZP;
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        request.authorizedParties()
                        .forEach(azp -> validateAuthorizedParty(ncaId, azp));
    }

    private void validatePatchAuthorizedPartyRequest(
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId,
            PatchAuthorizedPartyRequest request,
            String operation) {
        // Is wildcard(*) NCA ID being added/removed as an authorized account?
        if (request.authorizedParty().ncaId().equals(WILDCARD)) {
            var mesg = optVersionId
                    .map(versionId -> MESG_INVALID_AUTHZ_ACCOUNT_VERSION
                            .formatted(functionId, versionId, WILDCARD, operation))
                    .orElseGet(() -> MESG_INVALID_AUTHZ_ACCOUNT_FUNCTION
                            .formatted(functionId, WILDCARD, operation));
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        validateAuthorizedParty(ncaId, request.authorizedParty());
    }

    private String validateAuthorizedParty(
            String ncaId,
            AuthorizedPartyDto authorizedPartyDto) {
        // Primary / main NCA Id should not be specified as an authorized account.
        if (authorizedPartyDto.ncaId().equals(ncaId)) {
            var mesg = MESG_INVALID_AUTHORIZED_PARTY.formatted(ncaId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        // If authorized client is specified, then there must be a corresponding entry in
        // the client table in the DB matching the specified authorized account.
        if (StringUtils.isNotBlank(authorizedPartyDto.clientId())) {
            ClientEntity clientEntity;
            try {
                clientEntity = clientService.lookupClientOrThrow(authorizedPartyDto.clientId());
            } catch (NotFoundException ex) {
                log.error(ex.getMessage());
                throw new BadRequestException(ex.getMessage(), ex);
            }

            if (!clientEntity.getNcaId().equals(authorizedPartyDto.ncaId())) {
                var mesg = MESG_INVALID_AUTH_PARTY
                        .formatted(authorizedPartyDto.ncaId(), authorizedPartyDto.clientId());
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        }

        // A non-wildcard authz account must have an entry in the accounts table.
        if (!authorizedPartyDto.ncaId().equals(WILDCARD)) {
            try {
                accountService.assertAccountExistsOrThrow(authorizedPartyDto.ncaId());
            } catch (NotFoundException ex) {
                log.error(ex.getMessage());
                throw new BadRequestException(ex.getMessage(), ex);
            }
        }

        return ncaId;
    }

    private FunctionEntity getCandidateFunction(
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId,
            Predicate<FunctionEntity> authForPrivateFunction,
            String operation) {
        // If versionId is specified, then pick an exact function. Otherwise, pick the first entry.
        var functionEntity = optVersionId
                .map(versionId -> functionLookupService
                        .lookupUsingAccountIdAndFunctionIdAndVersionIdOrThrow(ncaId,
                                                                              functionId,
                                                                              versionId))
                .orElseGet(() -> functionLookupService
                        .lookupUsingAccountIdAndFunctionIdOrThrow(ncaId, functionId)
                        .getFirst());

        // Verify whether the auth token in the request allows the function to be operated upon.
        if (!authForPrivateFunction.test(functionEntity)) {
            var versionId = functionEntity.getFunctionVersionId();
            var mesg = MESG_FORBIDDEN_TO_OPERATE.formatted(functionId, versionId, operation);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
        return functionEntity;
    }

    private static FunctionEntity validateNcaId(
            FunctionEntity entity,
            String ncaId,
            UUID functionId,
            Optional<UUID> optVersionId) {
        // Verify that ncaId on the function matches with the passed in ncaId.
        if (!entity.getNcaId().equals(ncaId)) {
            var mesg = optVersionId
                        .map(versionId -> MESG_FUNCTION_VERSION_NOT_FOUND.formatted(functionId,
                                                                                    versionId,
                                                                                    ncaId))
                        .orElseGet(() -> MESG_FUNCTION_NOT_FOUND.formatted(functionId, ncaId));
            log.debug(mesg);
            throw new NotFoundException(mesg);
        }
        return entity;
    }
}
