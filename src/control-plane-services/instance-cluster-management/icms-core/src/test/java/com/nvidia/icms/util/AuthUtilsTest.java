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
package com.nvidia.icms.util;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.Instant;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.springframework.security.authentication.TestingAuthenticationToken;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;

class AuthUtilsTest {

    @AfterEach
    void tearDown() {
        SecurityContextHolder.clearContext();
    }

    @Test
    void getSubOrClusterIdFromSecurityContext_emptyAudienceSuffixFallsBackToSubject() {
        Jwt jwt = jwtWithSubjectAndAudience("legacy-subject", List.of("nvcf-icms:"));
        SecurityContextHolder.getContext().setAuthentication(new JwtAuthenticationToken(jwt));

        assertEquals("legacy-subject", AuthUtils.getSubOrClusterIdFromSecurityContext());
    }

    @Test
    void getSubOrClusterIdFromSecurityContext_blankAudienceSuffixFallsBackToSubject() {
        Jwt jwt = jwtWithSubjectAndAudience("legacy-subject", List.of("nvcf-icms:   "));
        SecurityContextHolder.getContext().setAuthentication(new JwtAuthenticationToken(jwt));

        assertEquals("legacy-subject", AuthUtils.getSubOrClusterIdFromSecurityContext());
    }

    @Test
    void getSubOrClusterIdFromSecurityContext_multipleDifferentClusterAudiencesFallsBackToSubject() {
        Jwt jwt = jwtWithSubjectAndAudience(
                "legacy-subject",
                List.of("nvcf-icms:cluster-a", "nvcf-icms:cluster-b"));
        SecurityContextHolder.getContext().setAuthentication(new JwtAuthenticationToken(jwt));

        assertEquals("legacy-subject", AuthUtils.getSubOrClusterIdFromSecurityContext());
    }

    @Test
    void getSubOrClusterIdFromSecurityContext_repeatedSameClusterAudienceReturnsClusterId() {
        Jwt jwt = jwtWithSubjectAndAudience(
                "legacy-subject",
                List.of("nvcf-icms:cluster-a", "other", "nvcf-icms:cluster-a"));
        SecurityContextHolder.getContext().setAuthentication(new JwtAuthenticationToken(jwt));

        assertEquals("cluster-a", AuthUtils.getSubOrClusterIdFromSecurityContext());
    }

    private static Jwt jwtWithSubjectAndAudience(String subject, List<String> audience) {
        Map<String, Object> headers = Map.of("alg", "RS256");
        Map<String, Object> claims = Map.of(
                "sub", subject,
                "aud", audience);
        return new Jwt("token", Instant.now(), Instant.now().plusSeconds(60), headers, claims);
    }

    @Test
    void workerSubjectNamespace_acceptsOnlyFixedWorkerServiceAccount() {
        assertEquals("inst-789", AuthUtils.workerSubjectNamespace("system:serviceaccount:inst-789:nvcf-worker"));
        assertTrue(AuthUtils.isValidWorkerPsatSubject("system:serviceaccount:ns:nvcf-worker"));
        assertNull(AuthUtils.workerSubjectNamespace("system:serviceaccount:ns:nvcf-worker-inst-1"));
        assertNull(AuthUtils.workerSubjectNamespace("system:serviceaccount:ns:nvca"));
        assertNull(AuthUtils.workerSubjectNamespace("system:serviceaccount::nvcf-worker"));
        assertNull(AuthUtils.workerSubjectNamespace("system:serviceaccount:a:b:nvcf-worker"));
        assertNull(AuthUtils.workerSubjectNamespace("spiffe://x/worker/y"));
        assertFalse(AuthUtils.isValidWorkerPsatSubject(null));
    }

    @Test
    void isClusterPrincipal_trueOnlyForClusterAuthorities() {
        assertFalse(AuthUtils.isClusterPrincipal());

        var svc = new TestingAuthenticationToken("nvcf-api", "n/a", "worker-token-introspect");
        SecurityContextHolder.getContext().setAuthentication(svc);
        assertFalse(AuthUtils.isClusterPrincipal());

        var psat = new TestingAuthenticationToken("cluster", "n/a", "nvca-cluster", "instance_request_update");
        SecurityContextHolder.getContext().setAuthentication(psat);
        assertTrue(AuthUtils.isClusterPrincipal());

        var apiKey = new TestingAuthenticationToken("cluster", "n/a", "apikey:nvca-cluster");
        SecurityContextHolder.getContext().setAuthentication(apiKey);
        assertTrue(AuthUtils.isClusterPrincipal());
    }
}
