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

import static com.nvidia.nvct.util.ProtoMappingUtils.fromTimestamp;
import static com.nvidia.nvct.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_WITH_TELEMETRIES_4;
import static com.nvidia.nvct.util.TestConstants.TEST_SECRETS;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.persistence.task.entity.TelemetriesUdt;
import com.nvidia.nvct.proto.SecretCredentialsRequest;
import com.nvidia.nvct.proto.WorkerGrpc;
import com.nvidia.nvct.service.ess.EssService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.service.token.TokenService;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.stub.MetadataUtils;
import java.net.URL;
import java.time.Duration;
import java.time.Instant;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
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
class GrpcWorkerRefreshSecretAssertionsTokenTest {
    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private TokenService tokenService;

    @Autowired
    private TaskService taskService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private EssService essService;

    @Value("${nvct.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${spring.security.oauth2.client.registration.notary.client-id}")
    private String notaryClientId;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

    private ManagedChannel channel;
    private WorkerGrpc.WorkerBlockingStub workerBlockingStub;
    private WorkerGrpc.WorkerStub workerStub;

    @SneakyThrows
    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockEssServer.stop();
        MockNotaryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        log.info("{} reset", this.getClass().getSimpleName());
        channel.shutdown();
        testTaskService.clearAll();
        MockEssServer.clearSecrets();
    }

    @BeforeEach
    void setup() {
        log.info("{} setup", this.getClass().getSimpleName());
    }

    private void setupGrpc(String jwtToken) {
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwtToken);
        channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        workerBlockingStub = WorkerGrpc.newBlockingStub(channel);
        workerStub = WorkerGrpc.newStub(channel);
    }

    @Test
    void refreshSecretsAssertionTokenForTaskWithoutSecretsAndTelemetries() {
        // Create Task with no secrets.
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);

        var token = tokenService.issueWorkerAccessAssertion(TEST_NCA_ID, TEST_TASK_ID_1);
        setupGrpc(token);

        var taskId = TEST_TASK_ID_1.toString();
        var request = SecretCredentialsRequest.newBuilder().setTaskId(taskId).build();
        var response = workerBlockingStub.requestSecretCredentials(request);
        assertThat(response).isNotNull();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(response.getSecretCredentialsToken()).isBlank();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    @Test
    void refreshSecretsAssertionTokenForTaskWithSecretsAndWithoutTelemetries() {
        // Create Task with secrets.
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        essService.saveSecrets(TEST_TASK_ID_1, TEST_SECRETS);
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        var token = tokenService.issueWorkerAccessAssertion(TEST_NCA_ID, TEST_TASK_ID_1);
        setupGrpc(token);

        var taskId = TEST_TASK_ID_1.toString();
        var request = SecretCredentialsRequest.newBuilder().setTaskId(taskId).build();
        var response = workerBlockingStub.requestSecretCredentials(request);
        assertThat(response).isNotNull();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(response.getSecretCredentialsToken()).isNotBlank();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    @Test
    void refreshSecretsAssertionTokenForTaskWithoutSecretsAndWithTelemetries() {
        // Create Task with Telemetries defined in account TEST_NCA_ID_WITH_TELEMETRIES_4.
        var telemetriesUdt = TelemetriesUdt.builder()
                        .logsTelemetryId(TEST_TELEMETRY_LOGS_ID).build();
        testTaskService.createTaskWithTelemetries(TEST_NCA_ID_WITH_TELEMETRIES_4,
                                   TEST_TASK_ID_1,
                                                  TEST_ICMS_REQ_ID_1,
                                   telemetriesUdt);

        var token = tokenService.issueWorkerAccessAssertion(TEST_NCA_ID_WITH_TELEMETRIES_4,
                                                            TEST_TASK_ID_1);
        setupGrpc(token);

        var taskId = TEST_TASK_ID_1.toString();
        var request = SecretCredentialsRequest.newBuilder().setTaskId(taskId).build();
        var response = workerBlockingStub.requestSecretCredentials(request);
        assertThat(response).isNotNull();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(response.getSecretCredentialsToken()).isNotBlank();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    @Test
    void refreshSecretsAssertionTokenForTaskWithSecretsAndWithTelemetries() {
        // Create Task with secrets amd Telemetries defined in account TEST_NCA_ID_WITH_TELEMETRIES_4.
        var telemetriesUdt = TelemetriesUdt.builder()
                .logsTelemetryId(TEST_TELEMETRY_LOGS_ID).build();
        testTaskService.createTaskWithTelemetries(TEST_NCA_ID_WITH_TELEMETRIES_4,
                                                  TEST_TASK_ID_1,
                                                  TEST_ICMS_REQ_ID_1,
                                                  telemetriesUdt);
        essService.saveSecrets(TEST_TASK_ID_1, TEST_SECRETS);
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        var token = tokenService.issueWorkerAccessAssertion(TEST_NCA_ID_WITH_TELEMETRIES_4,
                                                            TEST_TASK_ID_1);
        setupGrpc(token);

        var taskId = TEST_TASK_ID_1.toString();
        var request = SecretCredentialsRequest.newBuilder().setTaskId(taskId).build();
        var response = workerBlockingStub.requestSecretCredentials(request);
        assertThat(response).isNotNull();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(response.getSecretCredentialsToken()).isNotBlank();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

}
