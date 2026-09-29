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
import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_SECRET_NAME_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_SECRET_VALUE_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LIST_FUNCTIONS_DETAILS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_UPDATE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_DTOS;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_CLIENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NAME_V1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.TestAuthorizedPartiesService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.TestUtil;
import java.net.URI;
import java.util.List;
import java.util.Optional;
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
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class FunctionWithUserSecretsTest {

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestManagementService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private AuthorizedPartiesService authPartiesService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private EssService essService;

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
        MockEssServer.start(essBaseUrl);
        MockApiKeysServer.start(apiKeysBaseUrl);
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
        MockEssServer.clearSecrets();
        resetToDefault();
    }

    Stream<Arguments> createFunctionWithSecretsArgs() {
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("AWS_REGION", "us-west-2")
                .put("AWS_BUCKET", "ov-content")
                .put("AWS_ACCESS_KEY_ID", "ov-content-key-id")
                .put("AWS_SECRET_ACCESS_KEY", "ov-content-access-key")
                .put("AWS_SESSION_TOKEN", "ov-content-session-token");
        return Stream.of(
                // no secrets
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             null,
                             null,
                             HttpStatus.OK),
                // single secret
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode("value1")).build()),
                             Set.of("secret1"),
                             HttpStatus.OK),
                // multiple secrets
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder().name("secret2")
                                            .value(new StringNode("value2")).build(),
                                    SecretDto.builder().name("secret3")
                                            .value(secretJsonNodeValue).build()),
                             Set.of("secret1", "secret2", "secret3"),
                             HttpStatus.OK),
                // secret names with periods and hyphens
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
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
                             HttpStatus.OK),
                // secret names with underscores
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name("omni_s3_us-west-2_amazonaws_com")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder()
                                            .name("omni_s3_eu-north-1_amazonaws_com")
                                            .value(new StringNode("value2")).build(),
                                    SecretDto.builder()
                                            .name("omni_s3.ap-northeast-1_amazonaws_com")
                                            .value(secretJsonNodeValue).build()),
                             Set.of("omni_s3_us-west-2_amazonaws_com",
                                    "omni_s3_eu-north-1_amazonaws_com",
                                    "omni_s3.ap-northeast-1_amazonaws_com"),
                             HttpStatus.OK),
                // duplicate secrets
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode("value1")).build(),
                                    SecretDto.builder().name("secret1")
                                            .value(new StringNode("value2")).build()),
                             null,
                             HttpStatus.BAD_REQUEST),
                // empty secret name
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder().name("")
                                            .value(new StringNode("value1")).build()),
                             null,
                             HttpStatus.BAD_REQUEST),
                // Secret name - exactly MAX_SECRET_NAME_LENGTH in length
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(StringUtils.repeat("x", MAX_SECRET_NAME_LENGTH))
                                            .value(new StringNode("value1")).build()),
                             Set.of(StringUtils.repeat("x", MAX_SECRET_NAME_LENGTH)),
                             HttpStatus.OK),
                // long secret name - exceeding MAX_SECRET_NAME_LENGTH length
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder()
                                            .name(StringUtils.repeat("secret1", MAX_SECRET_NAME_LENGTH))
                                            .value(new StringNode("value1")).build()),
                             null,
                             HttpStatus.BAD_REQUEST),
                // empty secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode("")).build()),
                             null,
                             HttpStatus.BAD_REQUEST),
                // long secret value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder().name("secret1")
                                            .value(new StringNode(StringUtils.repeat("value1",
                                                                                   MAX_SECRET_VALUE_LENGTH)))
                                            .build()),
                             null,
                             HttpStatus.BAD_REQUEST),
                // bad secret name
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                          SCOPE_LIST_FUNCTIONS),
                                                             100),
                             Set.of(SecretDto.builder().name("*secret1*-\"")
                                            .value(new StringNode("value1")).build()),
                             null,
                             HttpStatus.BAD_REQUEST)
                        );
    }

    @ParameterizedTest
    @MethodSource("createFunctionWithSecretsArgs")
    void shouldCreateFunctionWithSecrets(
            String token,
            Set<SecretDto> secrets,
            Set<String> expectedSecretNames,
            HttpStatus expectedStatus) {
        // Create the original function in TEST_NCA_ID
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .secrets(secrets)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var functionId = responseBody.function().id();
        var versionId = responseBody.function().versionId();
        var functionDto = responseBody.function();
        
        // Verify hasSecrets field in FunctionEntity
        var functionEntity = functionsRepository.getByFunctionVersionId(versionId).orElseThrow();
        if (expectedSecretNames == null) {
            assertThat(functionDto.secrets()).isNull();
            assertThat(functionEntity.hasSecrets()).isFalse();
        } else {
            assertThat(functionDto.secrets())
                    .containsExactlyInAnyOrderElementsOf(expectedSecretNames);
            assertThat(functionEntity.hasSecrets()).isTrue();

            // Confirm secrets are saved in ESS
            var returnedNames = essService.getFunctionVersionSecretNames(functionId, versionId).orElse(null);
            assertThat(returnedNames).isNotNull();
            assertThat(returnedNames).containsExactlyInAnyOrderElementsOf(expectedSecretNames);
        }

    }

    @ParameterizedTest
    @MethodSource("createFunctionWithSecretsArgs")
    void shouldCreateFunctionVersionWithSecrets(
            String token,
            Set<SecretDto> secrets,
            Set<String> expectedSecretNames,
            HttpStatus expectedStatus) {
        // Create the original function in TEST_NCA_ID
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);
        // Then create a new version of it with secrets
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .secrets(secrets)
                .build();
        var requestEntity = RequestEntity.post(
                        URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var functionId = responseBody.function().id();
        var versionId = responseBody.function().versionId();
        var functionDto = responseBody.function();
        if (expectedSecretNames == null) {
            assertThat(functionDto.secrets()).isNull();
        } else {
            assertThat(functionDto.secrets())
                    .containsExactlyInAnyOrderElementsOf(expectedSecretNames);
            // Confirm secrets are saved in ESS
            var returnedNames = essService.getFunctionVersionSecretNames(functionId, versionId).orElse(null);
            assertThat(returnedNames).isNotNull();
            assertThat(returnedNames).containsExactlyInAnyOrderElementsOf(expectedSecretNames);
        }
    }

    Stream<Arguments> listFunctionWithSecretsArgs() {
        return Stream.of(
                Arguments.of(SCOPE_LIST_FUNCTIONS_DETAILS, null),
                Arguments.of(SCOPE_LIST_FUNCTIONS_DETAILS, Boolean.TRUE),
                Arguments.of(SCOPE_LIST_FUNCTIONS_DETAILS, Boolean.FALSE),
                Arguments.of(SCOPE_LIST_FUNCTIONS, Boolean.TRUE),
                Arguments.of(SCOPE_LIST_FUNCTIONS, Boolean.FALSE)
                        );
    }

    ;

    @ParameterizedTest
    @MethodSource("listFunctionWithSecretsArgs")
    void shouldListFunctionsWithSecretsByAccount(
            String listFunctionsScope,
            Boolean includeSecrets) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 listFunctionsScope),
                                                    100);
        // Create a function with secrets in TEST_NCA_ID
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
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .secrets(secrets)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // After function creation, list function.
        var endpoint = "/v2/nvcf/functions";
        var listRequestEntity = RequestEntity.get(URI.create(endpoint))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, ListFunctionsResponse.class);
        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).hasSize(1);

        var functionDto = responseBody.functions().getFirst();
        assertThat(functionDto).isNotNull();
        assertThat(functionDto.secrets()).isNull();
    }

    @ParameterizedTest
    @MethodSource("listFunctionWithSecretsArgs")
    void shouldListFunctionVersionsWithSecrets(
            String listFunctionsScope,
            Boolean includeSecrets) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 listFunctionsScope),
                                                    100);
        // Create a function with secrets in TEST_NCA_ID
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
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .secrets(secrets)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var functionId = responseEntity.getBody().function().id();

        // After function creation, list function.
        var endpoint = "/v2/nvcf/functions/" + functionId + "/versions";
        var listRequestEntity = RequestEntity.get(URI.create(endpoint))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, ListFunctionsResponse.class);
        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).hasSize(1);

        var functionDto = responseBody.functions().getFirst();
        assertThat(functionDto).isNotNull();
        assertThat(functionDto.secrets()).isNull();
    }


    @ParameterizedTest
    @MethodSource("listFunctionWithSecretsArgs")
    void shouldGetFunctionDetailsWithSecretsUsingVersionId(
            String listFunctionsScope,
            Boolean includeSecrets) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 listFunctionsScope),
                                                    100);
        // Create original function in TEST_NCA_ID
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Create a version with secrets
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
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .secrets(secrets)
                .build();
        var requestEntity =
                RequestEntity.post(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID + "/versions"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var versionId = responseEntity.getBody().function().versionId();

        // After function creation, list function
        var endpoint = "/v2/nvcf/functions/" + TEST_FUNCTION_ID + "/versions/" + versionId;
        if (includeSecrets != null) {
            endpoint += "?includeSecrets=" + includeSecrets;
        }
        var listRequestEntity =
                RequestEntity.get(URI.create(endpoint))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, FunctionResponse.class);
        var responseBody = listResponseEntity.getBody();

        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();

        var functionDto = responseBody.function();
        assertThat(functionDto).isNotNull();
        if (listFunctionsScope.equals(SCOPE_LIST_FUNCTIONS_DETAILS)) {
            if ((includeSecrets == null) || includeSecrets) {
                assertThat(functionDto.secrets()).isNotNull();
                assertThat(responseBody.function().secrets()).hasSize(3);
                assertThat(functionDto.secrets()).containsExactlyInAnyOrderElementsOf(
                        expectedSecretNames);
            } else {
                assertThat(functionDto.secrets()).isNull();
            }
        } else {
            assertThat(functionDto.secrets()).isNull();
        }
    }

    @Test
    void shouldDeleteFunctionWithSecrets() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_UPDATE_FUNCTION,
                                                 SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_DELETE_FUNCTION),
                                                    100);

        // Create a function with secrets in TEST_NCA_ID
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
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .secrets(secrets)
                .build();
        var requestEntity =
                RequestEntity.post(URI.create("/v2/nvcf/functions"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var functionId = responseEntity.getBody().function().id();
        var versionId = responseEntity.getBody().function().versionId();

        // Make sure there are secrets first before deleting a function
        var secretDtos = essService.getFunctionVersionSecrets(functionId, versionId).orElse(null);
        assertThat(secretDtos).isNotNull().hasSize(3);

        var deleteRequestEntity =
                RequestEntity.delete(URI.create("/v2/nvcf/functions/" + functionId
                                                        + "/versions/" + versionId))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var deleteResponseEntity = testRestTemplate.exchange(deleteRequestEntity, Void.class);
        assertThat(deleteResponseEntity.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);
        var entity = functionsRepository.getByFunctionVersionId(versionId);
        assertThat(entity).isEmpty();

        // Make sure secrets no longer exist after function deletion
        secretDtos = essService.getFunctionVersionSecrets(functionId, versionId).orElse(null);
        assertThat(secretDtos).isNull();
    }

    Stream<Arguments> publicFunctionsListSecrets() {
        return Stream.of(
                // Create token using OAuth2 Client tied to TEST_NCA_ID to list
                // functions when cloud credits are available. Public functions are created under
                // account TEST_PUBLIC_FUNCTION_NCA_ID. Current account is TEST_NCA_ID.
                // Regardless of the value of includeSecrets query param, secrets will not be
                // in the response as the public function is in TEST_PUBLIC_FUNCTION_NCA_ID
                // account, and we are using a token that is tied to TEST_NCA_ID account to
                // get function details.
                Arguments.of((Supplier<String>) () -> {
                                 return MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                                        List.of(SCOPE_LIST_FUNCTIONS_DETAILS),
                                                                        100);
                             },
                             false,
                             true),
                // Create token using OAuth2 Client tied to TEST_NCA_ID to list
                // functions when cloud credits are available. Public functions are created under
                // account TEST_PUBLIC_FUNCTION_NCA_ID. Current account is TEST_NCA_ID.
                // Regardless of the value of includeSecrets query param, secrets will not be
                // in the response as the public function is in TEST_PUBLIC_FUNCTION_NCA_ID
                // account, and we are using a token that is tied to TEST_NCA_ID account to
                // get function details.
                Arguments.of((Supplier<String>) () -> {
                                 return MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                                        List.of(SCOPE_LIST_FUNCTIONS_DETAILS),
                                                                        100);
                             },
                             false,
                             false),
                // Create token using OAuth2 Client tied to TEST_PUBLIC_FUNCTION_NCA_ID to list
                // functions when cloud credits are available. Public functions are created
                // under the current account i.e. TEST_PUBLIC_FUNCTION_NCA_ID. Response should
                // include secrets.
                Arguments.of((Supplier<String>) () -> {
                                 return MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_PUBLIC_FUNCTION_CLIENT_ID,
                                                                        List.of(SCOPE_LIST_FUNCTIONS_DETAILS),
                                                                        100);
                             },
                             true,
                             true),
                // Create token using OAuth2 Client tied to TEST_PUBLIC_FUNCTION_NCA_ID to list
                // functions when cloud credits are available. Public functions are created
                // under the current account i.e. TEST_PUBLIC_FUNCTION_NCA_ID. Response should
                // not include secrets.
                Arguments.of((Supplier<String>) () -> {
                                 return MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_PUBLIC_FUNCTION_CLIENT_ID,
                                                                        List.of(SCOPE_LIST_FUNCTIONS_DETAILS),
                                                                        100);
                             },
                             true,
                             false)
                        );
    }

    @ParameterizedTest
    @MethodSource("publicFunctionsListSecrets")
    void shouldListPublicFunctionWithSecrets(Object tokenSupplier,
                                             boolean publicFuncsInCurrentAccount,
                                             boolean includeSecrets) {
        authPartiesService.clearPublicFunctionCache();

        // Create an account for public functions
        testAccountService.createAccountAndAssociateClients(TEST_PUBLIC_FUNCTION_NCA_ID,
                                                            Set.of(TEST_PUBLIC_FUNCTION_CLIENT_ID));

        // Create a function associated with public account with secrets in TEST_PUBLIC_FUNCTION_NCA_ID
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_PUBLIC_FUNCTION_CLIENT_ID,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS_DETAILS),
                                                    100);
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
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_PUBLIC_FUNCTION_NAME_V1)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .secrets(secrets)
                .build();
        var requestEntity =
                RequestEntity.post(URI.create("/v2/nvcf/functions"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var functionId = responseEntity.getBody().function().id();
        var versionId = responseEntity.getBody().function().versionId();

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
                                       );
        testAuthPartiesService.associateAuthParties(TEST_PUBLIC_FUNCTION_NCA_ID, functionId,
                                                    Optional.empty(), authorizedParties1);

        // Retrieve function using the passed in token -- Only public functions are defined at
        // this point.
        token = TestUtil.getToken(tokenSupplier);
        var endpoint = "/v2/nvcf/functions/" + functionId +
                "/versions/" + versionId + "?includeSecrets=" + includeSecrets;
        var listRequestEntity = RequestEntity.get(URI.create(endpoint))
                .header("Authorization", "Bearer " + token)
                .build();
        var listResponseEntity = testRestTemplate.exchange(listRequestEntity, FunctionResponse.class);
        assertThat(listResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = listResponseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        var functionDto = responseBody.function();
        assertThat(functionDto.ncaId()).isEqualTo(TEST_PUBLIC_FUNCTION_NCA_ID);
        assertThat(functionDto.id()).isEqualTo(functionId);
        assertThat(functionDto.versionId()).isEqualTo(versionId);
        if (publicFuncsInCurrentAccount) {
            if (includeSecrets) {
                assertThat(functionDto.secrets()).isNotNull();
            } else {
                assertThat(functionDto.secrets()).isNull();
            }
        } else {
            assertThat(functionDto.secrets()).isNull();
        }

        // Delete functions and then account.
        functionsRepository.deleteAll();
        testAccountService.deleteAccount(TEST_PUBLIC_FUNCTION_NCA_ID);
    }

    @Test
    void shouldThrow400WithMissingQuotesAroundSecretValuesUsingRawPayload() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_PUBLIC_FUNCTION_CLIENT_ID,
                                                    List.of(SCOPE_REGISTER_FUNCTION,
                                                 SCOPE_LIST_FUNCTIONS_DETAILS),
                                                    100);
        var headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        headers.setBearerAuth(token);

        // Raw payload
        var payload = """
                {
                  "name": "nucleus-connectivity-test",
                  "inferenceUrl": "/connect",
                  "inferencePort": 8000,
                  "containerImage": "nvcr.io/0539311403528978/nucleus-connectivity-test:latest",
                  "health": {
                    "protocol": "HTTP",
                    "uri": "/health",
                    "port": 8000,
                    "timeout": "PT10S",
                    "expectedStatusCode": 200
                  },
                  "secrets": [
                    {
                      "name": "OMNI_TRUSTED_CERTIFICATE",
                      "value": LS0tLS1CRU...==
                    },
                    {
                      "name": "OMNI_MTLS_CLIENT_CERTIFICATE",
                      "value": LS0tLS1CRUdJTi...=
                    },
                    {
                      "name": "OMNI_MTLS_CLIENT_PRIVATE_KEY",
                      "value": LS0tLS1CRUdJTiBQU...=
                    }
                  ]
                }
                """;
        var request = new HttpEntity<>(payload, headers);
        var response = testRestTemplate.exchange(
                "/v2/nvcf/functions",
                HttpMethod.POST,
                request,
                String.class);

        assertThat(response).isNotNull();
        assertThat(response.getStatusCode()).isNotNull();
        assertThat(response.getStatusCode().value()).isEqualTo(400);
    }
}
