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
import static com.nvidia.nvcf.util.MockIcmsServer.InstancesHealthState.HEALTHY;
import static com.nvidia.nvcf.util.MockIcmsServer.InstancesHealthState.UNHEALTHY;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.nvcf.proto.SkywayAuthRequest;
import com.nvidia.nvcf.proto.SkywayAuthResponse;
import com.nvidia.nvcf.proto.SkywayGrpc;
import com.nvidia.nvcf.rest.function.invocation.BaseFunctionInvocationTest;
import com.nvidia.nvcf.util.MockIcmsServer;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import java.util.List;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

@Slf4j
class GrpcSkywayServiceTest extends BaseFunctionInvocationTest {
    private static final String SKYWAY_AUTH_SCOPE = "skyway:auth";

    @BeforeAll
    void setupMocks() {
        var mockIcmsServerHealthContexts = List.of(
                MockIcmsServer.IcmsRequestHealthContext.builder()
                        .gpu(MockIcmsServer.TestGpu.GFN_L40G)
                        .instanceHealthState(HEALTHY).build(),
                MockIcmsServer.IcmsRequestHealthContext.builder()
                        .gpu(MockIcmsServer.TestGpu.GFN_L40G)
                        .instanceHealthState(UNHEALTHY).build());
        MockIcmsServer.start(9096, jsonMapper, mockIcmsServerHealthContexts);
    }

    @AfterAll
    void cleanupMocks() {
        MockIcmsServer.stop();
    }

    @Test
    void authGetLogs() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_LIST_FUNCTIONS), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var authResponse = proxyBlockingStub.authGetLogs(proxyAuthRequest);
        validateSuccessResponse(authResponse);
        channel.shutdownNow();
    }

    @Test
    void authGetLogsInvalidClientSecretScope() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_DELETE_FUNCTION), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authGetLogs(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void authGetLogsInvalidClientSecret() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = "invalid_secret";
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authGetLogs(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }

    @Test
    void authExecuteCommand() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                         100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var authResponse = proxyBlockingStub.authExecuteCommand(proxyAuthRequest);
        validateSuccessResponse(authResponse);
        channel.shutdownNow();
    }

    @Test
    void authExecuteCommandInvalidClientSecretScope() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DELETE_FUNCTION),
                                                         100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authExecuteCommand(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void authExecuteCommandInvalidClientSecret() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = "invalid_secret";
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authExecuteCommand(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }

    @Test
    void authListInstances() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_FUNCTIONS),
                                                         100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var authResponse = proxyBlockingStub.authListInstances(proxyAuthRequest);
        validateSuccessResponse(authResponse);
        channel.shutdownNow();
    }

    @Test
    void authListInstancesInvalidAccountAccess() {
        setFunctionActive(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_FUNCTIONS),
                                                         100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID_2.toString())
                .setFunctionVersionId(TEST_VERSION_ID_2.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void authListInstancesInvalidClientSecretScope() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DELETE_FUNCTION),
                                                         100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void authListInstancesInvalidClientSecret() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = "invalid_secret";
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }

    @Test
    void invalidServiceSecretScopes() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SCOPE_DELETE_FUNCTION);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_FUNCTIONS),
                                                         100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void invalidServiceSecret() {
        setFunctionActive(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var md = new Metadata();
        var jwt = "invalid_secret";
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_FUNCTIONS),
                                                         100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }

    @Test
    void inactiveFunction() {
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_LIST_FUNCTIONS), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(TEST_FUNCTION_ID.toString())
                .setFunctionVersionId(TEST_VERSION_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authExecuteCommand(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void nonExistingFunction() {
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_LIST_FUNCTIONS), 100);
        var notExistingFunctionId = UUID.randomUUID().toString();
        var notExistingFunctionVersionId = UUID.randomUUID().toString();
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setFunctionId(notExistingFunctionId)
                .setFunctionVersionId(notExistingFunctionVersionId)
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authExecuteCommand(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    private static void validateSuccessResponse(SkywayAuthResponse authResponse) {
        assertThat(authResponse).isNotNull();
        assertThat(authResponse.getFunctionId()).isEqualTo(TEST_FUNCTION_ID.toString());
        assertThat(authResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(authResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(authResponse.getFunctionVersionId()).isEqualTo(TEST_VERSION_ID_1.toString());
        assertThat(authResponse.getBackend()).isEmpty(); // For backward compatible only
        assertThat(authResponse.getInstancesList()).hasSize(2);
        authResponse.getInstancesList().forEach(instance -> {
            assertThat(instance.getInstanceId()).isNotEmpty();
            // Hardcoded in MockIcmsServer's workload instance response transformer.
            assertThat(instance.getLocation()).isEqualTo("NP-LAX-03");
            assertThat(instance.getActive()).isTrue();
        });
    }
}
