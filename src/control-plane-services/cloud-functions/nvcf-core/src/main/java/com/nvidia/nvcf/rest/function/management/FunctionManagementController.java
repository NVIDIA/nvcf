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
package com.nvidia.nvcf.rest.function.management;

import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_VISIBILITY;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionIdsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.VisibilityEnum;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
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
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.validation.annotation.Validated;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@Validated
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Tag(name = "Function Management",
        description = """
                Defines Function Management endpoints for Account Admins. All the endpoints
                 defined in this API require a bearer token with appropriate scope in
                 the HTTP Authorization header. The scope is specified in the documentation
                 for each of the endpoints.
                """
)
public class FunctionManagementController {

    private static final String MESG_INVALID_PF_CREATE =
            "Function id '%s': Cannot create version of a public function";
    private static final String MESG_INVALID_PF_DELETE =
            "Function id '%s', version '%s': Cannot delete a public function";

    private final AccountService accountService;
    private final AuthorizedPartiesService azpsService;
    private final FunctionManagementFacade functionManagementFacade;
    private final RateLimiterService rateLimiterService;
    private final Tracer tracer;

    @PostMapping(value = "/v2/nvcf/functions", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Create Function",
            description = """
                    Creates a new function within the authenticated NVIDIA Cloud Account. Requires a
                     bearer token with 'register_function' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('register_function', 'apikey:register_function')")
    public CreateFunctionResponse createFunction(
            @Valid @NonNull @RequestBody CreateFunctionRequest createRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return functionManagementFacade.createFunction(ncaId,
                                                       Optional.empty(),
                                                       createRequest,
                                                       httpServletRequest,
                                                       authentication);
    }

    @PostMapping(value = "/v2/nvcf/functions/{functionId}/versions",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Create Function Version",
            description = """
                    Creates a version of the specified function within the authenticated NVIDIA
                     Cloud Account. If the function is public, then Account Admin cannot create
                     a version. Requires a bearer token with 'register_function' scope in the
                     HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('register_function', 'apikey:register_function')")
    public CreateFunctionResponse createFunction(
            @Parameter(description = "Function id", required = true)
            @NonNull @PathVariable("functionId") UUID functionId,
            @Valid @NonNull @RequestBody CreateFunctionRequest createRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        azpsService.verifyNotAPublicFunction(functionId,
                                             () -> MESG_INVALID_PF_CREATE.formatted(functionId));
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, functionId);
        return functionManagementFacade.createFunction(ncaId,
                                                       Optional.of(functionId),
                                                       createRequest,
                                                       httpServletRequest,
                                                       authentication);
    }

    @GetMapping("/v2/nvcf/functions")
    @Operation(
            summary = "List Functions",
            description = """
                    Lists all the functions associated with the authenticated NVIDIA Cloud Account.
                     Requires a bearer token  with 'list_functions' or 'list_functions_details'
                     scope in the HTTP Authorization header."""
    )
    @PreAuthorize("hasAnyAuthority('list_functions', 'list_functions_details', " +
            "'apikey:list_functions', 'apikey:list_functions_details')")
    public ListFunctionsResponse getFunctions(
            @Parameter(description = """
                    Query param 'visibility' indicates the kind of functions to be included
                     in the response.
                    """)
            @RequestParam(required = false,
                    defaultValue = DEFAULT_VISIBILITY) Set<VisibilityEnum> visibility,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return functionManagementFacade.getFunctions(ncaId,
                                                     authentication,
                                                     visibility);
    }

    @GetMapping("/v2/nvcf/functions/{functionId}/versions")
    @Operation(
            summary = "List Function Versions",
            description = """
                    Lists details of all the versions of the specified function in the authenticated
                     NVIDIA Cloud Account. Requires a bearer token  with 'list_functions' or
                     'list_functions_details' scopes in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('list_functions', 'list_functions_details', " +
            "'apikey:list_functions', 'apikey:list_functions_details')")
    public ListFunctionsResponse getFunctions(
            @Parameter(description = "Function id", required = true)
            @NonNull @PathVariable("functionId") UUID functionId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, functionId);
        return functionManagementFacade.getFunctions(ncaId, functionId, authentication);
    }

    @GetMapping("/v2/nvcf/functions/{functionId}/versions/{functionVersionId}")
    @Operation(
            summary = "Get Function Version Details",
            description = """
                    Retrieves detailed information of the specified function version in the
                     authenticated NVIDIA Cloud Account. Requires a bearer token with
                     'list_functions' or 'list_functions_details' scopes in the HTTP
                     Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('list_functions', 'list_functions_details', " +
            "'apikey:list_functions', 'apikey:list_functions_details')")
    public FunctionResponse getFunction(
            @Parameter(description = "Function id", required = true)
            @NonNull @PathVariable("functionId") UUID functionId,
            @Parameter(description = "Version id ", required = true)
            @NonNull @PathVariable("functionVersionId") UUID functionVersionId,
            @Parameter(description = """
                    Query param 'includeSecrets' indicates whether to include secret names
                     for the function in the response.
                    """)
            @RequestParam(required = false, defaultValue = "true") boolean includeSecrets,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, functionId, functionVersionId);
        return functionManagementFacade.getFunction(ncaId, functionId, functionVersionId,
                                                    includeSecrets, authentication);
    }

