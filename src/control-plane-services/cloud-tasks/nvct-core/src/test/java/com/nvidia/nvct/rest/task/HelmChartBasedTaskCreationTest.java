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
import static com.nvidia.nvct.util.TestConstants.L40G;
import static com.nvidia.nvct.util.TestConstants.OCI;
import static com.nvidia.nvct.util.TestConstants.OCI_L40G_INSTANCE_TYPE;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ENVIRONMENT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_CHART;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_CHART_NOT_EXISTS;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_CHART_NOT_SUPPORTED_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_CHART_UNKNOWN_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_CHART_WITH_CANARY_HOST;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_VALIDATION_POLICY_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_HELM_CONFIG_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_2;
import static com.nvidia.nvct.util.TestConstants.TEST_SECRETS;
import static com.nvidia.nvct.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.GpuSpecificationDto;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.TaskResponse;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockRevalServer;
import com.nvidia.nvct.util.MockIcmsServer;
import java.net.URI;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.List;
import java.util.stream.Stream;
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
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class HelmChartBasedTaskCreationTest {

    @Autowired
    private TestRestTemplate testRestTemplate;


    @Autowired
    private AccountService accountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestTaskService testTaskService;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvct.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${spring.security.oauth2.client.registration.notary.client-id}")
    private String notaryClientId;

    @Value("${nvct.reval.base-url}")
    private URI revalBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryHostname;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
        MockRevalServer.start(revalBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryHostname);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockEssServer.stop();
        MockNotaryServer.stop();
        MockRevalServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        accountService.invalidateCache();
        testTaskService.clearAll();
    }

    Stream<Arguments> helmBasedTaskArgs() {
        return Stream.of(
                Arguments.of(null,
                             null,
                             null,
                             TEST_HELM_CHART,
                             HttpStatus.OK),
                Arguments.of(TEST_CONTAINER_IMAGE,
                             TEST_CONTAINER_ARGS,
                             TEST_CONTAINER_ENVIRONMENT,
                             null,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(TEST_CONTAINER_IMAGE,
                             null,
                             null,
                             null,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(null,
                             TEST_CONTAINER_ARGS,
                             null,
                             null,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(null,
                             null,
                             TEST_CONTAINER_ENVIRONMENT,
                             null,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(TEST_CONTAINER_IMAGE,
                             null,
                             null,
                             TEST_HELM_CHART,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(null,
                             TEST_CONTAINER_ARGS,
                             null,
                             TEST_HELM_CHART,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(null,
                             null,
                             TEST_CONTAINER_ENVIRONMENT,
                             TEST_HELM_CHART,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(null,
                             null,
                             null,
                             TEST_HELM_CHART_NOT_EXISTS,
                             HttpStatus.NOT_FOUND),
                Arguments.of(null,
                             null,
                             null,
                             TEST_HELM_CHART_PERMISSION_DENIED,
                             HttpStatus.FORBIDDEN),
                Arguments.of(null,
                             null,
                             null,
                             TEST_HELM_CHART_UNKNOWN_REGISTRY,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(null,
                             null,
                             null,
                             TEST_HELM_CHART_NOT_SUPPORTED_REGISTRY,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(null,
                             null,
                             null,
                             TEST_HELM_CHART_WITH_CANARY_HOST,
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("helmBasedTaskArgs")
    void shouldLaunchHelmChartBasedTasks(
            URI containerImage,
            String containerArgs,
            List containerEnvironment,
            URI helmChart,
            HttpStatus expectedStatus) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                    100);
        var requestBodyBuilder = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(containerArgs)
                .helmChart(helmChart)
                .containerImage(containerImage)
                .tags(TEST_TAGS)
                .gpuSpecification(TEST_OCI_GPU_SPEC_HELM_CONFIG_DTO)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(1))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .secrets(TEST_SECRETS)
                .containerEnvironment(containerEnvironment);
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBodyBuilder.build());
        var responseEntity =
                testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.task().id()).isNotNull();
        assertThat(responseBody.task().name()).isEqualTo(TEST_TASK_NAME_1);
        assertThat(responseBody.task().status()).isEqualTo(TaskStatusEnum.QUEUED);
        assertThat(responseBody.task().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.task().containerArgs()).isEqualTo(containerArgs);
        assertThat(responseBody.task().containerEnvironment()).isEqualTo(containerEnvironment);
        assertThat(responseBody.task().createdAt()).isNotNull();
        assertThat(responseBody.task().gpuSpecification()).isEqualTo(
                TEST_OCI_GPU_SPEC_HELM_CONFIG_DTO);
        assertThat(responseBody.task().containerImage()).isEqualTo(containerImage);
        assertThat(responseBody.task().helmChart()).isEqualTo(helmChart);
    }

    @Test
    void shouldFailToLaunchHelmChartBasedTask() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                    100);
        // MockRevalServer is setup report validation failure when this configuration is used.
        var configuration = jsonMapper.createObjectNode()
                .put("serviceAccountName", "nvct")
                .put("fail", "fail");
        var gpuSpec = GpuSpecificationDto.builder().gpu(L40G).instanceType(OCI_L40G_INSTANCE_TYPE)
                .backend(OCI).configuration(configuration).build();
        var requestBodyBuilder = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .helmChart(TEST_HELM_CHART)
                .tags(TEST_TAGS)
                .gpuSpecification(gpuSpec)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(1))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .secrets(TEST_SECRETS)
                .containerEnvironment(TEST_CONTAINER_ENVIRONMENT);
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBodyBuilder.build());
        var responseEntity =
                testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    Stream<Arguments> invalidHelmChartUriArgs() {
        return Stream.of(
                Arguments.of(URI.create("ftp://registry.example.com/chart.tgz")),
                Arguments.of(URI.create("file:///path/to/chart.tgz")),
                Arguments.of(URI.create("ldap://registry.example.com/chart")),
                Arguments.of(URI.create("git://registry.example.com/chart.tgz")),
                Arguments.of(URI.create(
                        "helm.stg.ngc.nvidia.com/test-org/charts/test-chart-1.0.0.tgz")),
                Arguments.of(URI.create(
                        "123456789000.dkr.ecr.us-west-2.amazonaws.com/test-repo/test-chart:1.0.0"))
        );
    }

    @Test
    void shouldStoreAndReturnHelmValidationPolicy() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS,
                                                   SCOPE_TASK_DETAILS),
                                                    100);
        var createRequest = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .helmChart(TEST_HELM_CHART)
                .gpuSpecification(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(1))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .secrets(TEST_SECRETS)
                .build();
        var createEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(createRequest);
        var createResponse = testRestTemplate.exchange(createEntity, TaskResponse.class);

        assertThat(createResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        var created = createResponse.getBody();
        assertThat(created).isNotNull();
        assertThat(created.task().gpuSpecification()).isEqualTo(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO);

        var taskId = created.task().id();
        var getEntity = RequestEntity.get(URI.create("/v1/nvct/tasks/" + taskId))
                .header("Authorization", "Bearer " + token)
                .build();
        var getResponse = testRestTemplate.exchange(getEntity, TaskResponse.class);

        assertThat(getResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        var retrieved = getResponse.getBody();
        assertThat(retrieved).isNotNull();
        var retrievedSpec = retrieved.task().gpuSpecification();
        assertThat(retrievedSpec.gpu()).isEqualTo(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.gpu());
        assertThat(retrievedSpec.backend()).isEqualTo(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.backend());
        assertThat(retrievedSpec.instanceType())
                .isEqualTo(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.instanceType());
        assertThat(retrievedSpec.helmValidationPolicy())
                .isEqualTo(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.helmValidationPolicy());
    }

    @Test
    @lombok.SneakyThrows
    void shouldSendHelmValidationPolicyToIcms() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                    100);
        var createRequest = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .helmChart(TEST_HELM_CHART)
                .gpuSpecification(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(1))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .secrets(TEST_SECRETS)
                .build();
        var createEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(createRequest);
        var createResponse = testRestTemplate.exchange(createEntity, TaskResponse.class);

        assertThat(createResponse.getStatusCode()).isEqualTo(HttpStatus.OK);

        var expectedPolicyJson = jsonMapper.writeValueAsString(TEST_HELM_VALIDATION_POLICY_DTO);
        var policyRaw = MockIcmsServer.getCapturedHelmValidationPolicy();
        var policy = new String(Base64.getDecoder().decode(policyRaw), StandardCharsets.UTF_8);
        assertThat(policy).isEqualTo(expectedPolicyJson);
    }

    @ParameterizedTest
    @MethodSource("invalidHelmChartUriArgs")
    void shouldFailWithInvalidHelmChartUri(URI helmChartUri) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                    100);
        var requestBodyBuilder = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .helmChart(helmChartUri)
                .tags(TEST_TAGS)
                .gpuSpecification(TEST_OCI_GPU_SPEC_HELM_CONFIG_DTO)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(1))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                .description(TEST_DESCRIPTION)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .resultsLocation(TEST_RESULTS_LOCATION_2)
                .secrets(TEST_SECRETS);
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBodyBuilder.build());
        var responseEntity = testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }
}
