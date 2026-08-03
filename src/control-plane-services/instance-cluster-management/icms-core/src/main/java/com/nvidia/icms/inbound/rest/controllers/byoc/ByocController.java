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
package com.nvidia.icms.inbound.rest.controllers.byoc;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationCredentialsResponse;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.byoc.BartRegistrationResponse;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterHeartbeatRequest;
import com.nvidia.icms.inbound.rest.model.nvca.NvcaClusterHeartbeatRequest;
import com.nvidia.icms.service.ByocService;
import com.nvidia.icms.util.AuthUtils;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.util.audit.AuditUtils;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import java.util.Map;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@Tag(name = "Bring your own Cluster")
@RequestMapping(path = "/v1/bart", produces = APPLICATION_JSON_VALUE)
public class ByocController {

    @Autowired
    private ByocConfigurationProperties byocConfigurationProperties;

    @Autowired
    private ByocService byocService;

    @PostMapping
    @PreAuthorize("hasAuthority('byoc_registration')")
    @Operation(summary = "Cluster Registration",
            description = "API to register a new backend",
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
            },
            requestBody = @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    content = @Content(
                            mediaType = APPLICATION_JSON_VALUE,
                            schema = @Schema(implementation = BartRegistrationRequest.class)
                    )
            ))
    public BartRegistrationResponse registerBackend(
            HttpServletRequest request,
            @Valid @RequestBody BartRegistrationRequest bartRegistrationRequest
    ) {

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);

        return byocService.registerCluster(bartRegistrationRequest,
                                           AuthUtils.getSubFromSecurityContext(), auditProps);
    }

    // todo :- Remove this as it is not in SDD or reserve this for admin scope
    @DeleteMapping
    @PreAuthorize("hasAuthority('byoc_registration')")
    public void deleteBackend(
            HttpServletRequest request) {

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);
        byocService.deleteCluster(AuthUtils.getSubFromSecurityContext(), auditProps);
    }

    @PutMapping("/heartbeat")
    @PreAuthorize("hasAuthority('cluster_heartbeat')")
    @Operation(summary = "Cluster heartbeat",
            description = "API to send heartbeat of a cluster",
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
            },
            requestBody = @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    content = @Content(
                            mediaType = APPLICATION_JSON_VALUE,
                            schema = @Schema(implementation = ClusterHeartbeatRequest.class)
                    )
            ))
    public void clusterHeartbeat(
            HttpServletRequest request,
            @Valid @RequestBody ClusterHeartbeatRequest clusterHeartbeatRequest
    ) {
        logHeartReceived(clusterHeartbeatRequest, AuthUtils.getSubFromSecurityContext());
        byocService.registerClusterHeartbeat(clusterHeartbeatRequest,
                                             AuthUtils.getSubFromSecurityContext());
    }

    @GetMapping("/creds")
    @PreAuthorize("hasAuthority('byoc_registration')")
    @Operation(summary = "Get credentials for your backend",
            description = "Request to get credentials for the registered backend",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public BartRegistrationCredentialsResponse getCredsForRegisteredBackend(
            HttpServletRequest request) {

        return byocService.getClusterQueuesInfo(
                AuthUtils.getSubFromSecurityContext());
    }

    private void logHeartReceived(ClusterHeartbeatRequest heartbeatRequest, String zone) {
        try {
            log.info(
                    "HEART_BEAT_LOGGING, API: /v1/bart/heartbeat, cluster {}, updateRequestJson {},",
                    zone, GsonCompatMapper.toJson(heartbeatRequest));
        } catch (Exception exception) {
            log.error(
                    "HEART_BEAT_LOGGING: Failed to log heartbeat received from BART using /v1/bart/heartbeat API, error: {}, exception: ",
                    exception.getMessage(), exception);
        }
    }
}
