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
package com.nvidia.nvcf.rest.registry;

import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_ARTIFACT_TYPE_ENUMS;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_PROVISIONED_BY_ENUMS;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.rest.registry.dto.AddRegistryCredentialRequest;
import com.nvidia.nvcf.rest.registry.dto.ListRegistryCredentialDetailsResponse;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.rest.registry.dto.RecognizedRegistriesResponse;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsResponse;
import com.nvidia.nvcf.rest.registry.dto.UpdateRegistryCredentialRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.NonNull;
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
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Tag(name = "Registry Credential Management",
        description = """
                Defines Registry Credential Management endpoints. These endpoints can only be
                 invoked by Account Admins and require a bearer token in HTTP Authorization header
                 with 'manage_registry_credentials' scope.
                """
)
public class RegistryCredentialManagementController {

    private static final String AUTH_DESCRIPTION = """
            Requires a bearer token in the HTTP Authorization header with 'manage_registries' scope.
            """;

    private final RegistryCredentialManagementFacade registryFacade;
    private final AccountService accountService;
    private final RateLimiterService rateLimiterService;
    private final Tracer tracer;

    @PostMapping(value = {"/v2/nvcf/registry-credentials"}, consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Add new registry credential to the account",
            description = """
                    Adds a new registry credential to the account.
                    """ + AUTH_DESCRIPTION)
    @PreAuthorize(
            "hasAnyAuthority('manage_registry_credentials', 'apikey:manage_registry_credentials')")
    public RegistryCredentialDetailsResponse addRegistryCredential(
            @Valid @RequestBody AddRegistryCredentialRequest addRegistryCredentialRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        String ncaId = accountService.getNcaId(authentication);
        rateLimiterService.verifyLimits(ncaId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return registryFacade.addRegistryCredential(ncaId,
                                                    addRegistryCredentialRequest,
                                                    httpServletRequest,
                                                    authentication);
    }

    @GetMapping("/v2/nvcf/registry-credentials")
    @Operation(
            summary = "List Registry Credentials",
            description = """
                    Lists all the registry credentials associated with the authenticated
                     NVIDIA Cloud Account.
                    """ + AUTH_DESCRIPTION)
    @PreAuthorize(
            "hasAnyAuthority('manage_registry_credentials', 'apikey:manage_registry_credentials')")
    public ListRegistryCredentialDetailsResponse listRegistryCredentialDetails(
            @Parameter(description = """
                    Filters registry credentials using the specified 'artifactType' query param.
                    """)
            @RequestParam(required = false, defaultValue = DEFAULT_ARTIFACT_TYPE_ENUMS)
            Set<ArtifactTypeEnum> artifactType,
            @Parameter(description = """
                    Filters registry credentials using the specified 'provisionedBy' query param.
                    """)
            @RequestParam(required = false, defaultValue = DEFAULT_PROVISIONED_BY_ENUMS)
            Set<ProvisionedByEnum> provisionedBy,
            @NonNull Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        rateLimiterService.verifyLimits(ncaId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return registryFacade.listRegistryCredentials(ncaId, artifactType, provisionedBy);
    }

    @GetMapping("/v2/nvcf/registry-credentials/{registryCredentialId}")
    @Operation(
            summary = "Get Registry Credential Details",
            description = """
                    Retrieves detailed information of the specified registry credential
                     associated with the authenticated NVIDIA Cloud Account.
                    """ + AUTH_DESCRIPTION)
    @PreAuthorize("hasAnyAuthority('manage_registry_credentials', 'apikey:manage_registry_credentials')")
    public RegistryCredentialDetailsResponse getRegistryCredentialDetails(
            @Parameter(description = "Registry Credential id", required = true)
            @PathVariable UUID registryCredentialId,
            @NonNull Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        rateLimiterService.verifyLimits(ncaId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return registryFacade.getRegistryCredentialDetails(ncaId, registryCredentialId);
    }

    @PatchMapping(value = {"/v2/nvcf/registry-credentials/{registryCredentialId}"},
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Update registry credential",
            description = """
                    Updates the secret and/or the artifact types associated with the specified
                     registry credential. Artifact types specified in the request body are
                     added to the set of artifact types that already exist for the specified
                     registry credential.
                    """ + AUTH_DESCRIPTION)
    @PreAuthorize(
            "hasAnyAuthority('manage_registry_credentials', 'apikey:manage_registry_credentials')")
    public RegistryCredentialDetailsResponse updateRegistryCredential(
            @Parameter(description = "Registry Credential id", required = true)
            @PathVariable UUID registryCredentialId,
            @Valid @RequestBody UpdateRegistryCredentialRequest updateRegistryCredentialRequest,
            @NonNull HttpServletRequest httpServletRequest,
            @NonNull Authentication authentication) {
        String ncaId = accountService.getNcaId(authentication);
        rateLimiterService.verifyLimits(ncaId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return registryFacade.updateRegistryCredential(ncaId,
                                                       registryCredentialId,
                                                       updateRegistryCredentialRequest,
                                                       httpServletRequest,
                                                       authentication);
    }

    @DeleteMapping("/v2/nvcf/registry-credentials/{registryCredentialId}")
    @Operation(
            summary = "Delete Registry Credential",
            description = """
                    Deletes the specified registry credential associated with the authenticated
                     NVIDIA Cloud Account.
                    """ + AUTH_DESCRIPTION,
            responses = @ApiResponse(responseCode = "204")
    )
    @PreAuthorize(
            "hasAnyAuthority('manage_registry_credentials', 'apikey:manage_registry_credentials')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteRegistryCredential(
            @Parameter(description = "Registry Credential id", required = true)
            @NonNull @PathVariable("registryCredentialId") UUID registryCredentialId,
            @NonNull HttpServletRequest httpServletRequest,
            @NonNull Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        rateLimiterService.verifyLimits(ncaId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        registryFacade.deleteRegistryCredential(ncaId,
                                                registryCredentialId,
                                                httpServletRequest,
                                                authentication);
    }

    @GetMapping("/v2/nvcf/recognized-registries")
    @Operation(
            summary = "List Recognized Registries",
            description = """
                    Lists all the registries that are recognized by NVCF. Only when a registry
                     is recognized by NVCF that the users can add its credential to their account
                     and then use it to create functions.
                    """ + AUTH_DESCRIPTION)
    @PreAuthorize(
            "hasAnyAuthority('manage_registry_credentials', 'apikey:manage_registry_credentials')")
    public RecognizedRegistriesResponse getRecognizedRegistries(
            @NonNull Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        rateLimiterService.verifyLimits(ncaId);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return registryFacade.getRecognizedRegistries();
    }
}
