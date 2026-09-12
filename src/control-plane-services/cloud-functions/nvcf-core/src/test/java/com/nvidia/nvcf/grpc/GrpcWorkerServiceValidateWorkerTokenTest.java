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
package com.nvidia.nvcf.grpc;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.MACSigner;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvcf.configuration.AwsConfiguration.AwsProperties;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService;
import com.nvidia.nvcf.s3.MultipartUploadService;
import com.nvidia.nvcf.s3.S3PreSignedUrlGenerator;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.invocation.WorkerUrlGeneratorService;
import com.nvidia.nvcf.service.registry.RegistryArtifactService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.service.token.WorkerTokenIntrospectionService;
import com.nvidia.nvcf.service.worker.WorkerNatsService;
import com.nvidia.nvcf.service.worker.WorkerNotaryService;
import io.micrometer.tracing.Tracer;
import java.time.Instant;
import java.util.Date;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class GrpcWorkerServiceValidateWorkerTokenTest {

    private static final UUID FUNCTION_ID = UUID.randomUUID();
    private static final UUID VERSION_ID = UUID.randomUUID();
    private static final UUID OTHER_FUNCTION_ID = UUID.randomUUID();
    private static final UUID OTHER_VERSION_ID = UUID.randomUUID();
    private static final String LEGACY_TOKEN = "opaque-jwe-blob";

    @Mock private Tracer tracer;
    @Mock private GrpcTokenService grpcTokenService;
    @Mock private MultipartUploadService multipartUploadService;
    @Mock private NatsProperties natsProperties;
    @Mock private WorkerNotaryService workerNotaryService;
    @Mock private AwsProperties awsProperties;
    @Mock private S3PreSignedUrlGenerator preSignedUrlGenerator;
    @Mock private FunctionLookupService functionLookupService;
    @Mock private WorkerNatsService workerNatsService;
    @Mock private RegistryArtifactService artifactService;
    @Mock private WorkerUrlGeneratorService workerUrlGeneratorService;
    @Mock private IcmsClient icmsClient;
    @Mock private WorkerTokenIntrospectionService introspectionService;

    private GrpcWorkerService service;
    private String psat;

    @BeforeEach
    void setUp() throws Exception {
        service = new GrpcWorkerService(tracer, grpcTokenService, multipartUploadService,
                                        natsProperties, workerNotaryService, awsProperties,
                                        preSignedUrlGenerator, functionLookupService,
                                        workerNatsService, artifactService,
                                        workerUrlGeneratorService, icmsClient,
                                        introspectionService);
        var claims = new JWTClaimsSet.Builder()
                .subject("system:serviceaccount:inst-789:nvcf-worker")
                .audience(List.of("nvcf-icms:cl-abc123"))
                .expirationTime(Date.from(Instant.now().plusSeconds(900)))
                .build();
        var jwt = new SignedJWT(new JWSHeader(JWSAlgorithm.HS256), claims);
        jwt.sign(new MACSigner(new byte[32]));
        psat = jwt.serialize();
    }

    private static IcmsStubService.WorkerTokenIntrospectResult active(UUID functionId,
                                                                        UUID versionId) {
        return IcmsStubService.WorkerTokenIntrospectResult.builder()
                .active(true)
                .clientId("cl-abc123")
                .instanceId("inst-789")
                .workerId("utils")
                .requestId("sr-1")
                .functionId(functionId.toString())
                .functionVersionId(versionId.toString())
                .exp(Instant.now().plusSeconds(600).getEpochSecond())
                .tokenType("psat")
                .build();
    }

    @Test
    void delegated_activeAndBound_isAcceptedWithIcmsIdentity() {
        when(introspectionService.isEnabled()).thenReturn(true);
        when(introspectionService.introspect(psat)).thenReturn(active(FUNCTION_ID, VERSION_ID));

        var validated = service.validateWorkerToken(psat, FUNCTION_ID, VERSION_ID);

        assertThat(validated.delegated()).isTrue();
        assertThat(validated.token().functionId()).isEqualTo(FUNCTION_ID);
        assertThat(validated.token().functionVersionId()).isEqualTo(VERSION_ID);
        assertThat(validated.token().type()).isEqualTo(TokenType.WORKER);
        assertThat(validated.expiresAt()).isAfter(Instant.now());
        verify(grpcTokenService, never()).validateToken(anyString(), any());
    }

    @Test
    void delegated_boundToOtherFunction_isRejected() {
        when(introspectionService.isEnabled()).thenReturn(true);
        when(introspectionService.introspect(psat))
                .thenReturn(active(OTHER_FUNCTION_ID, OTHER_VERSION_ID));

        assertThatThrownBy(() -> service.validateWorkerToken(psat, FUNCTION_ID, VERSION_ID))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("not bound to requested workload");
    }

    @Test
    void delegated_boundToOtherVersion_isRejected() {
        when(introspectionService.isEnabled()).thenReturn(true);
        when(introspectionService.introspect(psat))
                .thenReturn(active(FUNCTION_ID, OTHER_VERSION_ID));

        assertThatThrownBy(() -> service.validateWorkerToken(psat, FUNCTION_ID, VERSION_ID))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("not bound to requested workload");
    }

    @Test
    void delegated_inactive_isRejected() {
        when(introspectionService.isEnabled()).thenReturn(true);
        when(introspectionService.introspect(psat)).thenReturn(
                IcmsStubService.WorkerTokenIntrospectResult.builder()
                        .active(false).error("JWT verification failed").build());

        assertThatThrownBy(() -> service.validateWorkerToken(psat, FUNCTION_ID, VERSION_ID))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("not active");
    }

    @Test
    void delegated_malformedBinding_isRejected() {
        when(introspectionService.isEnabled()).thenReturn(true);
        when(introspectionService.introspect(psat)).thenReturn(
                IcmsStubService.WorkerTokenIntrospectResult.builder()
                        .active(true).functionId("not-a-uuid")
                        .functionVersionId(VERSION_ID.toString())
                        .exp(Instant.now().plusSeconds(60).getEpochSecond()).build());

        assertThatThrownBy(() -> service.validateWorkerToken(psat, FUNCTION_ID, VERSION_ID))
                .isInstanceOf(ForbiddenException.class);
    }

    @Test
    void delegated_featureDisabled_isRejectedWithoutIntrospection() {
        when(introspectionService.isEnabled()).thenReturn(false);

        assertThatThrownBy(() -> service.validateWorkerToken(psat, FUNCTION_ID, VERSION_ID))
                .isInstanceOf(ForbiddenException.class);
        verify(introspectionService, never()).introspect(anyString());
        verify(grpcTokenService, never()).validateToken(anyString(), any());
    }

    @Test
    void legacy_matchingFunction_isAccepted() {
        when(grpcTokenService.validateToken(LEGACY_TOKEN, TokenType.WORKER))
                .thenReturn(new NvcfIssuedToken(FUNCTION_ID, VERSION_ID, Instant.now(),
                                                TokenType.WORKER));

        var validated = service.validateWorkerToken(LEGACY_TOKEN, FUNCTION_ID, VERSION_ID);

        assertThat(validated.delegated()).isFalse();
        assertThat(validated.token().functionId()).isEqualTo(FUNCTION_ID);
        verify(introspectionService, never()).introspect(anyString());
    }

    @Test
    void legacy_mismatchedFunction_isRejectedWithoutFallbackToIntrospection() {
        when(grpcTokenService.validateToken(LEGACY_TOKEN, TokenType.WORKER))
                .thenReturn(new NvcfIssuedToken(OTHER_FUNCTION_ID, VERSION_ID, Instant.now(),
                                                TokenType.WORKER));

        assertThatThrownBy(() -> service.validateWorkerToken(LEGACY_TOKEN, FUNCTION_ID, VERSION_ID))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("invalid worker token");
        verify(introspectionService, never()).isEnabled();
        verify(introspectionService, never()).introspect(anyString());
    }

    @Test
    void legacy_undecryptable_isRejectedWithoutFallbackToIntrospection() {
        when(grpcTokenService.validateToken(LEGACY_TOKEN, TokenType.WORKER))
                .thenThrow(new ForbiddenException("invalid token"));

        assertThatThrownBy(() -> service.validateWorkerToken(LEGACY_TOKEN, FUNCTION_ID, VERSION_ID))
                .isInstanceOf(ForbiddenException.class);
        verify(introspectionService, never()).introspect(anyString());
    }
}
