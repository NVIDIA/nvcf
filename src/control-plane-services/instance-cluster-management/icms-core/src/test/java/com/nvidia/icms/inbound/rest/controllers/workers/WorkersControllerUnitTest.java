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
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.workers.WorkerTokenIntrospectRequest;
import com.nvidia.icms.inbound.rest.model.workers.WorkerTokenIntrospectResponse;
import com.nvidia.icms.service.byoc.nvca.NvcaTokenVerificationService;
import com.nvidia.icms.service.workers.WorkerTokenVerificationService;
import java.time.Instant;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.http.ResponseEntity;
import org.springframework.security.oauth2.jwt.Jwt;

@ExtendWith(MockitoExtension.class)
class WorkersControllerUnitTest {

    @Mock
    private WorkerTokenVerificationService workerTokenVerificationService;

    @Test
    void introspectWorkerToken_featureFlagDisabled_returns404() {
        WorkersController controller = controllerWithOidcEnabled(false);

        var request = tokenRequest("any.token.value");
        ResponseEntity<WorkerTokenIntrospectResponse> response =
                controller.introspectWorkerToken(request);

        assertEquals(404, response.getStatusCode().value());
        assertNull(response.getBody());
    }

    @Test
    void introspectWorkerToken_tokenTooLarge_returns431() {
        WorkersController controller = controllerWithOidcEnabled(true);
        var request = tokenRequest("tok");

        when(workerTokenVerificationService.verify("tok"))
                .thenReturn(rejectedOutcome(
                        NvcaTokenVerificationService.RejectReason.TOKEN_TOO_LARGE,
                        "JWT exceeds 2048 byte limit"));

        var response = controller.introspectWorkerToken(request);

        assertEquals(431, response.getStatusCode().value());
        var body = response.getBody();
        assertNotNull(body);
        assertFalse(body.isActive());
        assertEquals("JWT verification failed", body.getError());
    }

    @Test
    void introspectWorkerToken_unknownCluster_returns200WithActiveFalse() {
        WorkersController controller = controllerWithOidcEnabled(true);
        var request = tokenRequest("tok");

        when(workerTokenVerificationService.verify("tok"))
                .thenReturn(rejectedOutcome(
                        NvcaTokenVerificationService.RejectReason.UNKNOWN_CLUSTER,
                        "No cluster found for the provided audience"));

        var response = controller.introspectWorkerToken(request);

        assertEquals(200, response.getStatusCode().value());
        var body = response.getBody();
        assertFalse(body.isActive());
        assertEquals("JWT verification failed", body.getError());
    }

    @Test
    void introspectWorkerToken_signatureInvalid_returns200WithActiveFalse() {
        WorkersController controller = controllerWithOidcEnabled(true);
        var request = tokenRequest("bad.token.value");

        // Service may return a detailed message; controller must coarsen it.
        when(workerTokenVerificationService.verify("bad.token.value"))
                .thenReturn(rejectedOutcome(
                        NvcaTokenVerificationService.RejectReason.SIGNATURE_INVALID,
                        "worker identity not in registered set"));

        var response = controller.introspectWorkerToken(request);

        assertEquals(200, response.getStatusCode().value());
        var body = response.getBody();
        assertFalse(body.isActive());
        assertEquals("JWT verification failed", body.getError());
    }

    @Test
    void introspectWorkerToken_validPsatToken_returns200WithActiveTrue() {
        WorkersController controller = controllerWithOidcEnabled(true);
        var request = tokenRequest("valid.psat.token");
        String clusterId = "cl-abc123";
        String instanceId = "inst-789";
        String workerId = "nvcf-worker-inst-789-0";
        String aud = "nvcf-icms:" + clusterId;

        Jwt jwt = buildJwt("system:serviceaccount:nvcf-backend:nvcf-worker-inst-789",
                "https://kubernetes.default.svc", aud);

        WorkerTokenVerificationService.Outcome activeOutcome =
                WorkerTokenVerificationService.Outcome.active(
                        jwt, clusterId, instanceId, workerId,
                        WorkerTokenVerificationService.TOKEN_TYPE_PSAT, aud);

        when(workerTokenVerificationService.verify("valid.psat.token"))
                .thenReturn(activeOutcome);

        var response = controller.introspectWorkerToken(request);

        assertEquals(200, response.getStatusCode().value());
        var body = response.getBody();
        assertNotNull(body);
        assertTrue(body.isActive());
        assertEquals("system:serviceaccount:nvcf-backend:nvcf-worker-inst-789", body.getSub());
        assertEquals(aud, body.getAud());
        assertEquals("https://kubernetes.default.svc", body.getIss());
        assertEquals(clusterId, body.getClientId());
        assertEquals(instanceId, body.getInstanceId());
        assertEquals(workerId, body.getWorkerId());
        assertEquals("psat", body.getTokenType());
        assertNull(body.getError());
    }

    @Test
    void introspectWorkerToken_validSpiffeToken_returns200WithActiveTrue() {
        WorkersController controller = controllerWithOidcEnabled(true);
        var request = tokenRequest("valid.spiffe.token");
        String clusterId = "cl-abc123";
        String instanceId = "inst-789";
        String workerId = "baa15c75-0000-0000-0000-000000000001";
        String aud = "nvcf-icms:" + clusterId;
        String sub = "spiffe://domain/cluster/c1/instance/" + instanceId + "/worker/" + workerId;

        Jwt jwt = buildJwt(sub, null, aud);

        WorkerTokenVerificationService.Outcome activeOutcome =
                WorkerTokenVerificationService.Outcome.active(
                        jwt, clusterId, instanceId, workerId,
                        WorkerTokenVerificationService.TOKEN_TYPE_SPIFFE, aud);

        when(workerTokenVerificationService.verify("valid.spiffe.token"))
                .thenReturn(activeOutcome);

        var response = controller.introspectWorkerToken(request);

        assertEquals(200, response.getStatusCode().value());
        var body = response.getBody();
        assertTrue(body.isActive());
        assertEquals("spiffe", body.getTokenType());
        assertEquals(workerId, body.getWorkerId());
    }

    // --- helpers ---

    private WorkersController controllerWithOidcEnabled(boolean enabled) {
        NvcaConfigurationProperties config = new NvcaConfigurationProperties();
        config.setOidcClusterIdentityEnabled(enabled);
        return new WorkersController(config, workerTokenVerificationService);
    }

    private WorkerTokenIntrospectRequest tokenRequest(String token) {
        var req = new WorkerTokenIntrospectRequest();
        req.setToken(token);
        return req;
    }

    private static WorkerTokenVerificationService.Outcome rejectedOutcome(
            NvcaTokenVerificationService.RejectReason reason, String message) {
        return WorkerTokenVerificationService.Outcome.reject(reason, message);
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
}
