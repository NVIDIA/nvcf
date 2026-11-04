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
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_4;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ENVIRONMENT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_WITH_TELEMETRIES_4;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;
import static com.nvidia.nvct.util.TestConstants.TEST_SECRETS;
import static com.nvidia.nvct.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TELEMETRY_METRICS_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TELEMETRY_TRACES_ID;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.TaskResponse;
import com.nvidia.nvct.service.telemetry.dto.TelemetriesDto;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import jakarta.annotation.Nullable;
import java.net.URI;
import java.net.URL;
import java.time.Duration;
import java.util.List;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
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
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(classes = {NvctTestApp.class,
        IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {"spring.profiles.active=test", "nvct.telemetry.enabled=false"})
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class TaskWithTelemetryDisabledTest {

    private static final String MESG_TELEMETRY_UNSUPPORTED =
            "telemetry is not supported on this deployment";

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
        testTaskService.clearAll();
        MockEssServer.clearSecrets();
    }

    Stream<Arguments> telemetriesArgs() {
        return Stream.of(
                Arguments.of(TelemetriesDto.builder()
                        .logsTelemetryId(TEST_TELEMETRY_LOGS_ID)
                        .metricsTelemetryId(TEST_TELEMETRY_METRICS_ID)
                        .tracesTelemetryId(TEST_TELEMETRY_TRACES_ID)
                        .build()),
                Arguments.of(TelemetriesDto.builder()
                        .logsTelemetryId(TEST_TELEMETRY_LOGS_ID)
                        .build()),
                Arguments.of(TelemetriesDto.builder()
                        .metricsTelemetryId(TEST_TELEMETRY_METRICS_ID)
                        .build()),
                Arguments.of(TelemetriesDto.builder()
                        .tracesTelemetryId(TEST_TELEMETRY_TRACES_ID)
                        .build()));
    }

    @ParameterizedTest
    @MethodSource("telemetriesArgs")
    void rejectsTaskCarryingTelemetriesBeforeAnythingIsWritten(TelemetriesDto telemetries) {
        var response = createTask(telemetries, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(response.getBody()).contains(MESG_TELEMETRY_UNSUPPORTED);
        assertThat(tasksRepository.countByNcaId(TEST_NCA_ID_WITH_TELEMETRIES_4)).isZero();
    }

    @Test
    void acceptsTaskWithoutTelemetries() {
        var response = createTask(null, TaskResponse.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().task().id()).isNotNull();
        assertThat(response.getBody().task().ncaId()).isEqualTo(TEST_NCA_ID_WITH_TELEMETRIES_4);
    }

    private <T> ResponseEntity<T> createTask(
            @Nullable TelemetriesDto telemetries, Class<T> responseType) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(
                TEST_CLIENT_ID_4, List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS), 100);
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .tags(TEST_TAGS)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(3))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .secrets(TEST_SECRETS)
                .containerEnvironment(TEST_CONTAINER_ENVIRONMENT)
                .telemetries(telemetries)
                .build();

        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        return testRestTemplate.exchange(requestEntity, responseType);
    }
}
