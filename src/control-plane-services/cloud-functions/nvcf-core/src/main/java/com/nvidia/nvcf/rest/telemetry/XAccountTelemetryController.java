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
package com.nvidia.nvcf.rest.telemetry;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;

import com.nvidia.nvcf.rest.telemetry.dto.ListTelemetryResponse;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryRequest;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryResponse;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import java.util.Map;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(value = "/v2/nvcf/accounts/{ncaId}/telemetries", produces = "application/json")
@Tag(name = "Cross-Account Telemetry Management for NVIDIA Super Admins",
        description = """
                Defines Telemetry Management endpoints for NVIDIA Super Admins to work across
                 accounts. All the endpoints defined in this API require a bearer token
                 with appropriate admin scopes in the HTTP Authorization header."""
)
public class XAccountTelemetryController {

    private final TelemetryFacade telemetryFacade;
    private final AccountService accountService;
    private final RateLimiterService rateLimiterService;
    private final Tracer tracer;

    @PostMapping( consumes = "application/json")
    @Operation(
            summary = "Create telemetry Endpoint Across Accounts",
            description = """
                        Creates telemetry configurations for a specified NVIDIA Cloud Account.
                        Requires a bearer token with 'admin:manage_telemetries' scope
                        in the HTTP Authorization header.
                        """
    )
    @PreAuthorize("hasAuthority('admin:manage_telemetries')")
    public TelemetryResponse createTelemetry(
            @PathVariable String ncaId,
            @Valid @RequestBody TelemetryRequest telemetryRequest,
            Authentication authentication) {
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return telemetryFacade.createTelemetry(ncaId, telemetryRequest);
    }

    @GetMapping()
    @Operation(
            summary = "List Telemetries defined in the specified NVIDIA Cloud Account",
            description = """
                        Retrieves all telemetries for the specified NVIDIA Cloud Account.
                         Requires a bearer token with the 'admin:manage_telemetries' scope in the
                         HTTP Authorization header.
                        """
    )
    @PreAuthorize("hasAuthority('admin:manage_telemetries')")
    public ListTelemetryResponse listTelemetries(
            @PathVariable String ncaId,
            Authentication authentication) {
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return telemetryFacade.listTelemetries(ncaId);
    }

    @GetMapping("{telemetryId}")
    @Operation(
            summary = "Get Telemetry Details",
            description = """
                        Retrieves details of a specific telemetry using telemetry ID.
                         Requires a bearer token with 'admin:manage_telemetries' scope in the
                         HTTP Authorization header.
                        """
    )
    @PreAuthorize("hasAuthority('admin:manage_telemetries')")
    public TelemetryResponse getTelemetry(
            @PathVariable String ncaId,
            @PathVariable UUID telemetryId,
            Authentication authentication) {
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return telemetryFacade.getTelemetry(ncaId, telemetryId);
    }

    @DeleteMapping("{telemetryId}")
    @Operation(
            summary = "Delete Telemetry",
            description = """
                        Deletes a specific telemetry for a NVIDIA Cloud Account. Requires
                         a bearer token with 'admin:manage_telemetries' scope in the
                         HTTP Authorization header. If there is a function dependent on
                         the Telemetry that is being deleted, this will result in response
                         with status code 400.
                        """,
            responses = {
                    @ApiResponse(responseCode = "204"),
                    @ApiResponse(responseCode = "400",
                                 description = "Cannot be deleted till Functions depend on it")
            }
    )
    @PreAuthorize("hasAuthority('admin:manage_telemetries')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteTelemetry(
            @PathVariable String ncaId,
            @PathVariable UUID telemetryId,
            Authentication authentication) {
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        telemetryFacade.deleteTelemetry(ncaId, telemetryId);
    }
}
