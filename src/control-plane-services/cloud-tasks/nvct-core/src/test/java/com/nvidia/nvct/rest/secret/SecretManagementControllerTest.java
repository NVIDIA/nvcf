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
package com.nvidia.nvct.rest.secret;

import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.CANCELED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.COMPLETED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.ERRORED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.EXCEEDED_MAX_QUEUED_DURATION;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.EXCEEDED_MAX_RUNTIME_DURATION;
import static com.nvidia.nvct.util.NvctConstants.MAX_SECRET_NAME_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.MAX_SECRET_VALUE_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.NGC_API_KEY;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_UPDATE_SECRETS;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT_2;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.entity.ResultHandlingStrategy;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.secret.dto.UpdateSecretsRequest;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvct.service.apikeys.ApiKeysService;
import com.nvidia.nvct.service.ess.EssService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.util.MockApiKeysServer;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNvcfServer;
import java.net.URI;
import java.net.URL;
import java.util.List;
import java.util.Set;
import java.util.function.Supplier;
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
class SecretManagementControllerTest {

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestRestTemplate testRestTemplate;


    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private EssService essService;

    @Autowired
    private TaskService taskService;

    @Autowired
    private AccountService accountService;

    @Autowired
    private ApiKeysService apiKeysService;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());

        MockApiKeysServer.start(apiKeysBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNvcfServer.start(nvcfBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockNvcfServer.stop();
        MockCasServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        MockApiKeysServer.resetToDefault();
        // use of MockApiKeysServer with different scopes causes the api-keys cache to dirty
        apiKeysService.invalidateCache();
        accountService.invalidateCache();
        testTaskService.clearAll();
        MockEssServer.clearSecrets();
    }

    Stream<Arguments> updateSecretArgs() {
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        var jwtCases = Stream.of(
                // no secrets with default resultHandlingStrategy
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             null,
                             null,
                             HttpStatus.BAD_REQUEST),
                // no secrets with resultHandlingStrategy UPLOAD
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             null,
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.BAD_REQUEST),
                // single secret
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(NGC_API_KEY)
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.NO_CONTENT),
                // update secret of a Task that belongs to a different account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT_2,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(NGC_API_KEY)
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.NOT_FOUND),
                // duplicate secrets
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("value2")).build()),
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.BAD_REQUEST),
                // Secret name -- exactly MAX_SECRET_NAME_LENGTH in length
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(StringUtils.repeat("x", MAX_SECRET_NAME_LENGTH))
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("value2")).build()),
                             ResultHandlingStrategy.NONE,
                             HttpStatus.NO_CONTENT),
                // secret names with periods, and hyphens
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
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
                             ResultHandlingStrategy.NONE,
                             HttpStatus.NO_CONTENT),
                // secret names with underscores
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
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
                             ResultHandlingStrategy.NONE,
                             HttpStatus.NO_CONTENT),
                // empty secret name
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("")
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.NONE,
                             HttpStatus.BAD_REQUEST),
                // long secret name - exceeds MAX_SECRET_NAME_LENGTH chars
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(StringUtils.repeat("secret1", MAX_SECRET_NAME_LENGTH))
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.NONE,
                             HttpStatus.BAD_REQUEST),
                // empty secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("")
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.NONE,
                             HttpStatus.BAD_REQUEST),
                // long secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode(StringUtils.repeat("value1",
                                                                                   MAX_SECRET_VALUE_LENGTH)))
                                            .build()),
                             ResultHandlingStrategy.NONE,
                             HttpStatus.BAD_REQUEST),
                // bad secret name
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS),
                                                             100),
                             Set.of(SecretDto.builder().name("*secret1*-\"")
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.NONE,
                             HttpStatus.BAD_REQUEST)
        );

        var apiKeysCases = Stream.of(
                // api-key with update_secrets scope and account-tasks resource
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("account-tasks", "*")),
                                                               List.of("update_secrets"));
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.NO_CONTENT),
                // api-key with update_secrets scope and specific task resource (matching)
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_1.toString())),
                                                               List.of("update_secrets"));
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.NO_CONTENT),
                // api-key with update_secrets scope and specific task resource (not matching)
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_2.toString())),
                                                               List.of("update_secrets"));
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.FORBIDDEN),
                // api-key with scope but no resources
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(),
                                                               List.of("update_secrets"));
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.FORBIDDEN),
                // api-key with missing scope
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(), List.of());
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(SecretDto.builder()
                                            .name("secret1")
                                            .value(new StringNode("value1")).build()),
                             ResultHandlingStrategy.UPLOAD,
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeysCases);
    }

    @ParameterizedTest
    @MethodSource("updateSecretArgs")
    void shouldUpdateForTasksWithSecrets(
            Object tokenSupplier,
            Set<SecretDto> secrets,
            ResultHandlingStrategy resultHandlingStrategy,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        // Create a task with a secret in TEST_NCA_ID
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1,
                                   TEST_ICMS_REQ_ID_1, resultHandlingStrategy);
        essService.saveSecrets(TEST_TASK_ID_1,
                               Set.of(SecretDto.builder()
                                              .name("test")
                                              .value(new StringNode("value1"))
                                              .build()));
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        // Update the task with new secrets
        var updateRequestBody = UpdateSecretsRequest.builder()
                .secrets(secrets)
                .build();
        var url = URI.create("/v1/nvct/secrets/tasks/" + TEST_TASK_ID_1);
        var updateRequestEntity = RequestEntity.put(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(updateRequestBody);
        var updateResponseEntity = testRestTemplate.exchange(updateRequestEntity, Void.class);
        assertThat(updateResponseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        // Check new secrets are actually added
        var newSecretDtos = essService.getSecrets(TEST_TASK_ID_1).orElse(null);
        assertThat(newSecretDtos).isNotNull().containsAll(secrets);
    }

    @Test
    void shouldNotUpdateForTasksWithoutSecrets() {
        var tokenSupplier = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                            List.of(SCOPE_UPDATE_SECRETS),
                                                            100);
        var token = getToken(tokenSupplier);
        // Create a task without secrets in TEST_NCA_ID
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1,
                                   TEST_ICMS_REQ_ID_1, ResultHandlingStrategy.UPLOAD);

        // Check there are no secrets
        var secretDtos = essService.getSecrets(TEST_TASK_ID_1).orElse(null);
        assertThat(secretDtos).isNull();

        // Update the task with new secrets
        var secrets = Set.of(SecretDto.builder().name("test").value(new StringNode("value1")).build());
        var updateRequestBody = UpdateSecretsRequest.builder()
                .secrets(secrets)
                .build();
        var url = URI.create("/v1/nvct/secrets/tasks/" + TEST_TASK_ID_1);
        var updateRequestEntity = RequestEntity.put(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(updateRequestBody);
        var updateResponseEntity = testRestTemplate.exchange(updateRequestEntity, Void.class);
        assertThat(updateResponseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    Stream<Arguments> updateSecretTerminalStatusArgs() {
        return Stream.of(
                Arguments.of(ERRORED),
                Arguments.of(CANCELED),
                Arguments.of(COMPLETED),
                Arguments.of(EXCEEDED_MAX_QUEUED_DURATION),
                Arguments.of(EXCEEDED_MAX_RUNTIME_DURATION)
                        );
    }

    @ParameterizedTest
    @MethodSource("updateSecretTerminalStatusArgs")
    void shouldNotUpdateForTasksWithTerminalStatus(TaskStatus taskStatus) {
        var tokenSupplier = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                            List.of(SCOPE_UPDATE_SECRETS),
                                                            100);
        var token = getToken(tokenSupplier);
        // Create a task with secrets in TEST_NCA_ID and status set to terminal
        testTaskService.createTask(TEST_NCA_ID,
                                   TEST_TASK_ID_1,
                                   TEST_ICMS_REQ_ID_1,
                                   taskStatus);
        essService.saveSecrets(TEST_TASK_ID_1,
                               Set.of(SecretDto.builder()
                                              .name("test")
                                              .value(new StringNode("value1"))
                                              .build()));
        
        // Update task to indicate it has secrets
        var taskEntity = taskService.fetchTask(TEST_TASK_ID_1);
        taskEntity.setHasSecrets(true);
        taskService.updateTask(taskEntity);

        // Update the task with new secrets
        var secrets = Set.of(SecretDto.builder().name("test").value(new StringNode("value1")).build());
        var updateRequestBody = UpdateSecretsRequest.builder()
                .secrets(secrets)
                .build();
        var url = URI.create("/v1/nvct/secrets/tasks/" + TEST_TASK_ID_1);
        var updateRequestEntity = RequestEntity.put(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(updateRequestBody);
        var updateResponseEntity = testRestTemplate.exchange(updateRequestEntity, Void.class);
        assertThat(updateResponseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

}
