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
package com.nvidia.nvcf.service.token;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.MACSigner;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.PlainJWT;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService;
import java.time.Instant;
import java.util.Date;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class WorkerTokenIntrospectionServiceTest {

    private static final String FUNCTION_ID = "b28e2fd6-6d35-4f38-a0a7-a2ddae81f310";
    private static final String VERSION_ID = "f2619229-35a7-4361-acca-2c4f1f4d609f";
    private static final byte[] HMAC_KEY = new byte[32];

    @Mock
    private IcmsClient icmsClient;

    private WorkerTokenIntrospectionService service;
    private WorkerTokenIntrospectionService disabledService;

    @BeforeEach
    void setUp() {
        service = new WorkerTokenIntrospectionService(icmsClient, true);
        disabledService = new WorkerTokenIntrospectionService(icmsClient, false);
    }

    static String signedJwt(List<String> audience) throws Exception {
        var claims = new JWTClaimsSet.Builder()
                .subject("system:serviceaccount:inst-789:nvcf-worker")
                .audience(audience)
                .expirationTime(Date.from(Instant.now().plusSeconds(900)))
                .build();
        var jwt = new SignedJWT(new JWSHeader(JWSAlgorithm.HS256), claims);
        jwt.sign(new MACSigner(HMAC_KEY));
        return jwt.serialize();
    }

    private static IcmsStubService.WorkerTokenIntrospectResult.WorkerTokenIntrospectResultBuilder
    boundActive() {
        return IcmsStubService.WorkerTokenIntrospectResult.builder()
                .active(true)
                .instanceId("inst-1")
                .workerId("utils")
                .tokenType("psat")
                .clientId("cl-1")
                .requestId("sr-1")
                .functionId(FUNCTION_ID)
                .functionVersionId(VERSION_ID)
                .exp(Instant.now().plusSeconds(900).getEpochSecond());
    }

    @Test
    void isEnabled_returnsTrue_whenFlagSet() {
        assertThat(service.isEnabled()).isTrue();
    }

    @Test
    void isEnabled_returnsFalse_whenFlagNotSet() {
        assertThat(disabledService.isEnabled()).isFalse();
    }

    @Test
    void isDelegatedToken_true_forSignedJwtWithIcmsAudience() throws Exception {
        assertThat(WorkerTokenIntrospectionService.isDelegatedToken(
                signedJwt(List.of("nvcf-icms:cl-abc123")))).isTrue();
    }

    @Test
    void isDelegatedToken_false_forSignedJwtWithoutIcmsAudience() throws Exception {
        assertThat(WorkerTokenIntrospectionService.isDelegatedToken(
                signedJwt(List.of("https://kubernetes.default.svc")))).isFalse();
    }

    @Test
    void isDelegatedToken_false_forUnsignedJwt() throws Exception {
        var plain = new PlainJWT(new JWTClaimsSet.Builder()
                                         .audience("nvcf-icms:cl-abc123").build());
        assertThat(WorkerTokenIntrospectionService.isDelegatedToken(plain.serialize())).isFalse();
    }

    @Test
    void isDelegatedToken_false_forOpaqueOrBlankTokens() {
        assertThat(WorkerTokenIntrospectionService.isDelegatedToken("not-a-jwt")).isFalse();
        assertThat(WorkerTokenIntrospectionService.isDelegatedToken("a.b.c.d.e")).isFalse();
        assertThat(WorkerTokenIntrospectionService.isDelegatedToken("")).isFalse();
        assertThat(WorkerTokenIntrospectionService.isDelegatedToken(null)).isFalse();
    }

    @Test
    void introspect_returnsActiveResult_andCaches() {
        when(icmsClient.introspectWorkerToken(any())).thenReturn(boundActive().build());

        var result1 = service.introspect("token.val.ue");
        var result2 = service.introspect("token.val.ue");

        assertThat(result1.isActive()).isTrue();
        assertThat(result2.isActive()).isTrue();
        assertThat(result2.getFunctionId()).isEqualTo(FUNCTION_ID);
        verify(icmsClient, times(1)).introspectWorkerToken(any());
    }

    @Test
    void introspect_doesNotCache_whenInactive() {
        var inactiveResult = IcmsStubService.WorkerTokenIntrospectResult.builder()
                .active(false)
                .error("JWT verification failed")
                .build();
        when(icmsClient.introspectWorkerToken(any())).thenReturn(inactiveResult);

        service.introspect("bad.token.here");
        service.introspect("bad.token.here");

        verify(icmsClient, times(2)).introspectWorkerToken(any());
    }

    @Test
    void introspect_activeWithoutExp_becomesInactive_andIsNotCached() {
        when(icmsClient.introspectWorkerToken(any())).thenReturn(boundActive().exp(null).build());

        var first = service.introspect("token.no.exp");
        var second = service.introspect("token.no.exp");

        assertThat(first.isActive()).isFalse();
        assertThat(first.getError())
                .isEqualTo(WorkerTokenIntrospectionService.ERROR_MISSING_BINDING);
        assertThat(second.isActive()).isFalse();
        verify(icmsClient, times(2)).introspectWorkerToken(any());
    }

    @Test
    void introspect_activeWithoutFunctionBinding_becomesInactive_andIsNotCached() {
        when(icmsClient.introspectWorkerToken(any()))
                .thenReturn(boundActive().functionId(null).build())
                .thenReturn(boundActive().functionVersionId("").build());

        assertThat(service.introspect("token.no.fn").isActive()).isFalse();
        assertThat(service.introspect("token.no.fn").isActive()).isFalse();
        verify(icmsClient, times(2)).introspectWorkerToken(any());
    }

    @Test
    void introspect_activeAlreadyExpired_isNotServedFromCache() {
        when(icmsClient.introspectWorkerToken(any()))
                .thenReturn(boundActive().exp(Instant.now().minusSeconds(5).getEpochSecond())
                                    .build());

        service.introspect("token.expired");
        service.introspect("token.expired");

        // A zero remaining lifetime yields a zero cache TTL, so the second call reaches ICMS.
        verify(icmsClient, times(2)).introspectWorkerToken(any());
    }

    @Test
    void cacheTtl_isCappedAtFifteenMinutes() {
        assertThat(WorkerTokenIntrospectionService.CACHE_TTL.toMinutes()).isEqualTo(15);
    }

    @Test
    void introspect_differentTokens_callIcmsSeparately() {
        when(icmsClient.introspectWorkerToken(any())).thenReturn(boundActive().build());

        service.introspect("token.a.1");
        service.introspect("token.b.2");

        verify(icmsClient, times(2)).introspectWorkerToken(any());
    }

    @Test
    void introspect_passesTokenToIcms() {
        when(icmsClient.introspectWorkerToken(any()))
                .thenReturn(IcmsStubService.WorkerTokenIntrospectResult.builder()
                                    .active(false).build());

        service.introspect("my.raw.token");

        verify(icmsClient).introspectWorkerToken(
                IcmsStubService.WorkerTokenIntrospectRequest.builder().token("my.raw.token").build());
    }
}
