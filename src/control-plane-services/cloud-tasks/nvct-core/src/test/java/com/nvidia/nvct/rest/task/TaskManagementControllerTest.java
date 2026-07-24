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

import static com.nvidia.boot.registries.service.registry.client.ngc.NgcRegistryUtils.removeArtifactHostName;
import static com.nvidia.nvct.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.CANCELED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.COMPLETED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.ERRORED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.QUEUED;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAGS_COUNT;
import static com.nvidia.nvct.util.NvctConstants.MAX_TAG_LENGTH;
import static com.nvidia.nvct.util.NvctConstants.NGC_API_KEY;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_CANCEL_TASK;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_DELETE_TASK;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LAUNCH_TASK;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_TASK_DETAILS;
import static com.nvidia.nvct.util.TestConstants.BASE_ARTIFACT_URL;
import static com.nvidia.nvct.util.TestConstants.GFN;
import static com.nvidia.nvct.util.TestConstants.L40G;
import static com.nvidia.nvct.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_2;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_3;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_4;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_MISSING_PROTOCOL_1;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_NOT_EXISTS_1;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_NOT_SUPPORTED_REGISTRY_1;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_PERMISSION_DENIED_REGISTRY_1;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_UNKNOWN_REGISTRY_1;
import static com.nvidia.nvct.util.TestConstants.MODEL_ARTIFACTS_URL_WITH_CANARY_HOST;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_2;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_3;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_4;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_MISSING_PROTOCOL_1;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_NOT_EXISTS_1;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_NOT_SUPPORTED_REGISTRY_1;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_PERMISSION_DENIED_REGISTRY_1;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_UNKNOWN_REGISTRY_1;
import static com.nvidia.nvct.util.TestConstants.RESOURCE_ARTIFACTS_URL_WITH_CANARY_HOST_1;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_5;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ENVIRONMENT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE_NOT_SUPPORTED;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE_UNKNOWN_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE_WITHOUT_TAG;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE_WITH_CANARY_HOST;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE_WITH_INVALID_TAG;
import static com.nvidia.nvct.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvct.util.TestConstants.TEST_MODELS;
import static com.nvidia.nvct.util.TestConstants.TEST_MODEL_NAME;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OCI_GPU_SPEC_DTO;
import static com.nvidia.nvct.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_RESOURCES;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_NAME_3;
import static com.nvidia.nvct.util.TestConstants.TEST_UNKNOWN_ORG_NAME;
import static com.nvidia.nvct.util.TestConstants.TEST_UNKNOWN_TEAM_NAME;
import static com.nvidia.nvct.util.TestConstants.TEST_VALID_ORG_NAME;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.fail;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.task.dto.ArtifactDto;
import com.nvidia.nvct.rest.task.dto.ContainerEnvironmentEntryDto;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.GpuSpecificationDto;
import com.nvidia.nvct.rest.task.dto.ListTasksResponse;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.rest.task.dto.TaskDto;
import com.nvidia.nvct.rest.task.dto.TaskResponse;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvct.service.apikeys.ApiKeysService;
import com.nvidia.nvct.service.task.TaskMapperService;
import com.nvidia.nvct.util.MockApiKeysServer;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.TestUtil;
import java.net.URI;
import java.net.URL;
import java.time.Duration;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.function.Supplier;
import java.util.stream.Collectors;
import java.util.stream.IntStream;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Disabled;
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
class TaskManagementControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private AccountService accountService;

    @Autowired
    private ApiKeysService apiKeysService;

    @Autowired
    private TasksRepository tasksRepository;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private TaskMapperService taskMapperService;

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

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvct.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryHostname;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockNvcfServer.start(nvcfBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryHostname);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
    }

    @AfterAll
    void cleanup() {
        MockApiKeysServer.stop();
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockEssServer.stop();
        MockNotaryServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        MockApiKeysServer.resetToDefault();
        // use of MockApiKeysServer with different scopes causes the api-keys cache to dirty
        apiKeysService.invalidateCache();
        accountService.invalidateCache();
        testTaskService.clearAll();
    }


    Stream<Arguments> createTaskArgs() {
        var validGfnSpec = GpuSpecificationDto.builder()
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                .clusters(Set.of("cluster01", "cluster02")).build();
        var specWithMissingInstanceType = GpuSpecificationDto.builder()
                .gpu(L40G).backend(GFN).clusters(Set.of("cluster01", "cluster02")).build();
        var specWithInvalidInstanceType = GpuSpecificationDto.builder()
                .gpu(L40G).backend(GFN).instanceType("invalid-instance-type").build();
        var specWithMissingBackend = GpuSpecificationDto.builder()
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).build();
        var specWithClusters = GpuSpecificationDto.builder()
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                .clusters(Set.of("cluster01", "cluster02")).build();
        var specWithMissingGpu = GpuSpecificationDto.builder()
                .backend(GFN).instanceType(L40G_INSTANCE_TYPE).build();
        var specWithInvalidGpu = GpuSpecificationDto.builder()
                .gpu("invalid-gpu").backend(GFN).instanceType(L40G_INSTANCE_TYPE).build();
        var specWithNonEmptyConfiguration =
                GpuSpecificationDto.builder().gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .clusters(Set.of("cluster01", "cluster02"))
                        .configuration(jsonMapper.createObjectNode().put("foo", "bar"))
                        .build();
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        var secrets = Set.of(SecretDto.builder()
                                     .name(NGC_API_KEY)
                                     .value(new StringNode("shhh!shhh!"))
                                     .build(),
                             SecretDto.builder()
                                     .name("secret2")
                                     .value(secretJsonNodeValue)
                                     .build());
        var containerEnvKeyWithHyphen =
                List.of(ContainerEnvironmentEntryDto.builder().key("KEY-1").value("VALUE_1")
                                .build(),
                        ContainerEnvironmentEntryDto.builder().key("KEY_2").value("VALUE_2")
                                .build());
        var containerEnvKeyWithSpace =
                List.of(ContainerEnvironmentEntryDto.builder().key("KEY 1").value("VALUE_1")
                                .build(),
                        ContainerEnvironmentEntryDto.builder().key("KEY_2").value("VALUE_2")
                                .build());
        var containerEnvKeyWithDollar =
                List.of(ContainerEnvironmentEntryDto.builder().key("KEY$1").value("VALUE_1")
                                .build(),
                        ContainerEnvironmentEntryDto.builder().key("KEY_2").value("VALUE_2")
                                .build());


        var jwtCases = Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // URIs containing team name.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_2,
                             RESOURCE_ARTIFACTS_URL_2,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // non-fully qualified model/resource URLS
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             removeArtifactHostName(MODEL_ARTIFACTS_URL_3),
                             removeArtifactHostName(RESOURCE_ARTIFACTS_URL_3),
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Model URI containing version name.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_4,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Model artifact with canary hostname
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_WITH_CANARY_HOST,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Model artifact with unknown registry
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_UNKNOWN_REGISTRY_1,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Model artifact url without protocol
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_MISSING_PROTOCOL_1,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Model artifact with not supported registry
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_NOT_SUPPORTED_REGISTRY_1,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Model artifact with invalid token
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_PERMISSION_DENIED_REGISTRY_1,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.FORBIDDEN),
                // Model artifact not exists
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_NOT_EXISTS_1,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.NOT_FOUND),
                // Resource URI containing version name.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_4,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Resource artifact with canary registry
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_WITH_CANARY_HOST_1,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Resource artifact with unknown registry
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_UNKNOWN_REGISTRY_1,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Resource artifact url without protocol
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_MISSING_PROTOCOL_1,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Resource artifact with not supported registry
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_NOT_SUPPORTED_REGISTRY_1,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Resource artifact with invalid token
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_PERMISSION_DENIED_REGISTRY_1,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.FORBIDDEN),
                // Resource artifact not exists
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_NOT_EXISTS_1,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.NOT_FOUND),
                // Optional model.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             null,
                             RESOURCE_ARTIFACTS_URL_2,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Optional resource.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             null,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Model URI does not end with "/files".
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             BASE_ARTIFACT_URL + "/v2/org/ajwc672qsbdd/models/svc/bis-test:v1",
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Null GpuSpecificationDto.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             null,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Missing instanceType in GpuSpec.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             specWithMissingInstanceType,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid instanceType in GpuSpec.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             specWithInvalidInstanceType,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Missing gpu in GpuSpec.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             specWithMissingGpu,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid gpu in GpuSpec.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             specWithInvalidGpu,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Missing backend in GpuSpec.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             specWithMissingBackend,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Clusters only in GpuSpec.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             specWithClusters,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Unknown client trying to create a task in an unknown account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-unknown-client",
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             null,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.NOT_FOUND),
                // Missing scope.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(), 100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.FORBIDDEN),
                // Missing token.
                Arguments.of(null,
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.UNAUTHORIZED),
                // Too many tags (limit=64).
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             IntStream.range(0, MAX_TAGS_COUNT + 1).mapToObj(i -> "tag" + i)
                                     .collect(Collectors.toSet()),
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Too long tags (limit=128).
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             Set.of(StringUtils.repeat("tag1", MAX_TAG_LENGTH),
                                    StringUtils.repeat("tag2", MAX_TAG_LENGTH)),
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid tag character.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             Set.of("\n&abc123["),
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Null maxRuntimeDuration with non-GFN backend.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             null,
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Null maxRuntimeDuration with GFN backend.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             null,
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Greater than 8hours of maxRuntimeDuration with GFN backend.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             Duration.ofHours(9),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Zero maxRuntimeDuration
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             Duration.ZERO,
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Zero maxQueuedDuration
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             Duration.ofHours(4),
                             Duration.ZERO,
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Zero terminationGacePeriodDuration
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             Duration.ofHours(4),
                             Duration.ofHours(3),
                             Duration.ZERO,
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Null maxQueuedDuration.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             Duration.ofHours(2),
                             null,
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Null terminationGracePeriodDuration.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             null,
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // terminationGracePeriodDuration greater than maxRuntimeDuration.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(4),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Default terminationGracePeriodDuration greater than maxRuntimeDuration.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             validGfnSpec,
                             Duration.ofMinutes(30), // 30minutes
                             Duration.ofHours(3),
                             null,                   // Default terminationGracePeriodDuration 1h
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Null secrets  with resultHandlingStrategy UPLOAD
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             null,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // No NGC_API_KEY in secrets with resultHandlingStrategy UPLOAD
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             Set.of(SecretDto.builder()
                                            .name("secret-key")
                                            .value(new StringNode("shhh!shhh!"))
                                            .build()),
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid resultsLocation with just org name and resultsHandlingStrategy UPLOAD
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             "orgA",
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid resultsLocation with just trailing slash after org name.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             "orgA/",
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid resultsLocation with trailing slash after team name.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             "orgA/teamB/",
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid resultsLocation with trailing slash after model name.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             "orgA/teamB/modelC/",
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid resultsLocation with more than org, team, and model names in the path.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             "orgA/teamB/modelC/extraD",
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Invalid/unknown org name in resultsLocation.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_UNKNOWN_ORG_NAME + "/" + TEST_MODEL_NAME,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.NOT_FOUND),
                // Invalid/unknown team name in resultsLocation.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_VALID_ORG_NAME + "/" + TEST_UNKNOWN_TEAM_NAME + "/" +
                                     TEST_MODEL_NAME,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.NOT_FOUND),
                // Invalid resultsLocation with just org name and resultsHandlingStrategy NONE.
                // resultsLocation is not validated if resultHandlingStrategy is NONE.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.NONE,
                             secrets,
                             "orgA",
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Blank resultsLocation with resultsHandlingStrategy NONE.
                // resultsLocation is not validated if resultHandlingStrategy is NONE.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.NONE,
                             secrets,
                             null,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // No NGC_API_KEY in secrets with resultHandlingStrategy NONE. Presence of
                // NGC_API_KEY in the secrets is not validated if resultHandlingStrategy is NONE.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.NONE,
                             Set.of(SecretDto.builder()
                                            .name("secret-key")
                                            .value(new StringNode("shhh!shhh!"))
                                            .build()),
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Null secrets with resultHandlingStrategy NONE. Presence of NGC_API_KEY in
                // the secrets is not validated if resultHandlingStrategy is NONE.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.NONE,
                             null,
                             TEST_RESULTS_LOCATION_1,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // Environment key with hyphen.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             containerEnvKeyWithHyphen,
                             HttpStatus.BAD_REQUEST),
                // Environment key with space.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             containerEnvKeyWithSpace,
                             HttpStatus.BAD_REQUEST),
                // Environment key with dollar.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             containerEnvKeyWithDollar,
                             HttpStatus.BAD_REQUEST),
                // bad task tags
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             Set.of("\n&abc123["),
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                // Tags with special namespace:key=value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             Set.of("namespace:key=value"),
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE_WITH_CANARY_HOST,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE_UNKNOWN_REGISTRY,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE_WITHOUT_TAG,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE_WITH_DIGEST,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE_NOT_EXISTS,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE_WITH_INVALID_TAG,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE_PERMISSION_DENIED,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE_NOT_SUPPORTED,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                             100),
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             specWithNonEmptyConfiguration,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.BAD_REQUEST));

        var apiKeysCases = Stream.of(
                // api-key with launch_task scope and account-tasks resource
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("account-tasks", "*")),
                                                               List.of("launch_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.OK),
                // api-key with scope but no resources
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(),
                                                               List.of("launch_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.FORBIDDEN),
                // api-key with missing scope
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(), List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CONTAINER_IMAGE,
                             MODEL_ARTIFACTS_URL_3,
                             RESOURCE_ARTIFACTS_URL_3,
                             TEST_TAGS,
                             TEST_OCI_GPU_SPEC_DTO,
                             Duration.ofHours(2),
                             Duration.ofHours(3),
                             Duration.ofHours(1),
                             ResultHandlingStrategyEnum.UPLOAD,
                             secrets,
                             TEST_RESULTS_LOCATION_2,
                             TEST_CONTAINER_ENVIRONMENT,
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeysCases);
    }

    @ParameterizedTest
    @MethodSource("createTaskArgs")
    void shouldCreateTask(Object tokenSupplier,
                          URI containerImage,
                          String modelUri,
                          String resourceUri,
                          Set<String> tags,
                          GpuSpecificationDto gpuSpecificationDto,
                          Duration maxRuntimeDuration,
                          Duration maxQueuedDuration,
                          Duration terminationGracePeriodDuration,
                          ResultHandlingStrategyEnum resultHandlingStrategy,
                          Set<SecretDto> secrets,
                          String resultsLocation,
                          List<ContainerEnvironmentEntryDto> containerEnvironment,
                          HttpStatus expectedStatus) {
        var requestBodyBuilder = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(containerImage)
                .tags(tags)
                .gpuSpecification(gpuSpecificationDto)
                .maxRuntimeDuration(maxRuntimeDuration)
                .maxQueuedDuration(maxQueuedDuration)
                .terminationGracePeriodDuration(terminationGracePeriodDuration)
                .description(TEST_DESCRIPTION)
                .resultsLocation(resultsLocation)
                .resultHandlingStrategy(resultHandlingStrategy)
                .secrets(secrets)
                .containerEnvironment(containerEnvironment);

        if (StringUtils.isNotBlank(modelUri)) {
            requestBodyBuilder.models(Set.of(
                    ArtifactDto.builder().name("model-1")
                            .version("1.0")
                            .uri(URI.create(modelUri))
                            .build()));
        }

        if (StringUtils.isNotBlank(resourceUri)) {
            requestBodyBuilder.resources(Set.of(
                    ArtifactDto.builder().name("resource-1")
                            .version("1.0")
                            .uri(URI.create(resourceUri))
                            .build()));
        }
        var token = TestUtil.getToken(tokenSupplier);
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
        assertThat(responseBody.task().containerArgs()).isEqualTo(TEST_CONTAINER_ARGS);
        assertThat(responseBody.task().helmChart()).isNull();
        assertThat(responseBody.task().containerImage()).isEqualTo(containerImage);
        assertThat(responseBody.task().containerEnvironment()).isEqualTo(containerEnvironment);
        assertThat(responseBody.task().createdAt()).isNotNull();
        assertThat(responseBody.task().gpuSpecification()).isEqualTo(gpuSpecificationDto);
        assertThat(responseBody.task().telemetries()).isNull();

        if (StringUtils.isNotBlank(resultsLocation)) {
            assertThat(responseBody.task().resultsLocation()).isEqualTo(resultsLocation);
        } else {
            assertThat(responseBody.task().resultsLocation()).isBlank();
        }

        if (resultHandlingStrategy != null) {
            assertThat(responseBody.task().resultHandlingStrategy())
                    .isEqualTo(resultHandlingStrategy);
        } else {
            assertThat(responseBody.task().resultHandlingStrategy()).isNull();
        }

        if (maxRuntimeDuration != null) {
            assertThat(responseBody.task().maxRuntimeDuration()).isNotNull();
            assertThat(responseBody.task().maxRuntimeDuration().toString())
                    .hasToString(maxRuntimeDuration.toString());
        } else {
            assertThat(responseBody.task().maxRuntimeDuration()).isNull();
        }
        assertThat(responseBody.task().maxQueuedDuration()).isNotNull();
        if (maxQueuedDuration != null) {
            assertThat(responseBody.task().maxQueuedDuration().toString())
                    .hasToString(maxQueuedDuration.toString());
        } else {
            assertThat(responseBody.task().maxQueuedDuration().toString())
                    .hasToString("PT72H"); // Default value.
        }
        assertThat(responseBody.task().terminationGracePeriodDuration()).isNotNull();
        if (terminationGracePeriodDuration != null) {
            assertThat(responseBody.task().terminationGracePeriodDuration().toString())
                    .hasToString(terminationGracePeriodDuration.toString());
        } else {
            assertThat(responseBody.task().terminationGracePeriodDuration().toString())
                    .hasToString("PT1H");  // Default value
        }

        if (StringUtils.isNotBlank(modelUri)) {
            assertThat(responseBody.task().models()).isNotNull().hasSize(1);
            var model = responseBody.task().models().stream().findFirst().get();
            if (modelUri.startsWith("http")) {
                assertThat(model.getUri()).isEqualTo(URI.create(modelUri));
            } else {
                // Make sure it already be transformed to fully-qualified url.
                // Since we are reading the artifact hostname from
                assertThat(model.getUri()).isEqualTo(URI.create("https://localhost-ngc" + modelUri));
            }
        } else {
            assertThat(responseBody.task().models()).isNull();
        }
        if (StringUtils.isNotBlank(resourceUri)) {
            assertThat(responseBody.task().resources()).isNotNull().hasSize(1);
            var resource = responseBody.task().resources().stream().findFirst().get();
            if (resourceUri.startsWith("http")) {
                assertThat(resource.getUri()).isEqualTo(URI.create(resourceUri));
            } else {
                // Make sure it already be transformed to fully-qualified url.
                assertThat(resource.getUri()).isEqualTo(
                        URI.create("https://localhost-ngc" + resourceUri));
            }
        } else {
            assertThat(responseBody.task().resources()).isNull();
        }

        assertThat(responseBody.task().tags()).isEqualTo(tags);
        assertThat(responseBody.task().description()).isEqualTo(TEST_DESCRIPTION);

        var taskId = responseBody.task().id();
        var entity = tasksRepository
                .getByTaskId(taskId)
                .orElseThrow(() -> new NotFoundException("Task not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getHealth()).isBlank();
    }

    @Test
    void shouldThrowExceptionForTooLongDescription() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK),
                                                    100);
        var requestBody = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .tags(TEST_TAGS)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(3))
                .terminationGracePeriodDuration(Duration.ofHours(1))
                // Create a description that exceeds MAX_DESCRIPTION_LENGTH (256)
                .description(StringUtils.repeat("a",  257))
                .resultHandlingStrategy(ResultHandlingStrategyEnum.NONE)
                .containerEnvironment(TEST_CONTAINER_ENVIRONMENT)
                .build();

        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, TaskResponse.class);
        
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    @Disabled
    void shouldThrowTooManyTasksException() {
        // Create 1st Task in TEST_NCA_ID_WITH_1_MAX_ALLOWED_TASKS_5 account that is
        // associated with client TEST_CLIENT_ID_5. TEST_NCA_ID_WITH_1_MAX_ALLOWED_TASKS_5 is
        // set up with maxTasksAllowed = 1.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_5,
                                                    List.of(SCOPE_LAUNCH_TASK),
                                                    100);
        var requestBodyBuilder = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .tags(TEST_TAGS)
                .gpuSpecification(GpuSpecificationDto.builder()
                                          .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                                          .clusters(Set.of("cluster01", "cluster02")).build())
                .maxRuntimeDuration(Duration.ofHours(2))
                .maxQueuedDuration(Duration.ofHours(2))
                .terminationGracePeriodDuration(Duration.ofHours(2))
                .description(TEST_DESCRIPTION)
                .resultHandlingStrategy(ResultHandlingStrategyEnum.NONE)
                .containerEnvironment(TEST_CONTAINER_ENVIRONMENT);
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBodyBuilder.build());
        var responseEntity =
                testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Create 2nd Task, expecting BAD_REQUEST.
        var secondRequestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBodyBuilder.build());
        responseEntity = testRestTemplate.exchange(secondRequestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    Stream<Arguments> listTasksArgs() {
        return Stream.of(
                // List tasks in TEST_NCA_ID account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_LIST_TASKS),
                                                             100),
                             List.of(TEST_TASK_ID_1, TEST_TASK_ID_2),
                             HttpStatus.OK),
                // List non-existent tasks in TEST_NCA_ID_2 account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_2,
                                                             List.of(SCOPE_LIST_TASKS),
                                                             100),
                             List.of(),
                             HttpStatus.OK),
                // Unknown client listing tasks in an unknown account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("unknown-client-id",
                                                             List.of(SCOPE_LIST_TASKS),
                                                             100),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                // Missing scope.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID, List.of(), 100),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                // Missing token.
                Arguments.of(null,
                             List.of(),
                             HttpStatus.UNAUTHORIZED)
                        );
    }

    @ParameterizedTest
    @MethodSource("listTasksArgs")
    void shouldListTasks(String token, List<UUID> expectedTasks, HttpStatus expectedStatus) {
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID.
        var task1 = TestUtil.createTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                              jsonMapper);
        var task2 = TestUtil.createTaskEntity(TEST_TASK_ID_2, TEST_NCA_ID, TEST_TASK_NAME_2,
                                              jsonMapper);

        // Save tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);

        var requestEntity =
                RequestEntity.get(URI.create("/v1/nvct/tasks"))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListTasksResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.cursor()).isNull();
        assertThat(responseBody.limit()).isNull();
        assertThat(expectedTasks).isNotNull();
        assertThat(responseBody.tasks()).hasSize(expectedTasks.size());
        var tasks = responseBody.tasks().stream()
                .collect(Collectors.toMap(TaskDto::id, Function.identity()));
        assertThat(tasks.keySet()).isEqualTo(new HashSet<>(expectedTasks));

        verifyReturnedTasksIntegrity(expectedTasks, QUEUED, tasks);
    }

    void verifyReturnedTasksIntegrity(
            List<UUID> expectedTasks,
            TaskStatus expectedStatus,
            Map<UUID, TaskDto> tasks) {
        for (UUID expectedTaskId : expectedTasks) {
            var taskDto = tasks.get(expectedTaskId);
            assertThat(taskDto).isNotNull();
            assertThat(taskDto.createdAt()).isNotNull();
            assertThat(taskDto.status()).isEqualTo(
                    TaskStatusEnum.fromText(expectedStatus.toString()));
            assertThat(taskDto.containerImage()).isNotNull();
            assertThat(taskDto.containerArgs()).isNotBlank();
            assertThat(taskDto.containerEnvironment()).hasSize(3);
            if (taskDto.id().equals(TEST_TASK_ID_1)) {
                assertThat(taskDto.name()).isEqualTo(TEST_TASK_NAME_1);
            } else if (taskDto.id().equals(TEST_TASK_ID_2)) {
                assertThat(taskDto.name()).isEqualTo(TEST_TASK_NAME_2);
            } else if (taskDto.id().equals(TEST_TASK_ID_3)) {
                assertThat(taskDto.name()).isEqualTo(TEST_TASK_NAME_3);
            } else {
                fail("unknown function returned");
            }
            assertThat(taskDto.description()).isNotEmpty();
            assertThat(taskDto.tags()).isNotEmpty();
            assertThat(taskDto.gpuSpecification()).isNotNull();
            assertThat(taskDto.models())
                    .isEqualTo(taskMapperService.getModelDtos(TEST_MODELS).get());
            assertThat(taskDto.resources())
                    .isEqualTo(taskMapperService.getResourceDtos(TEST_RESOURCES).get());
        }
    }

    Stream<Arguments> taskArgs() {
        var jwtCases = Stream.of(
                // TEST_TASK_ID_1 belongs to NCA ID tied to TEST_CLIENT_ID.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             HttpStatus.OK),
                // TEST_TASK_ID_3 does not exist.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_3,
                             HttpStatus.NOT_FOUND),
                // TEST_TASK_ID_1 does not belong to the NCA ID tied to TEST_CLIENT_ID_2.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_2,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             HttpStatus.NOT_FOUND),
                // Unknown subject/client.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("unknown-client-id",
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             HttpStatus.NOT_FOUND),
                // Missing scope.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID, List.of(), 100),
                             TEST_TASK_ID_1,
                             HttpStatus.FORBIDDEN),
                // No token.
                Arguments.of(null,
                             TEST_TASK_ID_1,
                             HttpStatus.UNAUTHORIZED)
        );

        var apiKeysCases = Stream.of(
                // api-key with task_details scope and account-tasks resource
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("account-tasks", "*")),
                                                               List.of("task_details"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.OK),
                // api-key with task_details scope and specific task resource (matching)
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_1.toString())),
                                                               List.of("task_details"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.OK),
                // api-key with task_details scope and specific task resource (not matching)
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_2.toString())),
                                                               List.of("task_details"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.FORBIDDEN),
                // api-key with scope but no resources
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(),
                                                               List.of("task_details"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.FORBIDDEN),
                // api-key with missing scope
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(), List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeysCases);
    }

    @ParameterizedTest
    @MethodSource("taskArgs")
    void shouldGetTaskDetails(Object tokenSupplier, UUID taskId, HttpStatus expectedStatus) {
        var token = TestUtil.getToken(tokenSupplier);
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID.
        testTaskService.createTaskWithModelAndResources(TEST_NCA_ID, TEST_TASK_ID_1,
                                                        UUID.randomUUID());
        testTaskService.createTaskWithModelAndResources(TEST_NCA_ID, TEST_TASK_ID_2,
                                                        UUID.randomUUID());

        var requestEntity =
                RequestEntity.get(URI.create("/v1/nvct/tasks/" + taskId))
                        .accept(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.task()).isNotNull();

        var taskDto = responseBody.task();
        assertThat(taskDto).isNotNull();
        assertThat(taskDto.createdAt()).isNotNull();
        assertThat(taskDto.status()).isEqualTo(TaskStatusEnum.QUEUED);
        assertThat(taskDto.containerImage()).isNotNull();
        assertThat(taskDto.containerArgs()).isNotBlank();
        assertThat(taskDto.containerEnvironment()).hasSize(3);
        assertThat(taskDto.name()).isEqualTo("Task-" + taskDto.id());
        assertThat(taskDto.description()).isNotEmpty();
        assertThat(taskDto.tags()).isNotEmpty();
        assertThat(taskDto.gpuSpecification()).isNotNull();
        assertThat(taskDto.models()).isEqualTo(taskMapperService.getModelDtos(TEST_MODELS).get());
        assertThat(taskDto.resources())
                .isEqualTo(taskMapperService.getResourceDtos(TEST_RESOURCES).get());
        assertThat(taskDto.resultsLocation()).isNotEmpty();
        assertThat(taskDto.resultHandlingStrategy()).isEqualTo(ResultHandlingStrategyEnum.UPLOAD);
        assertThat(taskDto.instances()).isNotEmpty();
    }

    Stream<Arguments> cancelTaskArgs() {
        var jwtCases = Stream.of(
                // TEST_TASK_ID_1 belongs to NCA ID tied to TEST_CLIENT_ID.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.OK),
                // TEST_TASK_ID_3 does not exist.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_3,
                             QUEUED,
                             HttpStatus.NOT_FOUND),
                // TEST_TASK_ID_1 does not belong to the NCA ID tied to TEST_CLIENT_ID_2.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_2,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.NOT_FOUND),
                // Unknown subject/client.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("unknown-client-id",
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.NOT_FOUND),
                // Task status is CANCELED already
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             CANCELED,
                             HttpStatus.BAD_REQUEST),
                // Task status is ERRORED already
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             ERRORED,
                             HttpStatus.BAD_REQUEST),
                // Task status is COMPLETED already
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             COMPLETED,
                             HttpStatus.BAD_REQUEST),
                // Missing scope.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID, List.of(), 100),
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.FORBIDDEN),
                // No token.
                Arguments.of(null,
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.UNAUTHORIZED)
                        );

        var apiKeysCases = Stream.of(
                // api-key authorized to cancel any private tasks of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("account-tasks", "*")),
                                                               List.of("cancel_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.OK),
                // api-key authorized to cancel specific task of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_1.toString())),
                                                               List.of("cancel_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.OK),
                // api-key attempt to cancel a task that does not have a matching resource entry.
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_2.toString())),
                                                               List.of("cancel_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.FORBIDDEN),
                // api-key attempt to cancel with no resource entries in the policy result.
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(),
                                                               List.of("cancel_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             QUEUED,
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeysCases);
    }

    @ParameterizedTest
    @MethodSource("cancelTaskArgs")
    void shouldCancelTask(Object tokenSupplier, UUID taskId, TaskStatus status, HttpStatus expectedStatus) {
        var token = TestUtil.getToken(tokenSupplier);
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID.
        var task1 = TestUtil.createTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                              jsonMapper);
        var task2 = TestUtil.createTaskEntity(TEST_TASK_ID_2, TEST_NCA_ID, TEST_TASK_NAME_2,
                                              jsonMapper);
        task1.setStatus(status);
        task2.setStatus(status);

        // Save tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);

        var requestEntity =
                RequestEntity.post(URI.create("/v1/nvct/tasks/" + taskId + "/cancel"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.task()).isNotNull();

        var taskDto = responseBody.task();
        assertThat(taskDto).isNotNull();
        assertThat(taskDto.createdAt()).isNotNull();
        assertThat(taskDto.status()).isEqualTo(TaskStatusEnum.CANCELED);
        assertThat(taskDto.containerImage()).isNotNull();
        assertThat(taskDto.containerArgs()).isNotBlank();
        assertThat(taskDto.containerEnvironment()).hasSize(3);
        assertThat(taskDto.name()).isEqualTo(TEST_TASK_NAME_1);
        assertThat(taskDto.description()).isNotEmpty();
        assertThat(taskDto.tags()).isNotEmpty();
        assertThat(taskDto.gpuSpecification()).isNotNull();
        assertThat(taskDto.models()).isEqualTo(taskMapperService.getModelDtos(TEST_MODELS).get());
        assertThat(taskDto.resources())
                .isEqualTo(taskMapperService.getResourceDtos(TEST_RESOURCES).get());
    }

    Stream<Arguments> deleteTaskArgs() {
        var jwtCases = Stream.of(
                // TEST_TASK_ID_1 belongs to NCA ID tied to TEST_CLIENT_ID.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             HttpStatus.NO_CONTENT),
                // TEST_TASK_ID_3 does not exist.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_3,
                             HttpStatus.NOT_FOUND),
                // TEST_TASK_ID_1 does not belong to the NCA ID tied to TEST_CLIENT_ID_2.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID_2,
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             HttpStatus.NOT_FOUND),
                // Unknown subject/client.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("unknown-client-id",
                                                             List.of(SCOPE_TASK_DETAILS,
                                                          SCOPE_CANCEL_TASK,
                                                          SCOPE_DELETE_TASK),
                                                             100),
                             TEST_TASK_ID_1,
                             HttpStatus.NOT_FOUND),
                // Missing scope.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_ID, List.of(), 100),
                             TEST_TASK_ID_1,
                             HttpStatus.FORBIDDEN),
                // No token.
                Arguments.of(null,
                             TEST_TASK_ID_1,
                             HttpStatus.UNAUTHORIZED)
                        );

        var apiKeysCases = Stream.of(
                // api-key authorized to delete any private tasks of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("account-tasks", "*")),
                                                               List.of("delete_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.NO_CONTENT),
                // api-key authorized to delete specific task of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_1.toString())),
                                                               List.of("delete_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.NO_CONTENT),
                // api-key attempt to delete a task that does not have a matching resource entry.
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(new Resource("task", TEST_TASK_ID_2.toString())),
                                                               List.of("delete_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.FORBIDDEN),
                // api-key attempt to delete with no resource entries in the policy result.
                Arguments.of((Supplier<String>) () -> {
                                 MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                                               List.of(),
                                                               List.of("delete_task"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_TASK_ID_1,
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeysCases);
    }

    @ParameterizedTest
    @MethodSource("deleteTaskArgs")
    void shouldDeleteTask(Object tokenSupplier, UUID taskId, HttpStatus expectedStatus) {
        var token = TestUtil.getToken(tokenSupplier);
        // Create Tasks in TEST_NCA_ID account tied to TEST_CLIENT_ID.
        var task1 = TestUtil.createTaskEntity(TEST_TASK_ID_1, TEST_NCA_ID, TEST_TASK_NAME_1,
                                              jsonMapper);
        var task2 = TestUtil.createTaskEntity(TEST_TASK_ID_2, TEST_NCA_ID, TEST_TASK_NAME_2,
                                              jsonMapper);

        // Save tasks.
        tasksRepository.save(task1);
        tasksRepository.save(task2);

        var requestEntity =
                RequestEntity.delete(URI.create("/v1/nvct/tasks/" + taskId))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        var entity = tasksRepository.getByTaskId(taskId);
        if (expectedStatus.isError()) {
            if (responseEntity.getStatusCode() != HttpStatus.NOT_FOUND) {
                assertThat(entity).isNotEmpty();
            }
            return;
        }
        assertThat(entity).isEmpty();
    }

    @Test
    void shouldFailWhenGracePeriodIsGreaterThanMaxRuntimeDuration() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LAUNCH_TASK, SCOPE_LIST_TASKS),
                                                    100);
        var modelUri = URI.create(BASE_ARTIFACT_URL +
                                          "/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/files");
        var modelDtos = Set.of(ArtifactDto.builder().name("model-1")
                                       .version("1.0").uri(modelUri).build());
        var resourceUri = URI.create(RESOURCE_ARTIFACTS_URL_3);
        var resourceDtos = Set.of(ArtifactDto.builder().name("resource-1")
                                          .version("1.0")
                                          .uri(resourceUri)
                                          .build());
        var requestBodyBuilder = CreateTaskRequest.builder()
                .name(TEST_TASK_NAME_1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE)
                .models(modelDtos)
                .resources(resourceDtos)
                .tags(TEST_TAGS)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .gpuSpecification(TEST_OCI_GPU_SPEC_DTO)
                .maxRuntimeDuration(Duration.ofHours(3))
                .terminationGracePeriodDuration(Duration.ofHours(5))
                .description(TEST_DESCRIPTION);
        var requestEntity = RequestEntity.post(URI.create("/v1/nvct/tasks"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBodyBuilder.build());
        var responseEntity =
                testRestTemplate.exchange(requestEntity, TaskResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }
}
