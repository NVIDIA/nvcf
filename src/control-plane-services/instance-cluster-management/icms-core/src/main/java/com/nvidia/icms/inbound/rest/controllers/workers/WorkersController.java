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
package com.nvidia.icms.inbound.rest.controllers.workers;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.workers.WorkerTokenIntrospectRequest;
import com.nvidia.icms.inbound.rest.model.workers.WorkerTokenIntrospectResponse;
import com.nvidia.icms.service.byoc.nvca.NvcaTokenVerificationService;
import com.nvidia.icms.service.workers.WorkerTokenVerificationService;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.ResponseEntity;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

/**
 * Worker token introspection endpoint (RFC 7662).
 *
 * <p>Public endpoint — no authentication required. Enabled only when
 * {@code icms.nvca.oidcClusterIdentityEnabled} is true (self-hosted deployments).
 * Returns HTTP 404 for managed NVCF where the feature flag is off.</p>
 */
@Slf4j
@RequiredArgsConstructor
@RestController
@Tag(name = "Workers")
@RequestMapping(path = "/v1/workers", produces = APPLICATION_JSON_VALUE)
public class WorkersController {

    private final NvcaConfigurationProperties nvcaConfig;
    private final WorkerTokenVerificationService workerTokenVerificationService;

    @PostMapping("tokens/introspect")
    @Operation(summary = "Worker token introspection (RFC 7662)",
            description = "Verify a worker PSAT or SPIFFE JWT and return resolved worker identity. "
                    + "Public endpoint — no authentication required.",
            responses = {
                    @ApiResponse(responseCode = "200",
                            description = "Introspection result (active or inactive)"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)),
                            responseCode = "400", description = "Missing or empty token"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)),
                            responseCode = "401", description = "JWT signature verification failed"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)),
                            responseCode = "403", description = "No cluster for the token audience"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)),
                            responseCode = "404", description = "Feature flag disabled"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)),
                            responseCode = "431", description = "JWT too large")
            })
    public ResponseEntity<WorkerTokenIntrospectResponse> introspectWorkerToken(
            @Valid @RequestBody WorkerTokenIntrospectRequest request) {

        if (!nvcaConfig.isOidcClusterIdentityEnabled()) {
            return ResponseEntity.notFound().build();
        }

        WorkerTokenVerificationService.Outcome outcome =
                workerTokenVerificationService.verify(request.getToken());

        if (!outcome.isActive()) {
            return mapRejection(outcome);
        }

        Jwt jwt = outcome.getJwt();
        return ResponseEntity.ok(WorkerTokenIntrospectResponse.builder()
                .active(true)
                .sub(jwt.getSubject())
                .aud(outcome.getAudience())
                .iss(jwt.getIssuer() != null ? jwt.getIssuer().toString() : null)
                .clientId(outcome.getClusterId())
                .instanceId(outcome.getInstanceId())
                .workerId(outcome.getWorkerId())
                .tokenType(outcome.getTokenType())
                .build());
    }

    private static final String GENERIC_REJECTION_MESSAGE = "JWT verification failed";

    private ResponseEntity<WorkerTokenIntrospectResponse> mapRejection(
            WorkerTokenVerificationService.Outcome outcome) {
        // Log the specific reason server-side; the caller receives only the
        // generic message so JWT library internals and registration state do
        // not leak to untrusted callers.
        log.debug("Worker token rejected: reason={} detail={}",
                outcome.getReason(), outcome.getErrorMessage());
        NvcaTokenVerificationService.RejectReason reason = outcome.getReason();
        if (reason == null) {
            return ResponseEntity.ok(
                    WorkerTokenIntrospectResponse.inactive(GENERIC_REJECTION_MESSAGE));
        }
        return switch (reason) {
            case TOKEN_TOO_LARGE ->
                    ResponseEntity.status(431).body(
                            WorkerTokenIntrospectResponse.inactive(GENERIC_REJECTION_MESSAGE));
            case UNKNOWN_CLUSTER ->
                    ResponseEntity.status(403).body(
                            WorkerTokenIntrospectResponse.inactive(GENERIC_REJECTION_MESSAGE));
            case MISSING_TOKEN, INVALID_AUDIENCE, SIGNATURE_INVALID ->
                    ResponseEntity.ok(
                            WorkerTokenIntrospectResponse.inactive(GENERIC_REJECTION_MESSAGE));
        };
    }
}
