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
package com.nvidia.nvcf.rest.secret;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.secret.dto.UpdateFunctionSecretsRequest;
import com.nvidia.nvcf.rest.secret.dto.UpdateTelemetrySecretRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
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
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(value = "/v2/nvcf/secrets", produces = APPLICATION_JSON_VALUE)
@Tag(name = "User Secret Management",
        description = """
                Defines User Secret Management endpoints for Account Admins. All the endpoints
                 defined in this API require a bearer token with 'update_secrets' scope in
                 the HTTP Authorization header.
                 """
)
public class SecretManagementController {

    private final SecretManagementFacade secretManagementFacade;
    private final AccountService accountService;
    private final RateLimiterService rateLimiterService;
    private final Tracer tracer;

    private static final String NCA_ID_DESCRIPTION = "NVIDIA Cloud Account Id";
    private static final String FUNCTION_ID_DESCRIPTION = "Function Id";
    private static final String FUNCTION_VERSION_DESCRIPTION = "Function Version Id";

    @PutMapping(value = "functions/{functionId}/versions/{versionId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Update user secrets for a function version",
            description = """
                    Updates secrets for the specified function version. This endpoint
                     requires a bearer token with 'update_secrets' scope in the HTTP
                     Authorization header.
                    """,
            responses = @ApiResponse(responseCode = "204")
    )
    @PreAuthorize("hasAnyAuthority('update_secrets', 'apikey:update_secrets')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void updateFunctionSecrets(
            @Parameter(description = FUNCTION_ID_DESCRIPTION, required = true)
            @NotNull @PathVariable UUID functionId,
            @Parameter(description = FUNCTION_VERSION_DESCRIPTION, required = true)
            @NotNull @PathVariable UUID versionId,
            @Valid @NonNull @RequestBody UpdateFunctionSecretsRequest request,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, functionId, versionId);
        secretManagementFacade.updateFunctionSecrets(ncaId,
                                                     functionId,
                                                     versionId,
                                                     request.secrets(),
                                                     authentication);
    }

    @PutMapping(value = "/telemetries/{telemetryId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Update secret for a specific Telemetry Id",
            description = """
                Updates secret for the specific telemetry Id within NVIDIA Cloud Account. 
                This endpoint requires a bearer token with 'update_secrets' scope in the 
                HTTP Authorization header.
                """,
            responses = @ApiResponse(responseCode = "204")
    )
    @PreAuthorize("hasAnyAuthority('update_secrets', 'apikey:update_secrets')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void updateTelemetrySecret(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @NotNull @PathVariable UUID telemetryId,
            @Valid @NonNull @RequestBody UpdateTelemetrySecretRequest request,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        secretManagementFacade.updateTelemetrySecret(ncaId, telemetryId, request.secret());
    }
}
