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
package com.nvidia.nvct.grpc;

import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.util.MockIcmsServer.InstancesState.HEALTHY;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_DELETE_TASK;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LAUNCH_TASK;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.proto.SkywayAuthRequest;
import com.nvidia.nvct.proto.SkywayAuthResponse;
import com.nvidia.nvct.proto.SkywayGrpc;
import com.nvidia.nvct.rest.task.dto.InstanceStateEnum;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import java.net.URL;
import java.util.List;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(classes = {NvctTestApp.class,
        IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "grpc.server.port=9090"
        })
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class GrpcSkywayServiceTest {
    private static final String SKYWAY_AUTH_SCOPE = "skyway:auth";

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void setupMocks() {
        var mockIcmsServerHealthContexts = List.of(MockIcmsServer.IcmsRequestContext.builder()
                                                          .instanceState(HEALTHY).build());
        MockIcmsServer.start(icmsBaseUrl, jsonMapper, mockIcmsServerHealthContexts);
        MockNvcfServer.start(nvcfBaseUrl);
    }

    @AfterAll
    void cleanupMocks() {
        MockIcmsServer.stop();
        MockNvcfServer.stop();
    }

    @AfterEach
    void reset() {
        testTaskService.clearAll();
    }

    @Test
    void authGetLogs() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_LIST_TASKS), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var authResponse = proxyBlockingStub.authGetLogs(proxyAuthRequest);
        validateSuccessResponse(authResponse);
        channel.shutdownNow();
    }

    @Test
    void authGetLogsInvalidClientSecretScope() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_DELETE_TASK), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authGetLogs(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void authGetLogsInvalidClientSecret() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = "invalid_secret";
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authGetLogs(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }

    @Test
    void authExecuteCommand() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth =
                MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LAUNCH_TASK), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var authResponse = proxyBlockingStub.authExecuteCommand(proxyAuthRequest);
        validateSuccessResponse(authResponse);
        channel.shutdownNow();
    }

    @Test
    void authExecuteCommandInvalidClientSecretScope() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth =
                MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DELETE_TASK), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authExecuteCommand(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void authExecuteCommandInvalidClientSecret() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = "invalid_secret";
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authExecuteCommand(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }

    @Test
    void authListInstances() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth =
                MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_TASKS), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        var authResponse = proxyBlockingStub.authListInstances(proxyAuthRequest);
        validateSuccessResponse(authResponse);
        channel.shutdownNow();
    }

    @Test
    void authListInstancesInvalidAccountAccess() {
        testTaskService.createTask(TEST_NCA_ID_2, TEST_TASK_ID_2, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth =
                MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_TASKS), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void authListInstancesInvalidClientSecretScope() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth =
                MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DELETE_TASK), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void authListInstancesInvalidClientSecret() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SKYWAY_AUTH_SCOPE);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = "invalid_secret";
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("UNAUTHENTICATED");
        channel.shutdownNow();
    }

    @Test
    void invalidServiceSecretScopes() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt(SCOPE_DELETE_TASK);
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth =
                MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_TASKS), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authListInstances(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    @Test
    void invalidServiceSecret() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1,
                                   TaskStatus.RUNNING);
        var md = new Metadata();
        var jwt = "invalid_secret";
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth =
                MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_LIST_TASKS), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
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
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_LIST_TASKS), 100);
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
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
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var proxyBlockingStub = SkywayGrpc.newBlockingStub(channel);
        var clientAuth = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                         List.of(SCOPE_LIST_TASKS), 100);
        var notExistingTaskId = UUID.randomUUID().toString();
        var proxyAuthRequest = SkywayAuthRequest.newBuilder()
                .setTaskId(notExistingTaskId)
                .setClientAuthorizationToken(clientAuth)
                .build();
        assertThatThrownBy(() -> proxyBlockingStub.authExecuteCommand(proxyAuthRequest))
                .isInstanceOf(StatusRuntimeException.class)
                .hasMessageContaining("PERMISSION_DENIED");
        channel.shutdownNow();
    }

    private static void validateSuccessResponse(SkywayAuthResponse authResponse) {
        assertThat(authResponse).isNotNull();
        assertThat(authResponse.getTaskId()).isEqualTo(TEST_TASK_ID_1.toString());
        assertThat(authResponse.getClientAuthSubject()).isEqualTo(TEST_CLIENT_SUBJECT);
        assertThat(authResponse.getClientNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(authResponse.getBackend()).isEmpty(); // For backward compatible only
        assertThat(authResponse.getInstancesList()).hasSize(1);
        authResponse.getInstancesList().forEach(instance -> {
            assertThat(instance.getInstanceId()).isNotEmpty();
            // Hardcoded in `src/test/resources/fixtures/icms/raw-healthy-instance.json`
            assertThat(instance.getLocation()).isEqualTo("NP-LAX-03");
            assertThat(instance.getState()).isEqualTo(InstanceStateEnum.RUNNING.toString());
        });
    }
}
