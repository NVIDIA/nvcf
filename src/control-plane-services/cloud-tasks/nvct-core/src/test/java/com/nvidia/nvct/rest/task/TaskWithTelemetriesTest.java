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
package com.nvidia.nvct.rest.task;

import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LAUNCH_TASK;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_TASK_DETAILS;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_4;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ENVIRONMENT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_WITH_TELEMETRIES_4;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;
import static com.nvidia.nvct.util.TestConstants.TEST_SECRETS;
import static com.nvidia.nvct.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TELEMETRY_METRICS_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TELEMETRY_TRACES_ID;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.TelemetriesUdt;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.ListTasksResponse;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.TaskResponse;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.telemetry.dto.TelemetriesDto;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import java.net.URI;
import java.net.URL;
import java.time.Duration;
import java.util.List;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(classes = {NvctTestApp.class,
        IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class TaskWithTelemetriesTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

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

    @Value("${nvct.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryHostname;

    @SneakyThrows
    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryHostname);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockCasServer.stop();
        MockIcmsServer.stop();
        MockEssServer.stop();
        MockNotaryServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        log.info("{} reset", this.getClass().getSimpleName());
        testTaskService.clearAll();
        MockEssServer.clearSecrets();
    }

    @BeforeEach
    void setup() {
        log.info("{} setup", this.getClass().getSimpleName());
    }

    Stream<Arguments> createTaskWithTelemetriesArgs() {
        var validCompleteTelemetriesDto = TelemetriesDto.builder()
                .logsTelemetryId(TEST_TELEMETRY_LOGS_ID)
                .metricsTelemetryId(TEST_TELEMETRY_METRICS_ID)
                .tracesTelemetryId(TEST_TELEMETRY_TRACES_ID)
                .build();
        var validPartialTelemetriesDto = TelemetriesDto.builder()
                .logsTelemetryId(TEST_TELEMETRY_LOGS_ID)
                .build();
        var mismatchedTelemtriesDto = TelemetriesDto.builder()
                .logsTelemetryId(TEST_TELEMETRY_TRACES_ID)
                .metricsTelemetryId(TEST_TELEMETRY_LOGS_ID)
                .tracesTelemetryId(TEST_TELEMETRY_METRICS_ID)
                .build();
        var mismatchedPartialTelemtriesDto = TelemetriesDto.builder()
                .logsTelemetryId(TEST_TELEMETRY_TRACES_ID)
                .build();

        return Stream.of(
                // Valid telemetries with token for a client that is associated with an account
                // with telemetries.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_4,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             validCompleteTelemetriesDto,
                             TEST_TELEMETRY_LOGS_ID,
                             TEST_TELEMETRY_METRICS_ID,
                             TEST_TELEMETRY_TRACES_ID,
                             HttpStatus.OK),
                // Valid telemetries with token for a client that is associated with an account
                // with telemetries.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_4,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             validPartialTelemetriesDto,
                             TEST_TELEMETRY_LOGS_ID,
                             null,
                             null,
                             HttpStatus.OK),
                // Valid telemetries with token for a client that is NOT associated with an account
                // with telemetries.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             validCompleteTelemetriesDto,
                             TEST_TELEMETRY_LOGS_ID,
                             TEST_TELEMETRY_METRICS_ID,
                             TEST_TELEMETRY_TRACES_ID,
                             HttpStatus.BAD_REQUEST),
                // Invalid telemetries with token for a client that is associated with an account
                // with telemetries.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_4,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             mismatchedTelemtriesDto,
                             null,
                             null,
                             null,
                             HttpStatus.BAD_REQUEST),
                // Invalid telemetries with token for a client that is associated with an account
                // with telemetries.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_4,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             mismatchedPartialTelemtriesDto,
                             null,
                             null,
                             null,
                             HttpStatus.BAD_REQUEST)
                        );
    }

    @ParameterizedTest
    @MethodSource("createTaskWithTelemetriesArgs")
    void shouldCreateTaskWithTelemetries(
            String token,
            TelemetriesDto telemetriesDto,
            UUID logsTelemetryId,
            UUID metricsTelemetryId,
            UUID tracesTelemetryId,
            HttpStatus expectedStatus) {
        var maxRuntimeDuration = Duration.ofHours(2);
        var maxQueuedDuration = Duration.ofHours(3);
        var terminationGracePeriodDuration = Duration.ofHours(1);
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .tags(TEST_TAGS)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .maxRuntimeDuration(maxRuntimeDuration)
                .maxQueuedDuration(maxQueuedDuration)
                .terminationGracePeriodDuration(terminationGracePeriodDuration)
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .secrets(TEST_SECRETS)
                .containerEnvironment(TEST_CONTAINER_ENVIRONMENT)
                .telemetries(telemetriesDto)
                .build();

        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.task().id()).isNotNull();
        assertThat(responseBody.task().name()).isEqualTo(TEST_TASK_NAME_1);
        assertThat(responseBody.task().status()).isEqualTo(TaskStatusEnum.QUEUED);
        assertThat(responseBody.task().ncaId()).isEqualTo(TEST_NCA_ID_WITH_TELEMETRIES_4);
        assertThat(responseBody.task().containerArgs()).isEqualTo(TEST_CONTAINER_ARGS);
        assertThat(responseBody.task().helmChart()).isNull();
        assertThat(responseBody.task().containerImage()).isEqualTo(TEST_CONTAINER_IMAGE);
        assertThat(responseBody.task().containerEnvironment()).isEqualTo(
                TEST_CONTAINER_ENVIRONMENT);
        assertThat(responseBody.task().createdAt()).isNotNull();
        assertThat(responseBody.task().gpuSpecification()).isEqualTo(TEST_OCI_GPU_SPEC_DTO);
        assertThat(responseBody.task().resultsLocation()).isEqualTo(TEST_RESULTS_LOCATION_1);
        assertThat(responseBody.task().resultHandlingStrategy())
                .isEqualTo(ResultHandlingStrategyEnum.UPLOAD);
        assertThat(responseBody.task().maxRuntimeDuration()).isNotNull();
        assertThat(responseBody.task().maxRuntimeDuration().toString())
                .hasToString(maxRuntimeDuration.toString());
        assertThat(responseBody.task().maxQueuedDuration().toString())
                .hasToString(maxQueuedDuration.toString());
        assertThat(responseBody.task().terminationGracePeriodDuration()).isNotNull();
        assertThat(responseBody.task().terminationGracePeriodDuration().toString())
                .hasToString(terminationGracePeriodDuration.toString());
        assertThat(responseBody.task().tags()).isEqualTo(TEST_TAGS);
        assertThat(responseBody.task().description()).isEqualTo(TEST_DESCRIPTION);

        var actualTelemetriesDto = responseBody.task().telemetries();
        assertThat(actualTelemetriesDto).isNotNull();
        assertThat(actualTelemetriesDto.logsTelemetryId()).isEqualTo(logsTelemetryId);
        assertThat(actualTelemetriesDto.metricsTelemetryId()).isEqualTo(metricsTelemetryId);
        assertThat(actualTelemetriesDto.tracesTelemetryId()).isEqualTo(tracesTelemetryId);

        var taskId = responseBody.task().id();
        var entity = tasksRepository
                .getByTaskId(taskId)
                .orElseThrow(() -> new NotFoundException("Task not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getHealth()).isBlank();
    }

    @Test
    void shouldListTasksWithTelemetries() {
        // Create Task in TEST_NCA_ID_WITH_TELEMETRIES_4 account with telemetries.
        var telemetriesUdt = TelemetriesUdt.builder()
                .logsTelemetryId(TEST_TELEMETRY_LOGS_ID)
                .metricsTelemetryId(TEST_TELEMETRY_METRICS_ID)
                .tracesTelemetryId(TEST_TELEMETRY_TRACES_ID)
                .build();
        testTaskService.createTaskWithTelemetries(TEST_NCA_ID_WITH_TELEMETRIES_4,
                                                  TEST_TASK_ID_1,
                                                  TEST_ICMS_REQ_ID_1,
                                                  telemetriesUdt);

        // Create token for the client that is associated with TEST_NCA_ID_WITH_TELEMETRIES_4
        // account.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_4,
                                                    List.of(SCOPE_LIST_TASKS),
                                                    100);
        var requestEntity = RequestEntity.get(URI.create("/v1/nvct/tasks"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListTasksResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var tasks = responseBody.tasks();
        assertThat(tasks).isNotEmpty();

        tasks.forEach(task -> {
            assertThat(task.telemetries()).isNotNull();
            assertThat(task.telemetries().logsTelemetryId()).isEqualTo(TEST_TELEMETRY_LOGS_ID);
            assertThat(task.telemetries().metricsTelemetryId()).isEqualTo(
                    TEST_TELEMETRY_METRICS_ID);
            assertThat(task.telemetries().tracesTelemetryId()).isEqualTo(TEST_TELEMETRY_TRACES_ID);
        });
    }

    @Test
    void shouldGetTaskDetailsWithTelemetries() {
        // Create Task in TEST_NCA_ID_WITH_TELEMETRIES_4 account with telemetries.
        var telemetriesUdt = TelemetriesUdt.builder()
                .logsTelemetryId(TEST_TELEMETRY_LOGS_ID)
                .metricsTelemetryId(TEST_TELEMETRY_METRICS_ID)
                .tracesTelemetryId(TEST_TELEMETRY_TRACES_ID)
                .build();
        testTaskService.createTaskWithTelemetries(TEST_NCA_ID_WITH_TELEMETRIES_4,
                                                  TEST_TASK_ID_1,
                                                  TEST_ICMS_REQ_ID_1,
                                                  telemetriesUdt);

        // Create token for the client that is associated with TEST_NCA_ID_WITH_TELEMETRIES_4
        // account.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_4,
                                                    List.of(SCOPE_TASK_DETAILS),
                                                    100);
        var requestEntity = RequestEntity.get(URI.create("/v1/nvct/tasks/" + TEST_TASK_ID_1))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var task = responseBody.task();
        assertThat(task).isNotNull();
        assertThat(task.id()).isEqualTo(TEST_TASK_ID_1);

        assertThat(task.telemetries()).isNotNull();
        assertThat(task.telemetries().logsTelemetryId()).isEqualTo(TEST_TELEMETRY_LOGS_ID);
        assertThat(task.telemetries().metricsTelemetryId()).isEqualTo(TEST_TELEMETRY_METRICS_ID);
        assertThat(task.telemetries().tracesTelemetryId()).isEqualTo(TEST_TELEMETRY_TRACES_ID);
    }

}
