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
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_CHART;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_VALIDATION_POLICY_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;
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
import com.nvidia.nvct.rest.task.dto.HelmValidationPolicyDto;
import com.nvidia.nvct.rest.task.dto.ListTasksResponse;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.TaskResponse;
import com.nvidia.nvct.rest.task.dto.ValidationPolicyNameEnum;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockRevalServer;
import com.nvidia.nvct.util.MockIcmsServer;
import java.net.URI;
import java.net.URL;
import java.time.Duration;
import java.util.List;
import java.util.stream.Stream;
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
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(classes = {NvctTestApp.class,
        IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class TaskWithHelmValidationPolicyTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvct.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryHostname;

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

    @Value("${nvct.reval.base-url}")
    private URI revalBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
        MockRevalServer.start(revalBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryHostname);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        MockCasServer.stop();
        MockIcmsServer.stop();
        MockEssServer.stop();
        MockNotaryServer.stop();
        MockRevalServer.stop();
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

    Stream<Arguments> createTaskWithHelmValidationPolicyArgs() {
        var validPolicyDefault = HelmValidationPolicyDto.builder()
                .name(ValidationPolicyNameEnum.DEFAULT)
                .extraKubernetesTypes(List.of(
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("apps")
                                .version("v1")
                                .kind("Deployment")
                                .build()))
                .build();
        var validPolicyUnrestricted = HelmValidationPolicyDto.builder()
                .name(ValidationPolicyNameEnum.UNRESTRICTED)
                .build();
        var validPolicyNoExtraTypes = HelmValidationPolicyDto.builder()
                .name(ValidationPolicyNameEnum.DEFAULT)
                .build();
        var invalidPolicyNullName = HelmValidationPolicyDto.builder()
                .name(null)
                .extraKubernetesTypes(List.of(
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("apps")
                                .version("v1")
                                .kind("Deployment")
                                .build()))
                .build();
        var invalidPolicyBlankGroup = HelmValidationPolicyDto.builder()
                .name(ValidationPolicyNameEnum.DEFAULT)
                .extraKubernetesTypes(List.of(
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("")
                                .version("v1")
                                .kind("Deployment")
                                .build()))
                .build();
        var invalidPolicyBlankVersion = HelmValidationPolicyDto.builder()
                .name(ValidationPolicyNameEnum.DEFAULT)
                .extraKubernetesTypes(List.of(
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("apps")
                                .version("")
                                .kind("Deployment")
                                .build()))
                .build();
        var invalidPolicyBlankKind = HelmValidationPolicyDto.builder()
                .name(ValidationPolicyNameEnum.DEFAULT)
                .extraKubernetesTypes(List.of(
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("apps")
                                .version("v1")
                                .kind("")
                                .build()))
                .build();
        var invalidPolicyEmptyExtraTypes = HelmValidationPolicyDto.builder()
                .name(ValidationPolicyNameEnum.DEFAULT)
                .extraKubernetesTypes(List.of())
                .build();

        return Stream.of(
                // Valid policy with DEFAULT name and extraKubernetesTypes.
                Arguments.of(validPolicyDefault, HttpStatus.OK),
                // Valid policy with UNRESTRICTED name and no extraKubernetesTypes.
                Arguments.of(validPolicyUnrestricted, HttpStatus.OK),
                // Valid policy with DEFAULT name and no extraKubernetesTypes.
                Arguments.of(validPolicyNoExtraTypes, HttpStatus.OK),
                // Invalid policy with null name.
                Arguments.of(invalidPolicyNullName, HttpStatus.BAD_REQUEST),
                // Invalid policy with blank group in extraKubernetesTypes.
                Arguments.of(invalidPolicyBlankGroup, HttpStatus.BAD_REQUEST),
                // Invalid policy with blank version in extraKubernetesTypes.
                Arguments.of(invalidPolicyBlankVersion, HttpStatus.BAD_REQUEST),
                // Invalid policy with blank kind in extraKubernetesTypes.
                Arguments.of(invalidPolicyBlankKind, HttpStatus.BAD_REQUEST),
                // Invalid policy with empty extraKubernetesTypes list.
                Arguments.of(invalidPolicyEmptyExtraTypes, HttpStatus.BAD_REQUEST));
    }

    @ParameterizedTest
    @MethodSource("createTaskWithHelmValidationPolicyArgs")
    void shouldCreateTaskWithHelmValidationPolicy(
            HelmValidationPolicyDto helmValidationPolicy,
            HttpStatus expectedStatus) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK,
                                                   SCOPE_LIST_TASKS),
                                                    100);
        var gpuSpec = GpuSpecificationDto.builder()
                .gpu(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.gpu())
                .instanceType(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.instanceType())
                .backend(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.backend())
                .helmValidationPolicy(helmValidationPolicy)
                .build();
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
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
                .build();

        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
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
        assertThat(responseBody.task().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.task().gpuSpecification()).isNotNull();
        assertThat(responseBody.task().gpuSpecification().helmValidationPolicy())
                .isEqualTo(helmValidationPolicy);
    }

    @Test
    void shouldListTasksWithHelmValidationPolicy() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK,
                                                   SCOPE_LIST_TASKS),
                                                    100);
        var requestBody = CreateTaskRequest.builder()
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
                .body(requestBody);
        var createResponse =
                testRestTemplate.exchange(createEntity, TaskResponse.class);
        assertThat(createResponse.getStatusCode()).isEqualTo(HttpStatus.OK);

        var listEntity = RequestEntity.get(URI.create("/v1/nvct/tasks"))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponse =
                testRestTemplate.exchange(listEntity, ListTasksResponse.class);
        assertThat(listResponse.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = listResponse.getBody();
        assertThat(responseBody).isNotNull();

        var tasks = responseBody.tasks();
        assertThat(tasks).isNotEmpty();

        tasks.forEach(task -> {
            assertThat(task.gpuSpecification()).isNotNull();
            assertThat(task.gpuSpecification().helmValidationPolicy())
                    .isEqualTo(TEST_HELM_VALIDATION_POLICY_DTO);
        });
    }

    @Test
    void shouldGetTaskDetailsWithHelmValidationPolicy() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK,
                                                   SCOPE_TASK_DETAILS),
                                                    100);
        var requestBody = CreateTaskRequest.builder()
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
                .body(requestBody);
        var createResponse =
                testRestTemplate.exchange(createEntity, TaskResponse.class);
        assertThat(createResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        var taskId = createResponse.getBody().task().id();

        var getEntity = RequestEntity
                .get(URI.create("/v1/nvct/tasks/" + taskId))
                .header("Authorization", "Bearer " + token)
                .build();
        var getResponse =
                testRestTemplate.exchange(getEntity, TaskResponse.class);
        assertThat(getResponse.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = getResponse.getBody();
        assertThat(responseBody).isNotNull();

        var task = responseBody.task();
        assertThat(task).isNotNull();
        assertThat(task.id()).isEqualTo(taskId);
        assertThat(task.gpuSpecification()).isNotNull();
        assertThat(task.gpuSpecification().helmValidationPolicy())
                .isEqualTo(TEST_HELM_VALIDATION_POLICY_DTO);
    }

    @Test
    void shouldRejectContainerBasedTaskWithHelmValidationPolicy() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK),
                                                    100);
        var gpuSpec = GpuSpecificationDto.builder()
                .gpu(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.gpu())
                .instanceType(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.instanceType())
                .backend(TEST_OCI_GPU_SPEC_WITH_HELM_POLICY_DTO.backend())
                .helmValidationPolicy(TEST_HELM_VALIDATION_POLICY_DTO)
                .build();
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerImage(TEST_CONTAINER_IMAGE)
                .tags(TEST_TAGS)
                .gpuSpecification(gpuSpec)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(1))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.UPLOAD)
                .secrets(TEST_SECRETS)
                .build();

        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }
}
