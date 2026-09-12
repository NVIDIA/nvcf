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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.workers.WorkerTokenIntrospectRequest;
import com.nvidia.icms.inbound.rest.model.workers.WorkerTokenIntrospectResponse;
import com.nvidia.icms.service.byoc.nvca.ClusterOIDCTokenVerificationService;
import com.nvidia.icms.service.workers.WorkerTokenVerificationService;
import java.lang.reflect.Method;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.List;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.authentication.TestingAuthenticationToken;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.jwt.Jwt;

@ExtendWith(MockitoExtension.class)
class WorkersControllerUnitTest {

    private static final String CLUSTER_ID = "cl-abc123";
    private static final String INSTANCE_ID = "inst-789";
    private static final String AUD = "nvcf-icms:" + CLUSTER_ID;
    private static final String SUB = "system:serviceaccount:inst-789:nvcf-worker";

    @Mock
    private WorkerTokenVerificationService workerTokenVerificationService;

    @AfterEach
    void clearSecurityContext() {
        SecurityContextHolder.clearContext();
    }

    @Test
    void introspectEndpoint_requiresIntrospectAuthority() throws Exception {
        Method m = WorkersController.class.getMethod("introspectWorkerToken",
                WorkerTokenIntrospectRequest.class);
        PreAuthorize pre = m.getAnnotation(PreAuthorize.class);

        assertNotNull(pre, "introspect endpoint must be guarded by @PreAuthorize");
        assertTrue(pre.value().contains(WorkersController.INTROSPECT_AUTHORITY));
    }

    @Test
    void introspectWorkerToken_featureFlagDisabled_returns200WithActiveFalse() {
        WorkersController controller = controller(false, 600);

        ResponseEntity<WorkerTokenIntrospectResponse> response =
                controller.introspectWorkerToken(tokenRequest("any.token.value"));

        assertEquals(200, response.getStatusCode().value());
        assertNotNull(response.getBody());
        assertFalse(response.getBody().isActive());
        assertEquals("JWT verification failed", response.getBody().getError());
        verify(workerTokenVerificationService, never()).verify(any());
    }

    @Test
    void introspectWorkerToken_rejected_returns200WithGenericError() {
        WorkersController controller = controller(true, 600);
        when(workerTokenVerificationService.verify("bad.token.value"))
                .thenReturn(WorkerTokenVerificationService.Outcome.reject(
                        ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                        "worker identity not in registered set"));

        var response = controller.introspectWorkerToken(tokenRequest("bad.token.value"));

        assertEquals(200, response.getStatusCode().value());
        var body = response.getBody();
        assertNotNull(body);
        assertFalse(body.isActive());
        assertEquals("JWT verification failed", body.getError());
        assertNull(body.getFunctionId());
    }

    @Test
    void introspectWorkerToken_activePsat_returnsIdentityAndBinding() {
        WorkersController controller = controller(true, 600);
        Jwt jwt = buildJwt(SUB, "https://kubernetes.default.svc", AUD);
        when(workerTokenVerificationService.verify("valid.psat.token"))
                .thenReturn(WorkerTokenVerificationService.Outcome.builder()
                        .jwt(jwt)
                        .clusterId(CLUSTER_ID)
                        .instanceId(INSTANCE_ID)
                        .workerId("utils")
                        .tokenType(WorkerTokenVerificationService.TOKEN_TYPE_PSAT)
                        .audience(AUD)
                        .exp(jwt.getExpiresAt().getEpochSecond())
                        .requestId("sr-1")
                        .functionId("f-1")
                        .functionVersionId("v-1")
                        .ncaId("nca-1")
                        .build());

        var response = controller.introspectWorkerToken(tokenRequest("valid.psat.token"));

        assertEquals(200, response.getStatusCode().value());
        var body = response.getBody();
        assertNotNull(body);
        assertTrue(body.isActive());
        assertEquals(SUB, body.getSub());
        assertEquals(AUD, body.getAud());
        assertEquals("https://kubernetes.default.svc", body.getIss());
        assertEquals(jwt.getExpiresAt().getEpochSecond(), body.getExp());
        assertEquals(CLUSTER_ID, body.getClientId());
        assertEquals(INSTANCE_ID, body.getInstanceId());
        assertEquals("utils", body.getWorkerId());
        assertEquals("sr-1", body.getRequestId());
        assertEquals("f-1", body.getFunctionId());
        assertEquals("v-1", body.getFunctionVersionId());
        assertNull(body.getTaskId());
        assertEquals("nca-1", body.getNcaId());
        assertEquals("psat", body.getTokenType());
        assertNull(body.getError());
    }

