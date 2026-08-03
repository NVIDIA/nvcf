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
import static com.nvidia.nvct.util.NvctConstants.ADMIN_SCOPE_DELETE_TASK;
import static com.nvidia.nvct.util.NvctConstants.ADMIN_SCOPE_LAUNCH_TASK;
import static com.nvidia.nvct.util.NvctConstants.ADMIN_SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.NvctConstants.ADMIN_SCOPE_TASK_DETAILS;
import static com.nvidia.nvct.util.NvctConstants.MAX_SECRET_NAME_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.MAX_SECRET_VALUE_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.NGC_API_KEY;
import static com.nvidia.nvct.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.ListTasksResponse;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.rest.task.dto.TaskResponse;
import com.nvidia.nvct.service.ess.EssService;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import java.net.URI;
import java.net.URL;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
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
class XAccountTaskWithUserSecretsTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private EssService essService;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TasksRepository tasksRepository;

    @Value("${nvct.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryHostname;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

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

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockEssServer.start(essBaseUrl);
        MockNvcfServer.start(nvcfBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryHostname);
    }

    @AfterAll
    void cleanup() {
        MockEssServer.stop();
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockNotaryServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testTaskService.clearAll();
        MockEssServer.clearSecrets();
    }

    Stream<Arguments> createTaskWithSecretsArgs() {
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        return Stream.of(
                // no secrets with default resultHandlingStrategy
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             null,
                             null,
                             null,
                             HttpStatus.BAD_REQUEST),
                // no secrets with resultHandlingStrategy UPLOAD(default)
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             null,
                             null,
                             ResultHandlingStrategyEnum.UPLOAD,
                             HttpStatus.BAD_REQUEST),
                // Only NGC_API_KEY secret
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder().name(NGC_API_KEY).value(new StringNode("value1")).build()),
                             Set.of(NGC_API_KEY),
                             ResultHandlingStrategyEnum.UPLOAD,
                             HttpStatus.OK),
                // single secret
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder().name(NGC_API_KEY).value(new StringNode("value1")).build()),
                             Set.of(NGC_API_KEY),
                             ResultHandlingStrategyEnum.UPLOAD,
                             HttpStatus.OK),
                // multiple secrets
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder().name(NGC_API_KEY).value(new StringNode("value1")).build(),
                                    SecretDto.builder().name("secret2").value(new StringNode("value2")).build(),
                                    SecretDto.builder().name("secret3").value(secretJsonNodeValue).build()),
                             Set.of(NGC_API_KEY, "secret2", "secret3"),
                             ResultHandlingStrategyEnum.UPLOAD,
                             HttpStatus.OK),
                // Secret name -- exactly MAX_SECRET_NAME_LENGTH in length
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(StringUtils.repeat("x", MAX_SECRET_NAME_LENGTH))
                                            .value(new StringNode("value1")).build()),
                             Set.of(StringUtils.repeat("x", MAX_SECRET_NAME_LENGTH)),
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.OK),
                // secret names with periods, and hyphens
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("omni.s3.us-west-2.amazonaws.com")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder()
                                            .name("omni.s3.eu-north-1.amazonaws.com")
                                            .value(new StringNode("value2")).build(),
                                    SecretDto.builder()
                                            .name("omni.s3.ap-northeast-1.amazonaws.com")
                                            .value(secretJsonNodeValue).build()),
                             Set.of("omni.s3.us-west-2.amazonaws.com",
                                    "omni.s3.eu-north-1.amazonaws.com",
                                    "omni.s3.ap-northeast-1.amazonaws.com"),
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.OK),
                // secret names with underscores
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("omni_s3_us-west-2_amazonaws_com")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder()
                                            .name("omni_s3_eu-north-1_amazonaws_com")
                                            .value(new StringNode("value2")).build(),
                                    SecretDto.builder()
                                            .name("omni.s3.ap-northeast-1.amazonaws.com")
                                            .value(secretJsonNodeValue).build()),
                             Set.of("omni_s3_us-west-2_amazonaws_com",
                                    "omni_s3_eu-north-1_amazonaws_com",
                                    "omni.s3.ap-northeast-1.amazonaws.com"),
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.OK),
                // resultHandlingStrategy NONE and missing NGC_API_KEY
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("secret2")
                                            .value(new StringNode("value2")).build()),
                             Set.of("secret2"),
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.OK),
                // resultHandlingStrategy UPLOAD and missing NGC_API_KEY
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("secret2")
                                            .value(new StringNode("value2")).build()),
                             Set.of("secret2"),
                             ResultHandlingStrategyEnum.UPLOAD,
                             HttpStatus.BAD_REQUEST),
                // duplicate secrets
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(NGC_API_KEY)
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder()
                                            .name(NGC_API_KEY)
                                            .value(new StringNode("value2")).build()),
                             null,
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.BAD_REQUEST),
                // empty secret name
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("")
                                            .value(new StringNode("value1")).build()),
                             null,
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.BAD_REQUEST),
                // long secret name - exceeds MAX_SECRET_NAME_LENGTH length
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(StringUtils.repeat("secret1", MAX_SECRET_NAME_LENGTH))
                                            .value(new StringNode("value1")).build()),
                             null,
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.BAD_REQUEST),
                // empty secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("")).build()),
                             null,
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.BAD_REQUEST),
                // long secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode(StringUtils.repeat("value1",
                                                                                   MAX_SECRET_VALUE_LENGTH)))
                                            .build()),
                             null,
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.BAD_REQUEST),
                // bad secret name
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LAUNCH_TASK),
                                                             100),
                             Set.of(SecretDto.builder().name("*secret1*-\"").value(new StringNode("value1")).build()),
                             null,
                             ResultHandlingStrategyEnum.NONE,
                             HttpStatus.BAD_REQUEST)
                        );
    }

    @ParameterizedTest
    @MethodSource("createTaskWithSecretsArgs")
    void shouldCreateTaskWithSecrets(
            String token,
            Set<SecretDto> secrets,
            Set<String> expectedSecretNames,
            ResultHandlingStrategyEnum resultHandlingStrategy,
            HttpStatus expectedStatus) {
        // Create the task in TEST_NCA_ID
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .tags(TEST_TAGS)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .description(TEST_DESCRIPTION)
                .resultsLocation(TEST_RESULTS_LOCATION_2)
                .resultHandlingStrategy(resultHandlingStrategy)
                .secrets(secrets)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks"))
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
        var taskId = responseBody.task().id();
        var taskDto = responseBody.task();
        if (expectedSecretNames == null) {
            assertThat(taskDto.secrets()).isNull();
        } else {
            assertThat(taskDto.secrets())
                    .containsExactlyInAnyOrderElementsOf(expectedSecretNames);

            // Confirm secrets are saved in ESS
           var returnedNames = essService.getSecretNames(taskId).orElse(null);
           assertThat(returnedNames).isNotNull();
           assertThat(returnedNames).containsExactlyInAnyOrderElementsOf(expectedSecretNames);
           
           // Confirm hasSecrets is set
           var taskEntity = tasksRepository.getByTaskId(taskId).orElse(null);
           assertThat(taskEntity).isNotNull();
           assertThat(taskEntity.hasSecrets()).isTrue();
        }

    }

    @Test
    void shouldListTasksWithSecretsByAccount() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_LAUNCH_TASK,
                                                 ADMIN_SCOPE_LIST_TASKS),
                                                    100);
        // Create a task with secrets in TEST_NCA_ID
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        var secrets = Set.of(SecretDto.builder().name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("value1")).build(),
                             SecretDto.builder().name("NGC_API_KEY")
                                     .value(new StringNode("value2")).build(),
                             SecretDto.builder().name("OV.US-WEST-2.CONTENT")
                                     .value(secretJsonNodeValue).build());
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .secrets(secrets)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // After task creation, list task
        var endpoint = "/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks";
        var listRequestEntity = RequestEntity.get(URI.create(endpoint))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, ListTasksResponse.class);
        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.tasks()).hasSize(1);

        var taskDto = responseBody.tasks().getFirst();
        assertThat(taskDto).isNotNull();
        assertThat(taskDto.secrets()).isNull();
    }

    Stream<Arguments> listTasksWithSecretsArgs() {
        return Stream.of(
                Arguments.of(Boolean.TRUE),
                Arguments.of(Boolean.FALSE),
                Arguments.of(Boolean.TRUE),
                Arguments.of(Boolean.FALSE));
    }

    @ParameterizedTest
    @MethodSource("listTasksWithSecretsArgs")
    void shouldGetTaskDetailsWithSecretsByAccount(Boolean includeSecrets) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_LAUNCH_TASK,
                                                 ADMIN_SCOPE_TASK_DETAILS),
                                                    100);
        // Create a task with secrets in TEST_NCA_ID
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        var secrets = Set.of(SecretDto.builder().name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("value1")).build(),
                             SecretDto.builder().name("NGC_API_KEY")
                                     .value(new StringNode("value2")).build(),
                             SecretDto.builder().name("OV.US-WEST-2.CONTENT")
                                     .value(secretJsonNodeValue).build());
        var expectedSecretNames = Set.of("AWS_SECRET_ACCESS_KEY",
                                         "NGC_API_KEY", "OV.US-WEST-2.CONTENT");
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .secrets(secrets)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var taskId = responseEntity.getBody().task().id();

        // After task creation, get task details
        var endpoint = "/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks/" + taskId;
        if (includeSecrets != null) {
            endpoint += "?includeSecrets=" + includeSecrets;
        }
        var listRequestEntity = RequestEntity.get(URI.create(endpoint))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var taskDto = responseBody.task();
        assertThat(taskDto).isNotNull();
        if ((includeSecrets == null) || includeSecrets) {
            assertThat(taskDto.secrets()).isNotNull();
            assertThat(taskDto.secrets()).containsExactlyInAnyOrderElementsOf(expectedSecretNames);
        } else {
            assertThat(taskDto.secrets()).isNull();
        }
    }

    @Test
    void shouldDeleteTaskWithSecrets() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_LAUNCH_TASK,
                                     ADMIN_SCOPE_DELETE_TASK),
                                                    100);

        // Create a task with secrets in TEST_NCA_ID
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        var secrets = Set.of(SecretDto.builder().name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("value1")).build(),
                             SecretDto.builder().name("NGC_API_KEY")
                                     .value(new StringNode("value2")).build(),
                             SecretDto.builder().name("OV.US-WEST-2.CONTENT")
                                     .value(secretJsonNodeValue).build());
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .secrets(secrets)
                .build();
        var requestEntity =
                RequestEntity.post(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var taskId = responseEntity.getBody().task().id();

        // Make sure there are secrets first before deleting a task
        var secretDtos = essService.getSecrets(taskId).orElse(null);
        assertThat(secretDtos).isNotNull().hasSize(3);
        
        // Confirm hasSecrets is set
        var taskEntity = tasksRepository.getByTaskId(taskId).orElse(null);
        assertThat(taskEntity).isNotNull();
        assertThat(taskEntity.hasSecrets()).isTrue();

        var deleteRequestEntity =
                RequestEntity.delete(URI.create("/v1/nvct/accounts/" + TEST_NCA_ID + "/tasks/" + taskId))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var deleteResponseEntity = testRestTemplate.exchange(deleteRequestEntity, Void.class);
        assertThat(deleteResponseEntity.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);
        var entity = tasksRepository
                .getByTaskId(taskId);
        assertThat(entity).isEmpty();

        // Make sure secrets no longer exist after task deletion
        secretDtos = essService.getSecrets(taskId).orElse(null);
        assertThat(secretDtos).isNull();
    }
}
