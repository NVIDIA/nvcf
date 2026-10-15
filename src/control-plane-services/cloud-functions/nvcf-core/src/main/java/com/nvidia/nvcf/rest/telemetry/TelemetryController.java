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
@RequestMapping(value = "/v2/nvcf/telemetries", produces = "application/json")
@Tag(name = "Telemetry Management",
        description = """
                Defines Telemetry Management endpoints for Account Admins. All the endpoints
                 defined in this API require a bearer token or an api-key with appropriate scope in
                 the HTTP Authorization header.
                 """
)
public class TelemetryController {

    private final TelemetryFacade telemetryFacade;
    private final AccountService accountService;
    private final Tracer tracer;

    @PostMapping(consumes = "application/json")
    @Operation(
            summary = "Create Telemetry",
            description = """
                Creates telemetry endpoints for NVIDIA Cloud Accounts.
                 requires a bearer token with 'manage_telemetries' scope in the HTTP
                 Authorization header.
                """
    )
    @PreAuthorize("hasAnyAuthority('manage_telemetries', 'apikey:manage_telemetries')")
    public TelemetryResponse createTelemetry(
            @Valid @RequestBody TelemetryRequest telemetryRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return telemetryFacade.createTelemetry(ncaId, telemetryRequest);

    }

    @GetMapping
    @Operation(
            summary = "List Telemetries",
            description = """
                Retrieves telemetry configurations for a specific NVIDIA Cloud Account.
                 requires a bearer token with 'manage_telemetries' scope in the HTTP
                 Authorization header.
                """
    )
    @PreAuthorize("hasAnyAuthority('manage_telemetries', 'apikey:manage_telemetries')")
    public ListTelemetryResponse getTelemetriesByAccount(Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return telemetryFacade.listTelemetries(ncaId);
    }

    @GetMapping("/{telemetryId}")
    @Operation(
            summary = "Get Telemetry Details",
            description = """
                Retrieves the details of a specific telemetry configuration by its ID.
                 requires a bearer token with 'manage_telemetries' scope in the HTTP
                 Authorization header.
                """
    )
    @PreAuthorize("hasAnyAuthority('manage_telemetries', 'apikey:manage_telemetries')")
    public TelemetryResponse getTelemetry(
            @PathVariable UUID telemetryId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return telemetryFacade.getTelemetry(ncaId, telemetryId);
    }

    @DeleteMapping("/{telemetryId}")
    @Operation(
            summary = "Delete Telemetry",
            description = """
                Deletes a specific telemetry configuration for a NVIDIA Cloud Account.
                 requires a bearer token with 'manage_telemetries' scope in the HTTP
                 Authorization header. If there any functions that are dependent on the
                 Telemetry that is being deleted, then response with 400 status code will
                 be returned.
                """,
            responses = {
                    @ApiResponse(responseCode = "204"),
                    @ApiResponse(responseCode = "400",
                                 description = "Cannot be deleted till Functions depend on it")
            }
    )
    @PreAuthorize("hasAnyAuthority('manage_telemetries', 'apikey:manage_telemetries')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteTelemetry(
            @PathVariable UUID telemetryId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        telemetryFacade.deleteTelemetry(ncaId, telemetryId);
    }
}
