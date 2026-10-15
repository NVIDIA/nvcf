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
package com.nvidia.icms.inbound.rest.controllers;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.service.internal.InternalInstanceService;
import com.nvidia.icms.util.AuthUtils;
import com.nvidia.icms.util.audit.AuditUtils;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;

import java.util.*;

import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@Tag(name = "Instance Internal APIs")
@RequestMapping(path = "/v1/sirs", produces = APPLICATION_JSON_VALUE)
public class InternalInstanceController {

    private final InternalInstanceService internalInstanceService;


    public InternalInstanceController(
            InternalInstanceService internalInstanceService) {
        this.internalInstanceService = internalInstanceService;
    }

    @PutMapping("/{spotInstanceRequestId}")
    @PreAuthorize("hasAuthority('spot-status-update') or hasAuthority('instance-status-update') or hasAuthority('instance_request_update') or hasAuthority('apikey:nvca-cluster')")
    @Operation(summary = "Update instance request status",
            description = "Request to update the status of an instance request",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "412", description = "Precondition Failed"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public void updateInstanceRequestStatus(
            HttpServletRequest request,
            @Schema(description =
                    "Instance request id for updating status of instance request")
            @PathVariable("spotInstanceRequestId")
            String instanceRequestId,
            @Valid @RequestBody
            SpotInstanceRequestStatusUpdateRequest instanceRequestStatusUpdateRequest) {
        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);
        internalInstanceService.updateInstanceRequestStatus(
                AuthUtils.getSubOrClusterIdFromSecurityContext(),
                instanceRequestId,
                instanceRequestStatusUpdateRequest, auditProps);
    }

    @PostMapping("/{spotInstanceRequestId}/{instanceId}")
    @PreAuthorize("hasAuthority('spot-status-update') or hasAuthority('instance-status-update') or hasAuthority('instance_request_update') or hasAuthority('apikey:nvca-cluster')")
    @Operation(summary = "Update instance status",
            description = "Update status of an instance associated with an instanceId and requestId",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public void updateInstanceStatus(
            HttpServletRequest request,
            @Schema(description = "Instance request id for updating status of instance request")
            @PathVariable("spotInstanceRequestId")
            String instanceRequestId,
            @Schema(description = "Instance id for updating status of instance")
            @PathVariable("instanceId")
            String instanceId,
            @Valid @RequestBody SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest) {

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);
        internalInstanceService.updateInstanceStatus(instanceRequestId, instanceId,
                                                     instanceStatusUpdateRequest,
                                                     AuthUtils.getSubOrClusterIdFromSecurityContext(),
                                                     auditProps);
    }

}
