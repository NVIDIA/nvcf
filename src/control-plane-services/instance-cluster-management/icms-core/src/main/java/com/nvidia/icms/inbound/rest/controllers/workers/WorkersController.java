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
import com.nvidia.icms.service.workers.WorkerTokenVerificationService;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import java.time.Clock;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

/**
 * Worker token introspection for the NVCF and NVCT relying parties.
 *
 * <p>Not public: callers must hold the {@code worker-token-introspect} authority, which is
 * granted only to the NVCF/NVCT service identities. Requests are rate limited per caller.
 * Enabled only when {@code icms.nvca.oidcClusterIdentityEnabled} is true.</p>
 */
@Slf4j
@RestController
@Tag(name = "Workers")
@RequestMapping(path = "/v1/workers", produces = APPLICATION_JSON_VALUE)
public class WorkersController {

    public static final String INTROSPECT_AUTHORITY = "worker-token-introspect";
    private static final String GENERIC_REJECTION_MESSAGE = "JWT verification failed";

    private final NvcaConfigurationProperties nvcaConfig;
    private final WorkerTokenVerificationService workerTokenVerificationService;
    private final int rateLimitPerMinute;
    private final Clock clock;
    private final Map<String, Window> windows = new ConcurrentHashMap<>();

    public WorkersController(NvcaConfigurationProperties nvcaConfig,
            WorkerTokenVerificationService workerTokenVerificationService,
            @Value("${icms.workers.introspect.rate-limit-per-minute:600}") int rateLimitPerMinute) {
        this(nvcaConfig, workerTokenVerificationService, rateLimitPerMinute, Clock.systemUTC());
    }

    WorkersController(NvcaConfigurationProperties nvcaConfig,
            WorkerTokenVerificationService workerTokenVerificationService,
            int rateLimitPerMinute, Clock clock) {
        this.nvcaConfig = nvcaConfig;
        this.workerTokenVerificationService = workerTokenVerificationService;
        this.rateLimitPerMinute = rateLimitPerMinute;
        this.clock = clock;
    }

    @PostMapping("tokens/introspect")
    @PreAuthorize("hasAuthority('" + INTROSPECT_AUTHORITY + "')")
    @Operation(summary = "Worker token introspection (RFC 7662)",
            description = "Verify a worker PSAT or SPIFFE JWT and return the resolved worker identity and "
                    + "workload binding. Requires the worker-token-introspect authority.",
            responses = {
                    @ApiResponse(responseCode = "200",
                            description = "Introspection result (active or inactive)"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)),
                            responseCode = "400", description = "Missing or empty token"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)),
                            responseCode = "403", description = "Caller lacks the introspect authority"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)),
                            responseCode = "429", description = "Caller exceeded the per-minute rate limit")
            })
    public ResponseEntity<WorkerTokenIntrospectResponse> introspectWorkerToken(
            @Valid @RequestBody WorkerTokenIntrospectRequest request) {

        if (!nvcaConfig.isOidcClusterIdentityEnabled()) {
            return ResponseEntity.ok(WorkerTokenIntrospectResponse.inactive(GENERIC_REJECTION_MESSAGE));
        }
        if (!allow(callerKey())) {
            return ResponseEntity.status(HttpStatus.TOO_MANY_REQUESTS).build();
        }

        WorkerTokenVerificationService.Outcome outcome =
                workerTokenVerificationService.verify(request.getToken());

        if (!outcome.isActive()) {
            // Reason and detail stay server-side; callers get a constant message so
            // registration state and JWT internals do not leak.
            log.debug("Worker token rejected: reason={} detail={}",
                    outcome.getReason(), outcome.getErrorMessage());
            return ResponseEntity.ok(WorkerTokenIntrospectResponse.inactive(GENERIC_REJECTION_MESSAGE));
        }

        Jwt jwt = outcome.getJwt();
        return ResponseEntity.ok(WorkerTokenIntrospectResponse.builder()
                .active(true)
                .sub(jwt.getSubject())
                .aud(outcome.getAudience())
                .iss(jwt.getIssuer() != null ? jwt.getIssuer().toString() : null)
                .exp(outcome.getExp())
                .clientId(outcome.getClusterId())
                .instanceId(outcome.getInstanceId())
                .workerId(outcome.getWorkerId())
                .requestId(outcome.getRequestId())
                .functionId(outcome.getFunctionId())
                .functionVersionId(outcome.getFunctionVersionId())
                .taskId(outcome.getTaskId())
                .ncaId(outcome.getNcaId())
                .tokenType(outcome.getTokenType())
                .build());
    }

    private static String callerKey() {
        Authentication authentication = SecurityContextHolder.getContext().getAuthentication();
        return authentication != null && authentication.getName() != null
                ? authentication.getName() : "anonymous";
    }

    /** Fixed one-minute window per caller; bounded map (one entry per principal). */
    private boolean allow(String caller) {
        if (rateLimitPerMinute <= 0) {
            return true;
        }
        long minute = clock.millis() / 60_000L;
        Window w = windows.compute(caller, (k, existing) ->
                existing == null || existing.minute != minute ? new Window(minute) : existing);
        return w.count.incrementAndGet() <= rateLimitPerMinute;
    }

    private static final class Window {
        private final long minute;
        private final AtomicInteger count = new AtomicInteger();

        private Window(long minute) {
            this.minute = minute;
        }
    }
}
