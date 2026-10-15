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

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.nvcf.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.persistence.function.entity.RateLimitUdt;
import com.nvidia.nvcf.proto.ProxyAuthRequest;
import com.nvidia.nvcf.proto.ProxyAuthResponse.FunctionVersion;
import com.nvidia.nvcf.proto.ProxyAuthResponse.FunctionVersion.BackendType;
import com.nvidia.nvcf.proto.ProxyAuthResponse.FunctionVersion.FunctionType;
import com.nvidia.nvcf.proto.ProxyGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.service.token.client.NotaryService.InvocationAssertion;
import com.nvidia.nvcf.util.MockNotaryServer;
import com.nvidia.nvcf.util.NotaryTokenUtils;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import java.net.URL;
import java.time.Instant;
import java.util.Date;
import java.util.List;
import java.util.Set;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;

@Slf4j
class GrpcProxyServiceTest extends BaseFunctionInvocationTest {

    @Autowired
    private FixedNatsPool fixedNatsPool;

    @Autowired
    private NatsProperties natsProperties;

    @Value("${nvcf.notary.base-url}")
    private URL notaryBaseUrl;

    @Value("${nvcf.notary.audiences.nvcf}")
    private String nvcfAudience;

    @BeforeAll
    void setupMocks() {
        MockNotaryServer.start(notaryBaseUrl.toString(), nvcfAudience, nvcfAudience);
    }

    @AfterAll
    void cleanupMocks() {
        MockNotaryServer.stop();
    }

    @Test
    void authStatefulWork() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("proxy:invoke_function");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = ProxyGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var proxyAuthRequest = ProxyAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var clientInvokeResponse = proxyBlockingStub.authStatefulWork(proxyAuthRequest);
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setType(FunctionType.DEFAULT)
                                         .setHasRateLimit(false)
                                         .setSyncCheck(false)
                                         .build());
        channel.shutdownNow();
    }

    @Test
    @SneakyThrows
    void authStatefulWorkWithNotaryTokenUsesAssertionClientIdAsSubject() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("proxy:invoke_function");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = ProxyGrpc.newBlockingStub(channel);
        var assertion = new InvocationAssertion(TEST_NCA_ID,
                                                TEST_FUNCTION_ID,
                                                TEST_VERSION_ID_1,
                                                null,
                                                "test_client_id");
        var clientAuth = NotaryTokenUtils.getJwt(TEST_CLIENT_SUBJECT,
                                                 jsonMapper.writeValueAsString(assertion),
                                                 notaryBaseUrl,
                                                 nvcfAudience,
                                                 Date.from(Instant.now()));
        var proxyAuthRequest = ProxyAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var clientInvokeResponse = proxyBlockingStub.authStatefulWork(proxyAuthRequest);
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(assertion.clientId());
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setType(FunctionType.DEFAULT)
                                         .setHasRateLimit(false)
                                         .setSyncCheck(false)
                                         .build());
        channel.shutdownNow();
    }

    @Test
    void authStatefulWorkWithRateLimit() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        setFunctionRateLimit(TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             RateLimitUdt.builder()
                                     .rate("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .syncCheck(true)
                                     .build());
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("proxy:invoke_function");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = ProxyGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var proxyAuthRequest = ProxyAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var clientInvokeResponse = proxyBlockingStub.authStatefulWork(proxyAuthRequest);
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setType(FunctionType.DEFAULT)
                                         .setHasRateLimit(true)
                                         .setSyncCheck(true)
                                         .build());
        channel.shutdownNow();
    }

    @Test
    void authStatefulWorkWithEmptyRateLimit() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        setFunctionRateLimit(TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             RateLimitUdt.builder().build());
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("proxy:invoke_function");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = ProxyGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var proxyAuthRequest = ProxyAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var clientInvokeResponse = proxyBlockingStub.authStatefulWork(proxyAuthRequest);
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setType(FunctionType.DEFAULT)
                                         .setHasRateLimit(false)
                                         .setSyncCheck(false)
                                         .build());
        channel.shutdownNow();
    }

    @Test
    void authStatefulWorkStreaming() {
        var function = setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        function.setFunctionType(
                com.nvidia.nvcf.persistence.function.entity.FunctionType.STREAMING);
        functionsRepository.insert(function);

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("proxy:invoke_function");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = ProxyGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var proxyAuthRequest = ProxyAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var clientInvokeResponse = proxyBlockingStub.authStatefulWork(proxyAuthRequest);
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setType(FunctionType.STREAMING)
                                         .setBackendType(BackendType.GFN)
                                         .setHasRateLimit(false)
                                         .setSyncCheck(false)
                                         .build());
        channel.shutdownNow();
    }

    @Test
    void authStatefulWorkLlm() {
        var function = setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        function.setFunctionType(
                com.nvidia.nvcf.persistence.function.entity.FunctionType.LLM);
        functionsRepository.insert(function);

        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("proxy:invoke_function");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = ProxyGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var proxyAuthRequest = ProxyAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var clientInvokeResponse = proxyBlockingStub.authStatefulWork(proxyAuthRequest);
        assertThat(clientInvokeResponse).isNotNull();
        assertThat(clientInvokeResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(clientInvokeResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(clientInvokeResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(clientInvokeResponse.getFunctionVersionsList())
                .containsExactly(FunctionVersion.newBuilder()
                                         .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                                         .setType(FunctionType.LLM)
                                         .setHasRateLimit(false)
                                         .setSyncCheck(false)
                                         .build());
        channel.shutdownNow();
    }

    @Test
    void badClientCredentials() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("proxy:invoke_function");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = ProxyGrpc.newBlockingStub(channel);
        var proxyAuthRequest = ProxyAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken("abc123")
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authStatefulWork(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }

    @Test
    void badServiceCredentials() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer abc123");
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = ProxyGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_INVOKE_FUNCTION), 100);
        var proxyAuthRequest = ProxyAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authStatefulWork(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }
}