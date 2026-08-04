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
package com.nvidia.icms.inbound.rest.controllers.tenantregistration;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.inbound.rest.model.tenantregistration.TenantRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.tenantregistration.TenantRegistrationResponse;
import com.nvidia.icms.service.tenantregistration.TenantRegistrationService;
import com.nvidia.icms.util.audit.AuditUtils;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import java.util.Map;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

/**
 * REST API for tenant (e.g. GDN) registration with SIS.
 * Allows tenants to register and receive a unique registration token for use during NVCF function creation.
 */
@Slf4j
@RestController
@Tag(name = "Tenant Registration")
@RequestMapping(path = "/v1/si/accounts", produces = APPLICATION_JSON_VALUE)
@RequiredArgsConstructor
public class TenantRegistrationController {

    private final TenantRegistrationService tenantRegistrationService;

    @PostMapping("{ncaId}/{tenant}/registrations")
    @PreAuthorize("hasAuthority('tenant_registration')")
    @Operation(
            summary = "Register a tenant application",
            description = "Register a new tenant application (e.g. GDN) with SIS. Returns a unique registration token for use during NVCF function creation. Requires JWT with tenant_registration scope.",
            requestBody = @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    content = @Content(
                            mediaType = APPLICATION_JSON_VALUE,
                            schema = @Schema(implementation = TenantRegistrationRequest.class)
                    )
            ),
            responses = {
                    @ApiResponse(
                            responseCode = "201",
                            description = "Created - Tenant application registered and token generated",
                            content = @Content(schema = @Schema(implementation = TenantRegistrationResponse.class))
                    ),
                    @ApiResponse(responseCode = "400", description = "Bad Request - Invalid request body or missing required fields"),
                    @ApiResponse(responseCode = "401", description = "Unauthorized - JWT invalid, expired, or not present"),
                    @ApiResponse(responseCode = "403", description = "Forbidden - Missing tenant_registration permission"),
                    @ApiResponse(responseCode = "500", description = "Internal Server Error")
            })
    public ResponseEntity<TenantRegistrationResponse> register(
            @Schema(description = "NVIDIA Cloud Account ID", requiredMode = Schema.RequiredMode.REQUIRED)
            @PathVariable("ncaId") String ncaId,
            @Schema(description = "Tenant identifier (e.g. gdn)", requiredMode = Schema.RequiredMode.REQUIRED)
            @PathVariable("tenant") String tenant,
            @Valid @RequestBody TenantRegistrationRequest request,
            HttpServletRequest httpRequest) {
        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(httpRequest);
        TenantRegistrationResponse response = tenantRegistrationService.register(ncaId, tenant, request, auditProps);
        return ResponseEntity.status(HttpStatus.CREATED).body(response);
    }

    @DeleteMapping("{ncaId}/{tenant}/registrations/{registrationId}")
    @PreAuthorize("hasAuthority('tenant_registration')")
    @Operation(
            summary = "Delete a tenant registration",
            description = "Delete a tenant registration token. Only the tenant and ncaId that created the registration can delete it. Requires JWT with tenant_registration scope.",
            responses = {
                    @ApiResponse(
                            responseCode = "204",
                            description = "No Content - Registration successfully deleted"
                    ),
                    @ApiResponse(responseCode = "400", description = "Bad Request - Invalid request parameters"),
                    @ApiResponse(responseCode = "401", description = "Unauthorized - JWT invalid, expired, or not present"),
                    @ApiResponse(responseCode = "403", description = "Forbidden - Missing tenant_registration permission or registration belongs to different ncaId/tenant"),
                    @ApiResponse(responseCode = "404", description = "Not Found - Registration ID not found"),
                    @ApiResponse(responseCode = "500", description = "Internal Server Error")
            })
    public ResponseEntity<Void> delete(
            @Schema(description = "NVIDIA Cloud Account ID", requiredMode = Schema.RequiredMode.REQUIRED)
            @PathVariable("ncaId") String ncaId,
            @Schema(description = "Tenant identifier (e.g. gdn)", requiredMode = Schema.RequiredMode.REQUIRED)
            @PathVariable("tenant") String tenant,
            @Schema(description = "Registration ID to delete", requiredMode = Schema.RequiredMode.REQUIRED)
            @PathVariable("registrationId") UUID registrationId,
            HttpServletRequest httpRequest) {
        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(httpRequest);
        tenantRegistrationService.delete(ncaId, tenant, registrationId, auditProps);
        return ResponseEntity.noContent().build();
    }
}
