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
package com.nvidia.nvcf.rest.ratelimit;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.function.management.FunctionManagementFacade;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.UUID;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.validation.annotation.Validated;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@Validated
@RequestMapping(value = "/v2/nvcf/accounts/{ncaId}/ratelimit/", produces = APPLICATION_JSON_VALUE)
@Tag(name = "Cross-Account User Ratelimit Management for NVIDIA Super Admins",
        description = """
                Defines User Ratelimit Management endpoints for NVIDIA Super Admins to work across
                 accounts. All the endpoints defined in this API require a bearer token
                 with 'admin:update_function' scope in the HTTP Authorization header."""
)

public class XAccountRatelimitManagementController {

    private static final String NCA_ID_DESCRIPTION = "NVIDIA Cloud Account Id";

    private final RatelimitManagementFacade ratelimitManagementFacade;
    private final FunctionManagementFacade functionManagementFacade;
    private final AccountService accountService;
    private final RateLimiterService rateLimiterService;
    private final Tracer tracer;

    @PutMapping(value = "functions/{functionId}/versions/{versionId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Update user ratelimit policy for a function version",
            deprecated = true,
            description = """
                    Deprecated: use PUT /v2/nvcf/accounts/{ncaId}/functions/{functionId}/versions/{versionId}
                     to update ratelimit and other mutable function fields. This legacy endpoint
                     only updates ratelimit for the specified function version and requires a
                     bearer token with 'admin:update_function' scope in the HTTP Authorization
                     header.
                    """
    )
    @PreAuthorize("hasAuthority('admin:update_function')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    @Deprecated
    public void updateRateLimit(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Function id", required = true)
            @NotNull @PathVariable UUID functionId,
            @Parameter(description = "Function version id", required = true)
            @NotNull @PathVariable UUID versionId,
            @Valid @NonNull @RequestBody UpdateFunctionRequest request,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, functionId, versionId);
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        functionManagementFacade.updateFunction(ncaId,
                                                functionId,
                                                versionId,
                                                request,
                                                httpServletRequest,
                                                authentication);
    }

    @DeleteMapping(value = "functions/{functionId}/versions/{versionId}",
            produces = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Delete user ratelimit policy for a function version",
            description = """
                    Delete ratelimit policy for the specified function version. This endpoint
                     requires a bearer token with 'admin:update_function' scope in the HTTP
                     Authorization header.
                    """
    )
    @PreAuthorize("hasAuthority('admin:update_function')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteRateLimit(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Function id", required = true)
            @NotNull @PathVariable UUID functionId,
            @Parameter(description = "Function version id", required = true)
            @NotNull @PathVariable UUID versionId,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        ratelimitManagementFacade.deleteRateLimit(ncaId, functionId, versionId, authentication);
    }
}
