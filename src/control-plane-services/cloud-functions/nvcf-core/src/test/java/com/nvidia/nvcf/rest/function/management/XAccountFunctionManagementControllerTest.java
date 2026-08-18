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
import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.ApiBodyFormatEnum.PREDICT_V2;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_UPDATE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_URI;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.fail;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.HealthDto;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionIdsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ProtocolEnum;
import com.nvidia.nvcf.rest.function.management.dto.RateLimitDto;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import jakarta.annotation.Nullable;
import java.net.URI;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
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

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class XAccountFunctionManagementControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestManagementService testManagementService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

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
    }

    Stream<Arguments> listArgs() {
        return Stream.of(
                Arguments.of(null, TEST_NCA_ID, null, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", TEST_NCA_ID, null, HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             TEST_NCA_ID,
                             null,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION), 100),
                             TEST_NCA_ID,
                             null,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("list_functions", "list_functions_any"),
                                                             100),
                             TEST_NCA_ID,
                             null,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_NCA_ID,
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2),
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS), 100),
                             TEST_NCA_ID,
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2),
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS_DETAILS), 100),
                             "badNcaId",
                             null,
                             HttpStatus.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("listArgs")
    void shouldListFunctionsByAccount(
            Object tokenSupplier,
            String ncaId,
            @Nullable List<UUID> expectedFunctions,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        createTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);
        createTestEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        var builder = RequestEntity.get(
                URI.create("/v2/nvcf/accounts/" + ncaId + "/functions"));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
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
    @MethodSource("listArgs")
    void shouldListFunctionIdsByAccount(
            Object tokenSupplier,
            String ncaId,
            @Nullable List<UUID> expectedFunctions,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create functions in the same account. Function with TEST_FUNCTION_ID has
        // two versions.
        createTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);
        createTestEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        createTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_3, TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        var builder = RequestEntity.get(
                URI.create("/v2/nvcf/accounts/" + ncaId + "/functions/ids"));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
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
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                            ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NCA_ID, TEST_FUNCTION_ID,
                             HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                            ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NCA_ID, TEST_FUNCTION_ID,
                             HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                            ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NCA_ID, TEST_FUNCTION_ID,
                             HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("random-oauth2-client-id",
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                            ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NCA_ID, TEST_FUNCTION_ID,
                             HttpStatus.OK, List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_NCA_ID, TEST_FUNCTION_ID, HttpStatus.FORBIDDEN, null),
                Arguments.of(null, TEST_NCA_ID, TEST_FUNCTION_ID, HttpStatus.UNAUTHORIZED, null)
        );
    }

    @ParameterizedTest
    @MethodSource("functionListArgsByFunction")
    void shouldListVersionsByFunction(
            Object tokenSupplier,
            String ncaId,
            UUID functionId,
            HttpStatus expectedStatus,
            List<UUID> expectedFunctionVersionIds) {
        var token = getToken(tokenSupplier);

        createTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);
        createTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_2, TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        createTestEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_3, TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/accounts/" + ncaId
                                                     + "/functions/" + functionId
                                                     + "/versions"))
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

    Stream<Arguments> functionCreateArgs() {
        return Stream.of(
                Arguments.of(null, HttpStatus.UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION), 100),
                             HttpStatus.OK));
    }

    @ParameterizedTest
    @MethodSource("functionCreateArgs")
    void shouldCreateFunction(
            Object tokenSupplier,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        var customUtilsContainerImageUri = "stg.nvcr.io/nv-cf/nvcf-core/custom-worker-utils:9.9.9";
        HealthDto healthDto = HealthDto.builder()
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
                .utilsContainerImage(customUtilsContainerImageUri)
                .healthUri(TEST_HEALTH_URI)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();
        var builder = RequestEntity.post(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/functions"))
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(requestBody);
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
        assertThat(responseBody.function().containerImage()).isEqualTo(TEST_NGC_CONTAINER_IMAGE);
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().containerEnvironment()).isNull();
        assertThat(responseBody.function().models()).isNull();
        assertThat(responseBody.function().healthUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);
        assertThat(responseBody.function().health()).isNotNull();
        assertThat(responseBody.function().health().getProtocol()).isEqualTo(ProtocolEnum.HTTP);
        assertThat(responseBody.function().health().getUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
        assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                EXPECTED_STATUS_CODE);
        var versionId = responseBody.function().versionId();
        var entity = functionsRepository.getByFunctionVersionId(versionId)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getUtilsContainerImage()).isNotBlank()
                .isEqualTo(customUtilsContainerImageUri);
        assertThat(entity.getFunctionType()).isEqualTo(FunctionType.DEFAULT);
    }

    Stream<Arguments> functionCreateVersionArgs() {
        return Stream.of(
                Arguments.of(null, TEST_FUNCTION_ID, HttpStatus.UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION), 100),
                             TEST_FUNCTION_ID_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             HttpStatus.OK));
    }

    @ParameterizedTest
    @MethodSource("functionCreateVersionArgs")
    void shouldCreateFunctionVersion(
            Object tokenSupplier,
            UUID functionId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create the original function.
        testManagementService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME);

        var customUtilsContainerImageUri = "stg.nvcr.io/nv-cf/nvcf-core/custom-worker-utils:9.9.9";
        HealthDto healthDto = HealthDto.builder()
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
                .utilsContainerImage(customUtilsContainerImageUri)
                .healthUri(TEST_HEALTH_URI)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();
        var builder = RequestEntity.post(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID +
                                           "/functions/" + functionId + "/versions"))
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(requestBody);
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
        assertThat(responseBody.function().containerImage()).isEqualTo(TEST_NGC_CONTAINER_IMAGE);
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().containerEnvironment()).isNull();
        assertThat(responseBody.function().models()).isNull();
        assertThat(responseBody.function().healthUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);
        assertThat(responseBody.function().health()).isNotNull();
        assertThat(responseBody.function().health().getProtocol()).isEqualTo(ProtocolEnum.HTTP);
        assertThat(responseBody.function().health().getUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
        assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                EXPECTED_STATUS_CODE);
        var versionId = responseBody.function().versionId();
        var entity = functionsRepository.getByFunctionVersionId(versionId)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getUtilsContainerImage()).isNotBlank()
                .isEqualTo(customUtilsContainerImageUri);
        assertThat(entity.getFunctionType()).isEqualTo(FunctionType.DEFAULT);
    }

    Stream<Arguments> functionDetailsArgs() {
        return Stream.of(
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("list_functions"), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             HttpStatus.NOT_FOUND));
    }

    @ParameterizedTest
    @MethodSource("functionDetailsArgs")
    void shouldGetFunctionDetails(
            Object tokenSupplier,
            UUID functionId,
            UUID functionVersionId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create a function.
        createTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);

        var builder = RequestEntity.get(
                URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID +
                                   "/functions/" + functionId +
                                   "/versions/" + functionVersionId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().ownedByDifferentAccount()).isNull();
    }

    Stream<Arguments> deleteArgs() {
        return Stream.of(
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("delete_function"), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DELETE_FUNCTION), 100),
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_3,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DELETE_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DELETE_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NO_CONTENT)
                        );
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
        createTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Create a version of TEST_FUNCTION_ID_2 in TEST_NCA_ID_2 account.
        createTestEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        var builder = RequestEntity.delete(
                URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                   + "/functions/" + functionId
                                   + "/versions/" + functionVersionId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
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
        return Stream.of(
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_TAGS_2, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1, TEST_TAGS_2, HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("register_function"), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION), 100),
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             TEST_TAGS_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             TEST_TAGS_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             TEST_TAGS_2,
                             HttpStatus.OK),
                // weird tag characters
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Set.of("\n&abc123["),
                             HttpStatus.BAD_REQUEST),
                // tags with special namespace:key=value
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_UPDATE_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             Set.of("namespace:key=value"),
                             HttpStatus.OK)
        );
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
        createTestEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Create a version of TEST_FUNCTION_ID_2 in TEST_NCA_ID_2 account.
        createTestEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        var requestBody = UpdateFunctionRequest.builder()
                .tags(tags)
                .build();
        var builder = RequestEntity.put(
                URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                   + "/metadata"
                                   + "/functions/" + functionId
                                   + "/versions/" + functionVersionId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        var functionEntity = functionsRepository.getByFunctionVersionId(functionVersionId);
        if (responseEntity.getStatusCode().isError()) {
            return;
        }

        var function = responseEntity.getBody().function();
        assertThat(function).isNotNull();
        assertThat(function.health()).isNotNull();
        assertThat(function.healthUri()).isNotNull();

        assertThat(functionEntity.isPresent()).isTrue();
        assertThat(functionEntity.get().getTags()).isEqualTo(tags);
        assertThat(functionEntity.get().getHealth()).isNotNull();
    }

    Stream<Arguments> functionCreateWithRateLimitArgs() {
        Set<String> tooManyNcaIds = new HashSet<>();
        for (int i = 1; i <= 33; i++) {
            tooManyNcaIds.add(TEST_NCA_ID + i);
        }

        return Stream.of(
                // valid ratelimit config
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .rateLimit("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .syncCheck(true)
                                     .perNcaIdRate(Map.of(TEST_NCA_ID_2, "3-M"))
                                     .build(),
                             HttpStatus.OK),
                // with only global rate configs
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .rateLimit("4-S")
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .syncCheck(true)
                                     .build(),
                             HttpStatus.OK),
                // with only per nca id rate configs
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .perNcaIdRate(Map.of(TEST_NCA_ID_2, "3-M"))
                                     .build(),
                             HttpStatus.OK),
                // no nca id exemptions
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("4-S").build(),
                             HttpStatus.OK),
                // empty limit string
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("").build(),
                             HttpStatus.BAD_REQUEST),
                // bad rate limit string
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("1S").build(),
                             HttpStatus.BAD_REQUEST),
                // zero rate limit
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("0-S").build(),
                             HttpStatus.BAD_REQUEST),
                // no global rate limit nor per nca id config
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().build(),
                             HttpStatus.BAD_REQUEST),
                // too many exempted nca ids
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder().rateLimit("4-S").exemptedNcaIds(tooManyNcaIds).build(),
                             HttpStatus.BAD_REQUEST),
                // per-ncaid rate ncaid cannot be in exemptedNcaIds
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
                                                             100),
                             RateLimitDto.builder()
                                     .perNcaIdRate(Map.of(TEST_NCA_ID, "3-M"))
                                     .exemptedNcaIds(Set.of(TEST_NCA_ID))
                                     .build(),
                             HttpStatus.BAD_REQUEST),
                // exemptedNcaIds cannot exist without rateLimit
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_REGISTER_FUNCTION,
                                                          ADMIN_SCOPE_LIST_FUNCTIONS),
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
            String token, RateLimitDto rateLimitDto, HttpStatus expectedStatus) {
        var customUtilsContainerImageUri = "stg.nvcr.io/nv-cf/nvcf-core/custom-worker-utils:9.9.9";
        HealthDto healthDto = HealthDto.builder()
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
                .utilsContainerImage(customUtilsContainerImageUri)
                .healthUri(TEST_HEALTH_URI)
                .rateLimit(rateLimitDto)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(healthDto)
                .build();
        var builder = RequestEntity.post(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/functions"))
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }

    private FunctionEntity createTestEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name) {
        return createTestEntity(id, versionId, ncaId, name, FunctionStatus.INACTIVE);
    }

    private FunctionEntity createTestEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name,
            FunctionStatus status) {
        var entity = FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(status)
                .ncaId(ncaId)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .utilsContainerImage(GO)
                .modelSpecs(Map.of(
                        "model-1", "{\"version\":\"1.0\",\"url\":\"" + TEST_MODEL_URL_1 + "\"}",
                        "model-2", "{\"version\":\"2.0\",\"url\":\"" + TEST_MODEL_URL_1 + "\"}"))
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .build();

        functionsRepository.save(entity);
        return entity;
    }

    @Test
    void shouldCreateStreamingFunction() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_REGISTER_FUNCTION), 100);
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .functionType(FunctionTypeEnum.STREAMING)
                .build();
        var requestEntity = RequestEntity.post(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/functions"))
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

}
