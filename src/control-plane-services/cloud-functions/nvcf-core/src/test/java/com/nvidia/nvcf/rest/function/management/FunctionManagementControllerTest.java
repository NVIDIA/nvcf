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
package com.nvidia.nvcf.rest.function.management;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.function.management.dto.ApiBodyFormatEnum.PREDICT_V2;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_CONTAINER_ARGS_FOR_MODEL_ONLY_FUNCTIONS;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_PORT;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_TAGS_COUNT;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_TAG_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LIST_FUNCTIONS_DETAILS;
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_UPDATE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_IMAGE_NOT_SUPPORTED;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_URI;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_DTOS;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_END_WITH_ZIP_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_NOT_EXISTS_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_NOT_SUPPORTED_REGISTRY_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_PERMISSION_DENIED_REGISTRY_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_WITH_VERSIONS_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITHOUT_TAG;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITH_INVALID_TAG;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_END_WITH_ZIP_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_NOT_EXISTS_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_NOT_SUPPORTED_REGISTRY_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_PERMISSION_DENIED_REGISTRY_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.fail;

import com.datastax.oss.driver.api.core.CqlSession;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.persistence.function.entity.RateLimitUdt;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.account.dto.AccountDetailsResponse;
import com.nvidia.nvcf.rest.account.dto.AccountUpdateRequest;
import com.nvidia.nvcf.rest.function.management.dto.ArtifactDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.HealthDto;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionIdsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ProtocolEnum;
import com.nvidia.nvcf.rest.function.management.dto.RateLimitDto;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import jakarta.annotation.Nullable;
import java.net.URI;
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
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.CsvSource;
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
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class FunctionManagementControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestManagementService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    protected JsonMapper jsonMapper;

    @Autowired
    private CqlSession cqlSession;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;


    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        resetToDefault();
    }

    Stream<Arguments> functionCreateArgs() {
        var defaultCases = Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.OK),
                // Model URI contains "versions".
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_WITH_VERSIONS_1,
                             null,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),
                // Model URI does not end with "/files" or "/zip".
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             "https://api.stg.ngc.nvidia.com/v2/org/ajwc672qsbdd/models/svc/bis-test:v1",
                             null,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-guy",
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             null,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             null,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.FORBIDDEN),
                Arguments.of(null,
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             null,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-some-key",
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             null,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.FORBIDDEN),
                // too many tags (limit=64)
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             IntStream.range(0, MAX_TAGS_COUNT + 1).mapToObj(i -> "tag" + i)
                                     .collect(Collectors.toSet()),
                             HttpStatus.BAD_REQUEST),
                // too long tags (limit=128)
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             Set.of(StringUtils.repeat("tag1", MAX_TAG_LENGTH),
                                    StringUtils.repeat("tag2", MAX_TAG_LENGTH)),
                             HttpStatus.BAD_REQUEST),
                // weird tag characters
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             Set.of("\n&abc123["),
                             HttpStatus.BAD_REQUEST),
                // tags with special namespace:key=value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             Set.of("namespace:key=value"),
                             HttpStatus.OK),
                // empty health uri should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             "",
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                // incorrect health uri should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             "",
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             null,
                             TEST_TAGS,
                             HttpStatus.OK),
                // Model URI ends with "/zip"
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_END_WITH_ZIP_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                // Resource URI ends with "/zip"
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_END_WITH_ZIP_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST)
        );
        var validationCases = Stream.of(
                // Not exists Model URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_NOT_EXISTS_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.NOT_FOUND),
                // Permission denied Model URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_PERMISSION_DENIED_REGISTRY_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.FORBIDDEN),
                // Not supported registry model URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_NOT_SUPPORTED_REGISTRY_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                // Not exists Resource URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_NOT_EXISTS_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.NOT_FOUND),
                // Permission denied Resource URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_PERMISSION_DENIED_REGISTRY_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.FORBIDDEN),
                // Not supported registry Resource URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_NOT_SUPPORTED_REGISTRY_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                // Not exists Container Image URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE_NOT_EXISTS,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.NOT_FOUND),
                // Permission denied Container Image URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE_PERMISSION_DENIED,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.FORBIDDEN),
                // Not supported registry Container URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_CONTAINER_IMAGE_NOT_SUPPORTED,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                // Not supported registry Container URL
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE_WITH_INVALID_TAG,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                // Container URL with Digest
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.OK),
                // Container URL without tag
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE_WITHOUT_TAG,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.OK)
        );

        var apiKeyCases = Stream.of(
                // Authorized to create functions in TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_REGISTER_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.OK),
                // No resource entries in the policy result.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_REGISTER_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.FORBIDDEN),
                // Attempt to create a function when the resource entry in the policy result only
                // allows creating a version of TEST_FUNCTION_ID_2.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/" + "*")),
                                             List.of(SCOPE_REGISTER_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.FORBIDDEN),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.FORBIDDEN),
                // Incorrect scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.FORBIDDEN),
                Arguments.of("nvapi-stg-some-key",
                             TEST_NGC_CONTAINER_IMAGE,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.FORBIDDEN)
        );
        return Stream.concat(Stream.concat(defaultCases, validationCases), apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("functionCreateArgs")
    void shouldCreateFunction(
            Object tokenSupplier, URI containerImage, String modelUri,
            String resourceUri, String healthUri, Set<String> tags, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        var model1Name = "model-1";
        var model1Version = "1.0";
        var modelDtos = List.of(FunctionModelDto.builder().name(model1Name)
                                        .version(model1Version).uri(URI.create(modelUri)).build());
        HealthDto healthDto = null;
        if (healthUri != null) {
            healthDto = HealthDto.builder()
                    .expectedStatusCode(EXPECTED_STATUS_CODE)
                    .timeout(HEALTH_TIMEOUT)
                    .port(TEST_INFERENCE_PORT)
                    .protocol(ProtocolEnum.HTTP)
                    .uri(URI.create(healthUri))
                    .build();
        }
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(containerImage)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(modelDtos)
                .tags(tags)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();
        Set<ArtifactDto> resourceDtos = null;
        var resource1Name = "resource-1";
        var resource1Version = "1.0";
        if (resourceUri != null) {
            resourceDtos = Set.of(
                    ArtifactDto.builder().name(resource1Name)
                            .version(resource1Version)
                            .uri(URI.create(resourceUri))
                            .build());
            requestBody.setResources(resourceDtos);
        }
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().versionId()).isNotNull();
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().apiBodyFormat()).isEqualTo(PREDICT_V2);
        assertThat(responseBody.function().containerArgs()).isEqualTo(TEST_CONTAINER_ARGS);
        assertThat(responseBody.function().containerImage()).isEqualTo(containerImage);
        assertThat(responseBody.function().containerEnvironment()).isNull();
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);
        assertThat(responseBody.function().models()).isNotNull().hasSize(1);
        assertThat(responseBody.function().models()).containsExactlyInAnyOrderElementsOf(modelDtos);
        if (resourceUri != null) {
            assertThat(responseBody.function().resources()).isNotNull().hasSize(1);
            assertThat(responseBody.function().resources()).isEqualTo(resourceDtos);
        }
        assertThat(responseBody.function().tags()).isEqualTo(tags);
        assertThat(responseBody.function().description()).isEqualTo(TEST_DESCRIPTION);
        assertThat(responseBody.function().health()).isNotNull();
        if (healthUri != null) {
            assertThat(responseBody.function().health().getProtocol()).isEqualTo(
                    ProtocolEnum.HTTP);
            assertThat(responseBody.function().healthUri()).isEqualTo(TEST_HEALTH_URI);
            assertThat(responseBody.function().health().getUri()).isEqualTo(TEST_HEALTH_URI);
            assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
            assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                    EXPECTED_STATUS_CODE);
        } else {
            assertThat(responseBody.function().health().getProtocol()).isEqualTo(
                    ProtocolEnum.HTTP);
            assertThat(responseBody.function().healthUri()).isEqualTo(DEFAULT_HEALTH_ENDPOINT);
            assertThat(responseBody.function().health().getUri()).isEqualTo(
                    DEFAULT_HEALTH_ENDPOINT);
            assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
            assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                    EXPECTED_STATUS_CODE);
        }

        var functionId = responseBody.function().id();
        var versionId = responseBody.function().versionId();
        var entity = functionsRepository.getByFunctionVersionId(versionId)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getFunctionId()).isEqualTo(functionId);
        assertThat(entity.getUtilsContainerImage())
                .isNotBlank()
                .isEqualTo(GO);
        assertThat(entity.getModelSpecs()).hasSize(1);
        assertThat(entity.getModelSpecs()).containsKey(model1Name);
        assertThat(entity.getModelSpecs().get(model1Name))
                .contains("\"version\":\"" + model1Version + "\"")
                .contains("\"url\":\"" + modelUri + "\"");
        if (resourceUri != null) {
            assertThat(entity.getResources()).hasSize(1);
            assertThat(entity.getResources()).contains(
                    ResourceUdt.builder()
                            .url(resourceUri)
                            .name(resource1Name)
                            .version(resource1Version)
                            .build()
            );
        }
        assertThat(entity.getFunctionType()).isEqualTo(FunctionType.DEFAULT);
    }

    Stream<Arguments> healthDtoVariants() {
        return Stream.of(
                // all default values
                Arguments.of(TEST_HEALTH_ENDPOINT, DEFAULT_HEALTH_EXPECTED_STATUS_CODE,
                             DEFAULT_HEALTH_TIMEOUT, DEFAULT_HEALTH_PORT, ProtocolEnum.HTTP),
                // custom
                Arguments.of("/health", 0, Duration.ofSeconds(1), 8080,
                             ProtocolEnum.GRPC),
                // nulls
                Arguments.of(null, 0, null, 8080, null)
        );
    }

    @ParameterizedTest
    @MethodSource("healthDtoVariants")
    void testCreateFunctionVariousHealthParams(
            String healthUri, int expectedStatusCode,
            Duration healthTimeout, int inferencePort,
            ProtocolEnum protocolEnum) {
        HealthDto healthDto = null;
        if (healthUri != null) {
            healthDto = HealthDto.builder()
                    .expectedStatusCode(expectedStatusCode)
                    .timeout(healthTimeout)
                    .port(inferencePort)
                    .protocol(protocolEnum)
                    .uri(URI.create(healthUri))
                    .build();
        }
        var modelDtos =
                List.of(FunctionModelDto.builder()
                                .name("model-1")
                                .version("1.0")
                                .uri(URI.create(TEST_MODEL_URL_1))
                                .build());
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(modelDtos)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();
        requestBody.setResources(Set.of(
                ArtifactDto.builder().name("resource-1")
                        .version("1.0")
                        .uri(URI.create(
                                TEST_RESOURCE_URL_1))
                        .build()));
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS),
                                                    100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        var responseBody = responseEntity.getBody();

        assertThat(responseBody).isNotNull();
        if (healthUri != null) {
            assertThat(responseBody.function().health().getProtocol()).isEqualTo(
                    protocolEnum);
            assertThat(responseBody.function().health().getUri()).isEqualTo(
                    URI.create(healthUri));
            assertThat(responseBody.function().health().getTimeout()).isEqualTo(healthTimeout);
            assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                    expectedStatusCode);
        } else {
            assertThat(responseBody.function().health().getProtocol()).isEqualTo(
                    ProtocolEnum.HTTP);
            assertThat(responseBody.function().health().getUri()).isEqualTo(
                    DEFAULT_HEALTH_ENDPOINT);
            assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
            assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                    EXPECTED_STATUS_CODE);
        }
    }

    @Test
    void shouldGetBadRequestOnInvalidUri() throws JacksonException {
        var validUri = "valid_uri";
        var invalidUri = "invalid uri";
        var modelDtos = List.of(
                FunctionModelDto.builder().name("model-1")
                        .version("1.0")
                        .uri(URI.create(TEST_MODEL_URL_1))
                        .build());
        var healthDto = HealthDto.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(ProtocolEnum.HTTP)
                .uri(URI.create(validUri))
                .build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(modelDtos)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS),
                                                    100);

        var stringBody = new String(jsonMapper.writeValueAsBytes(requestBody))
                .replace(validUri, invalidUri);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(stringBody.getBytes());

        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldCreateModelOnlyFunction() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS),
                                                    100);
        var modelUri =
                TEST_MODEL_URL_1;
        var modelDtos = List.of(FunctionModelDto.builder().name("model-1")
                                        .version("1.0").uri(URI.create(modelUri)).build());
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(modelDtos)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().versionId()).isNotNull();
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().apiBodyFormat()).isEqualTo(PREDICT_V2);
        assertThat(responseBody.function().containerArgs())
                .isEqualTo(DEFAULT_CONTAINER_ARGS_FOR_MODEL_ONLY_FUNCTIONS);
        assertThat(responseBody.function().containerImage()).isNull();
        assertThat(responseBody.function().containerEnvironment()).isNull();
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().models()).isNotNull().hasSize(1);
        assertThat(responseBody.function().healthUri()).isEqualTo(DEFAULT_HEALTH_ENDPOINT);

        var functionId = responseBody.function().id();
        var versionId = responseBody.function().versionId();
        var entity = functionsRepository.getByFunctionVersionId(versionId)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getUtilsContainerImage())
                .isNotBlank()
                .isEqualTo(GO);
        assertThat(entity.getContainerArgs())
                .isEqualTo(DEFAULT_CONTAINER_ARGS_FOR_MODEL_ONLY_FUNCTIONS);
    }


    Stream<Arguments> functionCreateVersionArgs() {
        var jwtCases = Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_FUNCTION_ID,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_FUNCTION_ID_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-guy",
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_FUNCTION_ID,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(null, TEST_FUNCTION_ID, HttpStatus.UNAUTHORIZED)
        );

        var apiKeyCases = Stream.of(
                // Authorized to create a version in TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_REGISTER_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             HttpStatus.OK),
                // Authorized to create versions of specific function of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + "*")),
                                             List.of(SCOPE_REGISTER_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             HttpStatus.OK),
                // No resource entries in the policy result.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_REGISTER_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                // Attempt to create a version of TEST_FUNCTION_ID when the resource entry in the
                // policy result only allows creating a version of TEST_FUNCTION_ID_2.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/" + "*")),
                                             List.of(SCOPE_REGISTER_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                // Incorrect scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of("nvapi-stg-some-key", TEST_FUNCTION_ID, HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("functionCreateVersionArgs")
    void shouldCreateFunctionVersion(
            Object tokenSupplier,
            UUID functionId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create the original function.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);

        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .utilsContainerImage(GO)
                .healthUri(TEST_HEALTH_URI)
                .build();
        var requestEntity = RequestEntity.post(
                        URI.create("/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().versionId()).isNotNull();
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().apiBodyFormat()).isEqualTo(PREDICT_V2);
        assertThat(responseBody.function().containerArgs()).isEqualTo(TEST_CONTAINER_ARGS);
        assertThat(responseBody.function().containerImage()).isEqualTo(TEST_NGC_CONTAINER_IMAGE);
        assertThat(responseBody.function().containerEnvironment()).isNull();
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().models()).isNotNull();
        assertThat(responseBody.function().healthUri()).isEqualTo(TEST_HEALTH_URI);
        var versionId = responseBody.function().versionId();
        var entity = functionsRepository.getByFunctionVersionId(versionId)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getUtilsContainerImage()).isNotBlank()
                .isEqualTo(GO);
        assertThat(entity.getFunctionLevelAuthorizedAccounts()).isNull();
        assertThat(entity.getVersionLevelAuthorizedAccounts()).isNull();
    }

    @Test
    void shouldFailToCreateVersionInDifferentAccount() {
        // Create token with subject that maps to TEST_NCA_ID account. This means, we are
        // creating a new function in TEST_NCA_ID account.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                    100);

        // Create the original function with TEST_FUNCTION_ID_2 in TEST_NCA_ID_2 account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2,
                                             TEST_FUNCTION_NAME);

        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .healthUri(TEST_HEALTH_URI)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .build();
        var requestEntity = RequestEntity.post(
                        URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID_2 + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    @Test
    void shouldFailToCreateFunctionDueToEmptyName() {
        // Create token with subject that maps to TEST_NCA_ID account. This means, we are
        // creating a new function in TEST_NCA_ID account.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                    100);

        // Create the original function with TEST_FUNCTION_ID_2 in TEST_NCA_ID_2 account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2,
                                             TEST_FUNCTION_NAME);

        var requestBody = CreateFunctionRequest.builder()
                .name("")
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .healthUri(TEST_HEALTH_URI)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .build();
        var requestEntity = RequestEntity.post(
                        URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID_2 + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldFailToCreateFunctionDueToLongName() {
        // Create token with subject that maps to TEST_NCA_ID account. This means, we are
        // creating a new function in TEST_NCA_ID account.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                    100);

        // Create the original function with TEST_FUNCTION_ID_2 in TEST_NCA_ID_2 account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2,
                                             TEST_FUNCTION_NAME);

        final var longName =
                "ioiyoeaayeyieyeoaiooyyiayyueiuyuyoeyyeyayuyeyioyioauuyuyaueiuieuayooyueuiuiueiioioueuaiooiayaeiyuuaeaaaieuoyyiuyeuoeyeayoeaiaoouu";
        HealthDto healthDto = HealthDto.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(ProtocolEnum.HTTP)
                .uri(TEST_HEALTH_URI)
                .build();
        var requestBody = CreateFunctionRequest.builder()
                .name(longName)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .healthUri(TEST_HEALTH_URI)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();
        var requestEntity = RequestEntity.post(
                        URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID_2 + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldFailToCreateFunctionDueToLongDescription() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                    100);

        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2,
                                             TEST_FUNCTION_NAME);

        var healthDto = HealthDto.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(ProtocolEnum.HTTP)
                .uri(TEST_HEALTH_URI)
                .build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .tags(TEST_TAGS)
                .description("a".repeat(257))
                .health(healthDto)
                .build();
        var requestEntity = RequestEntity.post(
                        URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID_2 + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    Stream<Arguments> functionCreateWithRateLimitArgs() {
        Set<String> tooManyNcaIds = new HashSet<>();
        for (int i = 1; i <= 33; i++) {
            tooManyNcaIds.add(TEST_NCA_ID + i);
        }

        return Stream.of(
                // valid ratelimit config
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .rateLimit("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .syncCheck(true)
                                     .perNcaIdRate(Map.of(TEST_NCA_ID_2, "3-M"))
                                     .build(),
                             HttpStatus.OK),
                // with only global rate configs
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .rateLimit("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .syncCheck(true)
                                     .build(),
                             HttpStatus.OK),
                // with only per nca id rate configs
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .perNcaIdRate(Map.of(TEST_NCA_ID_2, "3-M"))
                                     .build(),
                             HttpStatus.OK),
                // no nca id exemptions
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("4-S").build(),
                             HttpStatus.OK),
                // empty limit string
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("").build(),
                             HttpStatus.BAD_REQUEST),
                // bad rate limit string
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("1S").build(),
                             HttpStatus.BAD_REQUEST),
                // zero rate limit
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("0-S").build(),
                             HttpStatus.BAD_REQUEST),
                // no global rate limit nor per nca id config
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().build(),
                             HttpStatus.BAD_REQUEST),
                // too many exempted nca ids
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("4-S").exemptedNcaIds(tooManyNcaIds)
                                     .build(),
                             HttpStatus.BAD_REQUEST),
                // per-ncaid rate ncaid cannot be in exemptedNcaIds
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .perNcaIdRate(Map.of(TEST_NCA_ID, "3-M"))
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .build(),
                             HttpStatus.BAD_REQUEST),
                // exemptedNcaIds cannot exist without rateLimit
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .build(),
                             HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("functionCreateWithRateLimitArgs")
    void shouldCreateFunctionWithRateLimitConfig(
            String token,
            RateLimitDto rateLimitDto,
            HttpStatus expectedStatus) {
        var modelUri =
                TEST_MODEL_URL_1;
        var resourceUri =
                TEST_RESOURCE_URL_1;

        var modelDtos = List.of(FunctionModelDto.builder().name("model-1")
                                        .version("1.0").uri(URI.create(modelUri)).build());
        HealthDto healthDto = null;
        healthDto = HealthDto.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(ProtocolEnum.HTTP)
                .uri(URI.create(TEST_HEALTH_ENDPOINT))
                .build();
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(modelDtos)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .rateLimit(rateLimitDto)
                .resources(Set.of(
                        ArtifactDto.builder().name("resource-1")
                                .version("1.0")
                                .uri(URI.create(resourceUri))
                                .build()))
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        assertThat(responseEntity.getBody().function().rateLimit()).isEqualTo(rateLimitDto);
    }

    Stream<Arguments> functionListArgsByAccount() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             HttpStatus.OK, List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS_DETAILS),
                                                             100),
                             HttpStatus.OK, List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS_DETAILS),
                                                             100),
                             HttpStatus.OK, List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-guy",
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             HttpStatus.NOT_FOUND, null),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             HttpStatus.FORBIDDEN, null),
                Arguments.of(null, HttpStatus.UNAUTHORIZED, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", "*")),
                                List.of());
                    return "nvapi-stg-some-key";
                }, HttpStatus.FORBIDDEN, null),
                // Even though apikey contains "non-existent-account" as NCA Id, the expected
                // response should be HttpStatus.OK as we do not validate NCA Id. This allows users
                // of Personal Orgs to invoke/list/check-queue-depth for public functions
                // successfully during their trial period. Note that the expected list of
                // functions will be empty as there are no functions defined in the
                // non-existent-account.
                Arguments.of((Supplier<String>) () -> {
                    setResponse("non-existent-account", TEST_OWNER_ID,
                                List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                        new Resource("function", TEST_FUNCTION_ID_2 + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of()),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                        new Resource("function", TEST_FUNCTION_ID_2 + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + UUID.randomUUID()),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of(TEST_FUNCTION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", "*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of()),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of(TEST_FUNCTION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/" + TEST_VERSION_ID_2)),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of(TEST_FUNCTION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     UUID.randomUUID() + "/" + TEST_VERSION_ID_2)),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of()),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", UUID.randomUUID() + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of()),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/" + TEST_VERSION_ID_2)),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/" + TEST_VERSION_ID_2),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID + "/*"),
                                        new Resource("some type", "abcd")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/" + TEST_VERSION_ID_2),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + UUID.randomUUID()),
                                        new Resource("function",
                                                     UUID.randomUUID() + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, HttpStatus.OK, List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2))
        );
    }

    @ParameterizedTest
    @MethodSource("functionListArgsByAccount")
    void shouldListFunctionsByAccount(
            Object tokenSupplier,
            HttpStatus expectedStatus,
            @Nullable List<UUID> expectedFunctions) {
        var token = getToken(tokenSupplier);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME_2);

        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/functions"))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListFunctionsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(expectedFunctions).isNotNull();
        assertThat(responseBody.functions()).hasSize(expectedFunctions.size());
        var functions = responseBody.functions().stream()
                .collect(Collectors.toMap(FunctionDto::id, Function.identity()));
        assertThat(functions.keySet()).isEqualTo(new HashSet<>(expectedFunctions));
        for (UUID expectedFunctionId : expectedFunctions) {
            var functionDto = functions.get(expectedFunctionId);
            assertThat(functionDto).isNotNull();
            assertThat(functionDto.createdAt()).isNotNull();
            assertThat(functionDto.ownedByDifferentAccount()).isNull();
            assertThat(functionDto.healthUri()).isEqualTo(TEST_HEALTH_URI);
            assertThat(functionDto.containerArgs()).isNotBlank();
            assertThat(functionDto.activeInstances()).isNull();
            assertThat(functionDto.secrets()).isNull();
            assertThat(functionDto.rateLimit()).isNull();
            if (functionDto.id().equals(TEST_FUNCTION_ID)) {
                assertThat(functionDto.versionId()).isEqualTo(TEST_VERSION_ID_1);
            } else if (functionDto.id().equals(TEST_FUNCTION_ID_2)) {
                assertThat(functionDto.versionId()).isEqualTo(TEST_VERSION_ID_2);
            } else {
                fail("unknown function returned");
            }
            assertThat(functionDto.description()).isNotEmpty();
            assertThat(functionDto.tags()).isNotEmpty();
        }
    }

    @ParameterizedTest
    @MethodSource("functionListArgsByAccount")
    void shouldListFunctionIdsByAccount(
            Object tokenSupplier,
            HttpStatus expectedStatus,
            @Nullable List<UUID> expectedFunctions) {
        var token = getToken(tokenSupplier);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME_2);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_3, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME_3);

        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/functions/ids"))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListFunctionIdsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(expectedFunctions).isNotNull();
        assertThat(responseBody.functionIds()).hasSize(expectedFunctions.size());
        var functionIds = responseBody.functionIds();
        assertThat(functionIds).containsExactlyInAnyOrderElementsOf(expectedFunctions);
    }


    Stream<Arguments> functionListArgsByFunction() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_FUNCTION_ID,
                             HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS_DETAILS),
                                                             100),
                             TEST_FUNCTION_ID,
                             HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS_DETAILS),
                                                             100),
                             TEST_FUNCTION_ID, HttpStatus.OK, List.of(TEST_VERSION_ID_1,
                                                                      TEST_VERSION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-guy",
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_FUNCTION_ID, HttpStatus.NOT_FOUND, null),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_FUNCTIONS),
                                                             100),
                             UUID.randomUUID(), HttpStatus.NOT_FOUND, null),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null),
                Arguments.of(null, TEST_FUNCTION_ID, HttpStatus.UNAUTHORIZED, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", "*")),
                                List.of());
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", "*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of());
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", "*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("account-functions", TEST_FUNCTION_ID + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", TEST_FUNCTION_ID_2 + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", TEST_FUNCTION_ID + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, UUID.randomUUID(), HttpStatus.NOT_FOUND, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", TEST_FUNCTION_ID + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.OK, List.of(TEST_VERSION_ID_1)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_2)),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_2),
                                        new Resource("function", TEST_FUNCTION_ID + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_2),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID + "/" + UUID.randomUUID()),
                                        new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/" + TEST_VERSION_ID_3),
                                        new Resource("function", UUID.randomUUID() + "/*"),
                                        new Resource("some type", "abcd")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function",
                                                     UUID.randomUUID() + "/" + TEST_VERSION_ID_2)),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null),
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new Resource("function", UUID.randomUUID() + "/*")),
                                List.of(SCOPE_LIST_FUNCTIONS));
                    return "nvapi-stg-some-key";
                }, TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null)
        );
    }

    @ParameterizedTest
    @MethodSource("functionListArgsByFunction")
    void shouldListVersionsByFunction(
            Object tokenSupplier,
            UUID functionId,
            HttpStatus expectedStatus,
            List<UUID> expectedFunctionVersionIds) {
        var token = getToken(tokenSupplier);

        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_2, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME_2);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_3, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME_3);

        var requestEntity =
                RequestEntity.get(
                                URI.create("/v2/nvcf/functions/" + functionId + "/versions"))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListFunctionsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(expectedFunctionVersionIds).isNotNull();
        assertThat(responseBody.functions()).hasSize(expectedFunctionVersionIds.size());
        var functions = responseBody.functions().stream()
                .collect(Collectors.toMap(FunctionDto::versionId, Function.identity()));
        assertThat(functions.keySet()).isEqualTo(new HashSet<>(expectedFunctionVersionIds));
        for (UUID expectedFunctionId : expectedFunctionVersionIds) {
            var functionDto = functions.get(expectedFunctionId);
            assertThat(functionDto).isNotNull();
            assertThat(functionDto.ownedByDifferentAccount()).isNull();
            assertThat(functionDto.createdAt()).isNotNull();
            assertThat(functionDto.containerArgs()).isNotBlank();
            assertThat(functionDto.activeInstances()).isNull();
            assertThat(functionDto.secrets()).isNull();
            assertThat(functionDto.rateLimit()).isNull();
            assertThat(functionDto.id()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(functionDto.healthUri()).isEqualTo(TEST_HEALTH_URI);
            assertThat(functionDto.description()).isNotEmpty();
            assertThat(functionDto.tags()).isNotEmpty();
            assertThat(functionDto.health()).isNotNull();
            assertThat(functionDto.health().getProtocol()).isEqualTo(ProtocolEnum.HTTP);
            assertThat(functionDto.health().getUri()).isEqualTo(TEST_HEALTH_URI);
            assertThat(functionDto.health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
            assertThat(functionDto.health().getExpectedStatusCode()).isEqualTo(
                    EXPECTED_STATUS_CODE);
        }
    }


    Stream<Arguments> functionDetailsArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_FUNCTIONS,
                                                          SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-guy",
                                                             List.of(SCOPE_LIST_FUNCTIONS,
                                                          SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_FUNCTIONS,
                                                          SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_FUNCTIONS,
                                                          SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.UNAUTHORIZED),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of("dont_list_functions"));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_2, HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_2, HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_2)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_1)),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of("nvapi-stg-some-key", TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of("nvapi-stg-some-key", TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of("nvapi-stg-some-key", TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2, HttpStatus.FORBIDDEN)
        );
    }

    @ParameterizedTest
    @MethodSource("functionDetailsArgs")
    void shouldGetFunctionDetailsUsingVersionId(
            Object tokenSupplier,
            UUID functionId,
            UUID functionVersionId,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);
        // Create a function.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);

        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/functions/" + functionId
                                                     + "/versions/" + functionVersionId))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().ownedByDifferentAccount()).isNull();
        assertThat(responseBody.function().activeInstances()).isNull();
        assertThat(responseBody.function().healthUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().containerArgs()).isNotBlank();
        assertThat(responseBody.function().description()).isNotEmpty();
        assertThat(responseBody.function().tags()).isNotEmpty();
        assertThat(responseBody.function().health()).isNotNull();
        assertThat(responseBody.function().health().getProtocol()).isEqualTo(ProtocolEnum.HTTP);
        assertThat(responseBody.function().health().getUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
        assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                EXPECTED_STATUS_CODE);
    }

    @Test
    void shouldGetFunctionDetailsUsingVersionIdWithDeletedRatelimit() {
        String token = getToken(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                                List.of(SCOPE_LIST_FUNCTIONS,
                                                             SCOPE_UPDATE_FUNCTION,
                                                             SCOPE_DELETE_FUNCTION),
                                                                100));
        // Create a function with empty ratelimit
        testService.createTestFunctionEntity(TEST_FUNCTION_ID,
                                             TEST_VERSION_ID_1,
                                             TEST_NCA_ID,
                                             TEST_FUNCTION_NAME,
                                             RateLimitUdt.builder().build());

        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                                     + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().ownedByDifferentAccount()).isNull();
        assertThat(responseBody.function().activeInstances()).isNull();
        assertThat(responseBody.function().healthUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().containerArgs()).isNotBlank();
        assertThat(responseBody.function().description()).isNotEmpty();
        assertThat(responseBody.function().tags()).isNotEmpty();
        assertThat(responseBody.function().rateLimit()).isNull();
        assertThat(responseBody.function().health()).isNotNull();
        assertThat(responseBody.function().health().getProtocol()).isEqualTo(ProtocolEnum.HTTP);
        assertThat(responseBody.function().health().getUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
        assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                EXPECTED_STATUS_CODE);
    }


    Stream<Arguments> deleteArgs() {
        var jwtCases = Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NO_CONTENT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-guy",
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             HttpStatus.FORBIDDEN),
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_2, HttpStatus.UNAUTHORIZED)
        );

        var apiKeyCases = Stream.of(
                // Authorized to delete any private functions of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_DELETE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NO_CONTENT),
                // Authorized to delete on specific function version of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_DELETE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NO_CONTENT),
                // Authorized to delete any versions of specific function of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + "*")),
                                             List.of(SCOPE_DELETE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NO_CONTENT),
                // Attempt to delete a function of TEST_NCA_ID account that does not have
                // a matching resource entry in the policy result.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_3 + "/" + "*")),
                                             List.of(SCOPE_DELETE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                // Attempt to delete a function of TEST_NCA_ID account with no resource
                // entries in the policy result.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_DELETE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                // Attempt to delete a function belonging to TEST_NCA_ID_2 account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/" + TEST_VERSION_ID_2)),
                                             List.of(SCOPE_DELETE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             HttpStatus.NOT_FOUND),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                // Incorrect scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of("nvapi-stg-some-key", TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2, HttpStatus.FORBIDDEN)
        );
        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("deleteArgs")
    void shouldDeleteFunction(
            Object tokenSupplier,
            UUID functionId,
            UUID functionVersionId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create a version of TEST_FUNCTION_ID in TEST_NCA_ID account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);

        // Create a version of TEST_FUNCTION_ID_2 in TEST_NCA_ID_2 account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2,
                                             TEST_FUNCTION_NAME_2);

        var requestEntity =
                RequestEntity.delete(URI.create("/v2/nvcf/functions/" + functionId
                                                        + "/versions/" + functionVersionId))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        var entity = functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_1);
        if (expectedStatus.isError()) {
            assertThat(entity).isNotEmpty();
            return;
        }
        assertThat(entity).isEmpty();
    }

    Stream<Arguments> updateFunctionArgs() {
        var jwtCases = Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-guy",
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             TEST_TAGS_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             TEST_TAGS_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN),
                // weird tag characters
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Set.of("\n&abc123["),
                             HttpStatus.BAD_REQUEST),
                // tags with special namespace:key=value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_FUNCTION,
                                                          SCOPE_DELETE_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Set.of("namespace:key=value"),
                             HttpStatus.OK),
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_2, TEST_TAGS_2,
                             HttpStatus.UNAUTHORIZED)
        );

        var apiKeyCases = Stream.of(
                // Authorized to update any private functions of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_UPDATE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.OK),
                // Authorized to update a specific function version of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_UPDATE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.OK),
                // Authorized to update all versions of specific function of TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + "*")),
                                             List.of(SCOPE_UPDATE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.OK),
                // Attempt to update a function of TEST_NCA_ID account that does not have
                // a matching resource entry in the policy result.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_3 + "/" + "*")),
                                             List.of(SCOPE_UPDATE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN),
                // Attempt to update a function of TEST_NCA_ID account with no
                // resource entries in the policy result.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_UPDATE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN),
                // Attempt to update a function belonging to TEST_NCA_ID_2 account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/" + TEST_VERSION_ID_2)),
                                             List.of(SCOPE_UPDATE_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             TEST_TAGS_2,
                             HttpStatus.NOT_FOUND),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN),
                // Incorrect scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN),
                Arguments.of("nvapi-stg-some-key",
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("updateFunctionArgs")
    void shouldUpdateFunction(
            Object tokenSupplier,
            UUID functionId,
            UUID functionVersionId,
            Set<String> tags,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create a version of TEST_FUNCTION_ID in TEST_NCA_ID account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);

        // Create a version of TEST_FUNCTION_ID_2 in TEST_NCA_ID_2 account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2,
                                             TEST_FUNCTION_NAME_2);

        var requestBody = UpdateFunctionRequest.builder()
                .tags(tags)
                .build();
        var requestEntity =
                RequestEntity.put(URI.create("/v2/nvcf/metadata/functions/" + functionId
                                                     + "/versions/" + functionVersionId))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        var functionEntity = functionsRepository.getByFunctionVersionId(functionVersionId);
        if (expectedStatus.isError()) {
            return;
        }

        var function = responseEntity.getBody().function();
        assertThat(function).isNotNull();
        assertThat(function.health()).isNotNull();
        assertThat(function.healthUri()).isNotNull();

        assertThat(functionEntity).isNotEmpty();
        assertThat(functionEntity.get().getTags()).isEqualTo(tags);
        assertThat(functionEntity.get().getHealth()).isNotNull();
    }

    @ParameterizedTest
    @CsvSource({
            """
                    {
                      "name": "test-function-name",
                      "inferenceUrl": "test-inference-url",
                      "inferencePort": 7777,
                      "containerArgs": "test-container-args",
                      "containerImage": "stg.nvcr.io/test-account/test-container-image:latest",
                      "apiBodyFormat": "PREDICT_V2",
                      "models": [
                        {
                          "version": "1",
                          "uri": "/v2/org/zq9tgrjzrfpo/models/mixtral/1/files"
                        }
                      ]
                    }
                    """,
            """
                    {
                      "name": "test-function-name",
                      "inferenceUrl": "test-inference-url",
                      "inferencePort": 7777,
                      "containerArgs": "test-container-args",
                      "containerImage": "stg.nvcr.io/test-account/test-container-image:latest",
                      "apiBodyFormat": "PREDICT_V2",
                      "resources": [
                        {
                          "version": "1",
                          "uri": "/v2/org/zq9tgrjzrfpo/models/mixtral/1/files"
                        }
                      ]
                    }
                    """,
    })
    void shouldRejectNullOrEmptyArtifacts(String requestBody) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS),
                                                    100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldCreateStreamingFunction() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS),
                                                    100);
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .functionType(FunctionTypeEnum.STREAMING)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().containerImage()).isEqualTo(TEST_NGC_CONTAINER_IMAGE);
        assertThat(responseBody.function().functionType()).isEqualTo(FunctionTypeEnum.STREAMING);

        var versionId = responseBody.function().versionId();
        var entity = functionsRepository.getByFunctionVersionId(versionId)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getFunctionType()).isEqualTo(FunctionType.STREAMING);
    }

    @Test
    void shouldDefaultToNonStreamingFunction() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS),
                                                    100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                // omitting the streaming field
                .body("""
                              {
                                "name": "test-function-name",
                                "inferenceUrl": "test-inference-url",
                                "inferencePort": 7777,
                                "containerArgs": "test-container-args",
                                "containerImage": "%s"
                              }
                              """.formatted(TEST_NGC_CONTAINER_IMAGE.toString()));
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().containerImage()).isEqualTo(TEST_NGC_CONTAINER_IMAGE);
        assertThat(responseBody.function().functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);

        var functionId = responseBody.function().id();
        var versionId = responseBody.function().versionId();
        var entity = functionsRepository.getByFunctionVersionId(versionId)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getFunctionType()).isEqualTo(FunctionType.DEFAULT);
    }

    @Test
    void shouldHandleNullFunctionTypeInDB() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS),
                                                    100);
        // Create a function and remove the function type from the db.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);
        cqlSession.execute("DELETE " + FunctionEntity.COLUMN_FUNCTION_TYPE + " FROM "
                                   + FunctionEntity.TABLE_NAME + " WHERE "
                                   + FunctionEntity.COLUMN_FUNCTION_VERSION_ID + " = ?",
                           TEST_VERSION_ID_1);

        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                                     + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);

        var entity = functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_1)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getFunctionType()).isNull();
    }

    @Test
    void shouldResponseTimeoutISOFormat() throws JacksonException {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS),
                                                    100);
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .functionType(FunctionTypeEnum.STREAMING)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var jsonNodes = jsonMapper.readValue(responseEntity.getBody(), ObjectNode.class);
        assertThat(jsonNodes).isNotEmpty();
        assertThat(
                jsonNodes.get("function").get("health").get("timeout").textValue())
                .isEqualTo("PT10S");
    }

    @Test
    void shouldThrowTooManyFunctionsException() throws JacksonException {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                    100);

        // update account set max allowed = 1
        var accountRequestBody = AccountUpdateRequest.builder()
                .maxFunctionsAllowed(1)
                .build();
        var accoutnRequestEntity = RequestEntity.patch(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " +
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of("account_setup"), 100))
                .body(accountRequestBody);
        var accountResponseEntity =
                testRestTemplate.exchange(accoutnRequestEntity, AccountDetailsResponse.class);
        assertThat(accountResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(accountResponseEntity.getBody().account().maxFunctionsAllowed()).isEqualTo(1);

        // create functions
        var functionRequestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .functionType(FunctionTypeEnum.STREAMING)
                .build();
        var functionRequestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(functionRequestBody);
        // create the first function; expecting OK
        var functionResponseEntity1 =
                testRestTemplate.exchange(functionRequestEntity, String.class);
        assertThat(functionResponseEntity1.getStatusCode()).isEqualTo(HttpStatus.OK);
        UUID functionId = jsonMapper.readValue(functionResponseEntity1.getBody(),
                                               CreateFunctionResponse.class).function().id();

        // create the second function: expecting BAD_REQUEST
        var functionResponseEntity2 =
                testRestTemplate.exchange(functionRequestEntity, String.class);
        assertThat(functionResponseEntity2.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        // verify we still can create versions with the first function
        var versionRequestEntity = RequestEntity.post(
                        URI.create("/v2/nvcf/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(functionRequestBody);
        var versionResponseEntity =
                testRestTemplate.exchange(versionRequestEntity, String.class);
        assertThat(versionResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

}