    @GetMapping("/v2/nvcf/functions/ids")
    @Operation(
            summary = "List Function Ids",
            description = """
                    Lists ids of all the functions in the authenticated NVIDIA Cloud Account.
                     Requires a bearer token with 'list_functions' or 'list_functions_details'
                     scopes in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('list_functions', 'list_functions_details', " +
            "'apikey:list_functions', 'apikey:list_functions_details')")
    public ListFunctionIdsResponse getFunctionIds(
            @Parameter(description = """
                    Query param 'visibility' indicates the kind of functions to be included
                     in the response.
                    """)
            @RequestParam(required = false,
                    defaultValue = DEFAULT_VISIBILITY) Set<VisibilityEnum> visibility,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return functionManagementFacade.getFunctionIds(ncaId, authentication, visibility);
    }

    @DeleteMapping("/v2/nvcf/functions/{functionId}/versions/{functionVersionId}")
    @Operation(
            summary = "Delete Function",
            description = """
                    Deletes the specified function version in the authenticated NVIDIA Cloud
                     Account. Requires a bearer token with 'delete_function' scope in the HTTP
                     Authorization header. If the function version is public, then Account Admin
                     cannot delete the function.
                    """,
            responses = @ApiResponse(responseCode = "204")
    )
    @PreAuthorize("hasAnyAuthority('delete_function', 'apikey:delete_function')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteFunction(
            @Parameter(description = "Function id", required = true)
            @NonNull @PathVariable("functionId") UUID functionId,
            @Parameter(description = "Version id", required = true)
            @NonNull @PathVariable("functionVersionId") UUID functionVersionId,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        azpsService.verifyNotAPublicFunction(functionId, functionVersionId,
                                             () -> MESG_INVALID_PF_DELETE
                                                     .formatted(functionId, functionVersionId));
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, functionId, functionVersionId);
        functionManagementFacade.deleteFunction(ncaId,
                                                functionId,
                                                functionVersionId,
                                                httpServletRequest,
                                                authentication);
    }

    @PutMapping(value = "/v2/nvcf/metadata/functions/{functionId}/versions/{functionVersionId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Update Function Metadata",
            deprecated = true,
            description = """
                    Deprecated: use PUT /v2/nvcf/functions/{functionId}/versions/{functionVersionId}
                    to update tags, rate limit, model-specific LLM config, and other mutable
                    function fields. This legacy endpoint updates the same mutable function version
                    fields for the authenticated NVIDIA Cloud Account and requires a bearer token
                    with 'update_function' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('update_function', 'apikey:update_function')")
    @Deprecated
    public FunctionResponse updateFunctionMetadata(
            @Parameter(description = "Function id", required = true)
            @NonNull @PathVariable("functionId") UUID functionId,
            @Parameter(description = "Version id", required = true)
            @NonNull @PathVariable("functionVersionId") UUID functionVersionId,
            @Valid @NonNull @RequestBody UpdateFunctionRequest updateRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, functionId, functionVersionId);
        return functionManagementFacade.updateFunction(ncaId,
                                                       functionId,
                                                       functionVersionId,
                                                       updateRequest,
                                                       httpServletRequest,
                                                       authentication);
    }

    @PutMapping(value = "/v2/nvcf/functions/{functionId}/versions/{functionVersionId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Updates mutable fields of the specified function version.",
            description = """
                    Updates mutable fields of the specified function version within the
                    authenticated NVIDIA Cloud Account. The current request payload supports tags
                    updates, rate limit updates, and model-specific LLM config updates. Requires
                    a bearer token with 'update_function' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('update_function', 'apikey:update_function')")
    public FunctionResponse updateFunction(
            @Parameter(description = "Function id", required = true)
            @NonNull @PathVariable("functionId") UUID functionId,
            @Parameter(description = "Version id", required = true)
            @NonNull @PathVariable("functionVersionId") UUID functionVersionId,
            @Valid @NonNull @RequestBody UpdateFunctionRequest updateRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, functionId, functionVersionId);
        return functionManagementFacade.updateFunction(ncaId,
                                                       functionId,
                                                       functionVersionId,
                                                       updateRequest,
                                                       httpServletRequest,
                                                       authentication);
    }
}
