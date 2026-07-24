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

import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG;
import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LAUNCH_TASK;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_HELM_CONFIG_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_2;
import static com.nvidia.nvct.util.TestConstants.TEST_SECRETS;
import static com.nvidia.nvct.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.mock.ecr.MockEcrPrivateRegistryServer;
import com.nvidia.boot.mock.ecr.MockEcrPublicRegistryServer;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
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
import java.time.Duration;
import java.util.List;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
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
class TaskCreationWithEcrRegistryTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private AccountService accountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestTaskService testTaskService;

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

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.registries.recognized.container.ecr.hostname}")
    private String ecrPrivateBaseUrl;

    @Value("${nvct.registries.recognized.container.ecr-public.hostname}")
    private String ecrPublicBaseUrl;

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
        MockEcrPrivateRegistryServer.start(ecrPrivateBaseUrl);
        MockEcrPublicRegistryServer.start(ecrPublicBaseUrl);
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
        MockEcrPrivateRegistryServer.stop();
        MockEcrPublicRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        accountService.invalidateCache();
        testTaskService.clearAll();
    }

    Stream<Arguments> CreateTaskWithContainerImageArgs() {
        return Stream.of(
                // existing ecr private image
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_WITH_TAG, HttpStatus.OK),
                // ecr private image with digest
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST, HttpStatus.OK),
                // ecr private image tag does not exist
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_TAG_NOT_FOUND, HttpStatus.BAD_REQUEST),
                // ecr private image digest does not exist
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_DIGEST_NOT_FOUND, HttpStatus.BAD_REQUEST),
                // ecr private image no permission
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_PERMISSION_DENIED, HttpStatus.FORBIDDEN),
                // existing ecr public image
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG, HttpStatus.OK),
                // ecr public image with digest
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST, HttpStatus.OK),
                // ecr public image tag does not exist
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_TAG_NOT_FOUND, HttpStatus.BAD_REQUEST),
                // ecr public image digest does not exist
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST_NOT_FOUND,
                             HttpStatus.BAD_REQUEST),
                // ecr public image no permission
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_PERMISSION_DENIED,
                             HttpStatus.FORBIDDEN)
        );
    }

    @ParameterizedTest
    @MethodSource("CreateTaskWithContainerImageArgs")
    void shouldLaunchContainerBasedTask(
            URI containerImage,
            HttpStatus expectedStatus) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                    100);
        var requestBodyBuilder = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(containerImage)
                .tags(TEST_TAGS)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
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
        assertThat(responseBody.task().createdAt()).isNotNull();
        assertThat(responseBody.task().containerImage()).isEqualTo(containerImage);
    }

    Stream<Arguments> CreateTaskWithHelmChartArgs() {
        return Stream.of(
                // existing ecr private helm chart
                Arguments.of(TEST_ECR_HELM_CHART_WITH_TAG, HttpStatus.OK),
                // ecr private helm chart with digest
                Arguments.of(TEST_ECR_HELM_CHART_WITH_DIGEST, HttpStatus.OK),
                // ecr private helm chart tag does not exist
                Arguments.of(TEST_ECR_HELM_CHART_TAG_NOT_FOUND, HttpStatus.BAD_REQUEST),
                // ecr private helm chart digest does not exist
                Arguments.of(TEST_ECR_HELM_CHART_DIGEST_NOT_FOUND, HttpStatus.BAD_REQUEST),
                // ecr private helm chart no permission
                Arguments.of(TEST_ECR_HELM_CHART_PERMISSION_DENIED, HttpStatus.FORBIDDEN),

                // existing ecr public helm chart
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG, HttpStatus.OK),
                // ecr private helm chart with digest
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST, HttpStatus.OK),
                // ecr private helm chart tag does not exist
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_TAG_NOT_FOUND, HttpStatus.BAD_REQUEST),
                // ecr private helm chart digest does not exist
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_DIGEST_NOT_FOUND, HttpStatus.BAD_REQUEST),
                // ecr private helm chart no permission
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_PERMISSION_DENIED, HttpStatus.FORBIDDEN)
        );
    }

    @ParameterizedTest
    @MethodSource("CreateTaskWithHelmChartArgs")
    void shouldLaunchTasksWithHelmCharts(
            URI helmChart,
            HttpStatus expectedStatus) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                    100);
        var requestBodyBuilder = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .helmChart(helmChart)
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
        assertThat(responseBody.task().createdAt()).isNotNull();
        assertThat(responseBody.task().helmChart()).isEqualTo(helmChart);
    }
}