    @Test
    void introspectWorkerToken_activeSpiffeTask_returnsTaskBinding() {
        WorkersController controller = controller(true, 600);
        String sub = "spiffe://domain/cluster/" + CLUSTER_ID + "/instance/" + INSTANCE_ID + "/worker/w1";
        Jwt jwt = buildJwt(sub, null, AUD);
        when(workerTokenVerificationService.verify("valid.spiffe.token"))
                .thenReturn(WorkerTokenVerificationService.Outcome.builder()
                        .jwt(jwt).clusterId(CLUSTER_ID).instanceId(INSTANCE_ID).workerId("w1")
                        .tokenType(WorkerTokenVerificationService.TOKEN_TYPE_SPIFFE).audience(AUD)
                        .exp(jwt.getExpiresAt().getEpochSecond())
                        .requestId("sr-2").taskId("t-1").ncaId("nca-1")
                        .build());

        var body = controller.introspectWorkerToken(tokenRequest("valid.spiffe.token")).getBody();

        assertNotNull(body);
        assertTrue(body.isActive());
        assertEquals("spiffe", body.getTokenType());
        assertEquals("t-1", body.getTaskId());
        assertNull(body.getFunctionId());
    }

    @Test
    void introspectWorkerToken_overRateLimit_returns429ForThatCallerOnly() {
        WorkersController controller = controller(true, 2);
        when(workerTokenVerificationService.verify(any()))
                .thenReturn(WorkerTokenVerificationService.Outcome.reject(
                        ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID, "x"));

        authenticateAs("nvcf-api");
        assertEquals(200, controller.introspectWorkerToken(tokenRequest("t1")).getStatusCode().value());
        assertEquals(200, controller.introspectWorkerToken(tokenRequest("t2")).getStatusCode().value());
        assertEquals(429, controller.introspectWorkerToken(tokenRequest("t3")).getStatusCode().value());

        authenticateAs("nvct-api");
        assertEquals(200, controller.introspectWorkerToken(tokenRequest("t4")).getStatusCode().value());
    }

    @Test
    void introspectWorkerToken_rateLimitWindowResets() {
        MutableClock clock = new MutableClock(Instant.parse("2026-09-04T00:00:00Z"));
        WorkersController controller = controller(true, 1, clock);
        when(workerTokenVerificationService.verify(any()))
                .thenReturn(WorkerTokenVerificationService.Outcome.reject(
                        ClusterOIDCTokenVerificationService.RejectReason.SIGNATURE_INVALID, "x"));
        authenticateAs("nvcf-api");

        assertEquals(200, controller.introspectWorkerToken(tokenRequest("t1")).getStatusCode().value());
        assertEquals(429, controller.introspectWorkerToken(tokenRequest("t2")).getStatusCode().value());
        clock.advanceSeconds(61);
        assertEquals(200, controller.introspectWorkerToken(tokenRequest("t3")).getStatusCode().value());
    }

    // --- helpers ---

    private WorkersController controller(boolean oidcEnabled, int rateLimitPerMinute) {
        return controller(oidcEnabled, rateLimitPerMinute, Clock.systemUTC());
    }

    private WorkersController controller(boolean oidcEnabled, int rateLimitPerMinute, Clock clock) {
        NvcaConfigurationProperties config = new NvcaConfigurationProperties();
        config.setOidcClusterIdentityEnabled(oidcEnabled);
        return new WorkersController(config, workerTokenVerificationService, rateLimitPerMinute, clock);
    }

    private static void authenticateAs(String name) {
        var auth = new TestingAuthenticationToken(name, "n/a", WorkersController.INTROSPECT_AUTHORITY);
        auth.setAuthenticated(true);
        SecurityContextHolder.getContext().setAuthentication(auth);
    }

    private static WorkerTokenIntrospectRequest tokenRequest(String token) {
        var req = new WorkerTokenIntrospectRequest();
        req.setToken(token);
        return req;
    }

    private static Jwt buildJwt(String sub, String iss, String aud) {
        return Jwt.withTokenValue("fake.jwt.token")
                .header("alg", "RS256")
                .subject(sub)
                .issuer(iss != null ? iss : "https://example.com")
                .audience(List.of(aud))
                .issuedAt(Instant.now())
                .expiresAt(Instant.now().plusSeconds(900))
                .build();
    }

    private static final class MutableClock extends Clock {
        private Instant now;

        private MutableClock(Instant now) {
            this.now = now;
        }

        void advanceSeconds(long seconds) {
            now = now.plusSeconds(seconds);
        }

        @Override
        public java.time.ZoneId getZone() {
            return ZoneOffset.UTC;
        }

        @Override
        public Clock withZone(java.time.ZoneId zone) {
            return this;
        }

        @Override
        public Instant instant() {
            return now;
        }
    }
}
