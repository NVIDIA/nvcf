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
import static com.nvidia.nvcf.rest.function.management.dto.ProtocolEnum.HTTP;
import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LIST_FUNCTIONS_DETAILS;
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_URI;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NAME_V1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NAME_V2;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.fail;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.TestAuthorizedPartiesService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionIdsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.TestUtil;
import java.net.URI;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.function.Supplier;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.SneakyThrows;
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
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ProblemDetail;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class PublicFunctionManagementTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private AuthorizedPartiesService authPartiesService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private JsonMapper jsonMapper;

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

    @BeforeEach
    void beforeEach() {
        // Create an account for public functions.
        testAccountService.createAccountAndAssociateClients(TEST_PUBLIC_FUNCTION_NCA_ID, null);

        // Create two function versions of TEST_PUBLIC_FUNCTION_ID_1 in account
        // TEST_PUBLIC_FUNCTION_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_PUBLIC_FUNCTION_ID_1,
                                                  TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                                  TEST_PUBLIC_FUNCTION_NCA_ID,
                                                  TEST_PUBLIC_FUNCTION_NAME_V1);
        testAuthPartiesService.createTestFunction(TEST_PUBLIC_FUNCTION_ID_1,
                                                  TEST_PUBLIC_FUNCTION_VERSION_ID_2,
                                                  TEST_PUBLIC_FUNCTION_NCA_ID,
                                                  TEST_PUBLIC_FUNCTION_NAME_V2);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_PUBLIC_FUNCTION_NCA_ID,
                                                    TEST_PUBLIC_FUNCTION_ID_1,
                                                    Optional.empty(),
                                                    authorizedParties1);
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
        testAccountService.deleteAccount(TEST_PUBLIC_FUNCTION_NCA_ID);
    }

    @SneakyThrows
    @Test
    void shouldFailToCreateVersionOfOwnPublicFunction() {
        // Account Admin cannot perform this operation.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION), 100);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        assertThat(authPartiesService.isFunctionPublic(TEST_FUNCTION_ID)).isTrue();

        // Try creating a version of the public function.
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME_2)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .healthUri(TEST_HEALTH_URI)
                .build();
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions/"
                                                                  + TEST_FUNCTION_ID + "/versions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot create version of a public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    Stream<Arguments> listFunctionsArgs() {
        return Stream.of(
                // Create token/key for Account Admin of TEST_NCA_ID to list functions.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             Set.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2,
                                    TEST_PUBLIC_FUNCTION_ID_1),
                             Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2),
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_FUNCTIONS_DETAILS), 100),
                             Set.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2,
                                    TEST_PUBLIC_FUNCTION_ID_1),
                             Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2),
                             HttpStatus.OK),
                // If a non-existent-client is used as subject to create a JWT, then it means
                // there is no corresponding account. When used as a bearer token, it will result
                // in a 404/Not Found response.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("non-existent-client",
                                                             List.of(SCOPE_LIST_FUNCTIONS_DETAILS), 100),
                             null,
                             null,
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2,
                                    TEST_PUBLIC_FUNCTION_ID_1),
                             Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2),
                             HttpStatus.OK),
                // Even though apikey contains "non-existent-account" as NCA Id, the expected
                // response should be HttpStatus.OK as we do not validate NCA Id. This allows users
                // of Personal Orgs to invoke/list/check-queue-depth for public functions
                // successfully during their trial period. Note that the expected list of functions
                // will only contain public function ids/versionIds as there are no functions
                // defined in the non-existent-account. Also, note that the response will have a
                // 200/OK status. This is different from JWT as mentioned above.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-account", TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(TEST_PUBLIC_FUNCTION_ID_1),
                             Set.of(TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2),
                             HttpStatus.OK)
        );
    }


    @ParameterizedTest
    @MethodSource("listFunctionsArgs")
    void shouldListAllFunctions(
            Object tokenSupplier,
            Set<UUID> expectedFunctionIds,
            Set<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        // The list should include: a) own functions, b) authorized functions, c) public functions

        // Get a token for Account Admin of TEST_NCA_ID to list functions.
        var token = TestUtil.getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID_2 in account TEST_NCA_ID_2.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        // Authorize TEST_NCA_ID account for TEST_FUNCTION_ID_2 created under TEST_NCA_ID_2.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(TEST_NCA_ID).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID_2, TEST_FUNCTION_ID_2,
                                                    Optional.empty(), authorizedParties1);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Try retrieving functions.
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/functions"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListFunctionsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).hasSize(expectedFunctionVersionIds.size()); // 2 public, 1 authorized, 1 own
        assertThat(responseBody.functions().stream()
                           .map(FunctionDto::id).distinct().toList())
                .containsExactlyInAnyOrderElementsOf(expectedFunctionIds);
        var functions = responseBody.functions().stream()
                .collect(Collectors.toMap(FunctionDto::versionId, Function.identity()));
        assertThat(functions.keySet()).containsExactlyInAnyOrderElementsOf(expectedFunctionVersionIds);
        for (UUID expectedFunctionId : expectedFunctionVersionIds) {
            var functionDto = functions.get(expectedFunctionId);
            assertThat(functionDto).isNotNull();
            assertThat(functionDto.description()).isNotEmpty();
            assertThat(functionDto.tags()).isNotEmpty();
            assertThat(functionDto.health()).isNotNull();
            // we should never provide details in list functions request
            assertThat(functionDto.activeInstances()).isNull();
            if (functionDto.id().equals(TEST_FUNCTION_ID)) { // Own functions
                assertThat(functionDto.versionId()).isEqualTo(TEST_VERSION_ID_1);
                assertThat(functionDto.ownedByDifferentAccount()).isNull();
                assertThat(functionDto.ncaId()).isEqualTo(TEST_NCA_ID);
                assertThat(functionDto.inferenceUrl()).isNotNull();
                assertThat(functionDto.containerArgs()).isNotBlank();
                assertThat(functionDto.containerImage()).isNotNull();
            } else if (functionDto.id().equals(TEST_FUNCTION_ID_2)) { // Authorized functions
                assertThat(functionDto.versionId()).isEqualTo(TEST_VERSION_ID_2);
                assertThat(functionDto.ownedByDifferentAccount()).isTrue();
                assertThat(functionDto.ncaId()).isEqualTo(TEST_NCA_ID_2);
                assertThat(functionDto.inferenceUrl()).isNull();
                assertThat(functionDto.inferencePort()).isNull();
                assertThat(functionDto.containerArgs()).isBlank();
                assertThat(functionDto.containerImage()).isNull();
                assertThat(functionDto.containerEnvironment()).isNull();
                assertThat(functionDto.helmChart()).isNull();
                assertThat(functionDto.helmChartServiceName()).isBlank();
                assertThat(functionDto.models()).isNull();
                assertThat(functionDto.resources()).isNull();
            } else if (functionDto.id().equals((TEST_PUBLIC_FUNCTION_ID_1))) { // Public functions
                assertThat(functionDto.versionId()).isIn(expectedFunctionVersionIds);
                assertThat(functionDto.ownedByDifferentAccount()).isTrue();
                assertThat(functionDto.ncaId()).isEqualTo(TEST_PUBLIC_FUNCTION_NCA_ID);
                assertThat(functionDto.inferenceUrl()).isNull();
                assertThat(functionDto.inferencePort()).isNull();
                assertThat(functionDto.containerArgs()).isBlank();
                assertThat(functionDto.containerImage()).isNull();
                assertThat(functionDto.containerEnvironment()).isNull();
                assertThat(functionDto.helmChart()).isNull();
                assertThat(functionDto.helmChartServiceName()).isBlank();
                assertThat(functionDto.models()).isNull();
                assertThat(functionDto.resources()).isNull();
            } else {
                fail("Unknown function returned");
            }
        }
    }

    @ParameterizedTest
    @MethodSource("listFunctionsArgs")
    void shouldListIdsOfAllFunctions(
            Object tokenSupplier,
            Set<UUID> expectedFunctionIds,
            Set<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        // The list should include ids of --
        //              a) own functions, b) authorized functions, c) public functions

        // Get a token for Account Admin of TEST_NCA_ID to list functions.
        var token = TestUtil.getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID_2 in account TEST_NCA_ID_2.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        // Authorize TEST_NCA_ID account for TEST_FUNCTION_ID_2 created under TEST_NCA_ID_2.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(TEST_NCA_ID).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID_2, TEST_FUNCTION_ID_2,
                                                    Optional.empty(), authorizedParties1);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Try retrieving function ids.
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/functions/ids"))
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
        assertThat(responseBody.functionIds()).hasSize(expectedFunctionIds.size());
        assertThat(responseBody.functionIds())
                .containsExactlyInAnyOrderElementsOf(expectedFunctionIds);
    }

    Stream<Arguments> listFunctionVersionArgs() {
        return Stream.of(
                // Create token/key for Account Admin of TEST_NCA_ID to list functions.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             Set.of(TEST_PUBLIC_FUNCTION_ID_1),
                             Set.of(TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2),
                             HttpStatus.OK),
                // If a non-existent-client is used as subject to create a JWT, then it means
                // there is no corresponding account. When used as a bearer token, it will result
                // in a 404/Not Found response.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("non-existent-client",
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             null,
                             null,
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(TEST_PUBLIC_FUNCTION_ID_1),
                             Set.of(TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2),
                             HttpStatus.OK),
                // Even though apikey contains "non-existent-account" as NCA Id, the expected
                // response should be HttpStatus.OK as we do not validate NCA Id. This allows users
                // of Personal Orgs to invoke/list/check-queue-depth for public functions
                // successfully during their trial period. Note that the expected list of functions
                // will only contain public function ids/versionIds as there are no functions
                // defined in the non-existent-account. Also, note that the response will have a
                // 200/OK status. This is different from JWT as mentioned above.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-account", TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             Set.of(TEST_PUBLIC_FUNCTION_ID_1),
                             Set.of(TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2),
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("listFunctionVersionArgs")
    void shouldListAllVersionsOfPublicFunction(
            Object tokenSupplier,
            Set<UUID> expectedFunctionIds,
            Set<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        // Create a token for Account Admin of TEST_NCA_ID to list functions.
        var token = TestUtil.getToken(tokenSupplier);

        // Retrieve public function versions created under TEST_PUBLIC_FUNCTION_NCA_ID account.
        var requestEntity = RequestEntity
                .get(URI.create("/v2/nvcf/functions/" + TEST_PUBLIC_FUNCTION_ID_1 + "/versions"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListFunctionsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).hasSize(expectedFunctionVersionIds.size()); // 2 public
        assertThat(responseBody.functions().stream()
                           .map(FunctionDto::id).distinct().toList())
                .containsExactlyInAnyOrderElementsOf(expectedFunctionIds);
        var functions = responseBody.functions().stream()
                .collect(Collectors.toMap(FunctionDto::versionId, Function.identity()));
        assertThat(functions.keySet()).containsExactlyInAnyOrderElementsOf(expectedFunctionVersionIds);
        for (UUID expectedFunctionId : expectedFunctionVersionIds) {
            var functionDto = functions.get(expectedFunctionId);
            assertThat(functionDto).isNotNull();
            // we should not provide details in list functions request
            assertThat(functionDto.activeInstances()).isNull();
            if (functionDto.id().equals((TEST_PUBLIC_FUNCTION_ID_1))) { // Public functions
                assertThat(functionDto.versionId()).isIn(expectedFunctionVersionIds);
                assertThat(functionDto.ownedByDifferentAccount()).isTrue();
                assertThat(functionDto.ncaId()).isEqualTo(TEST_PUBLIC_FUNCTION_NCA_ID);
                assertThat(functionDto.inferenceUrl()).isNull();
                assertThat(functionDto.inferencePort()).isNull();
                assertThat(functionDto.containerArgs()).isBlank();
                assertThat(functionDto.containerImage()).isNull();
                assertThat(functionDto.helmChart()).isNull();
                assertThat(functionDto.helmChartServiceName()).isBlank();
                assertThat(functionDto.models()).isNull();
                assertThat(functionDto.resources()).isNull();
                assertThat(functionDto.description()).isNotEmpty();
                assertThat(functionDto.tags()).isNotEmpty();
                assertThat(functionDto.health()).isNotNull();
            } else {
                fail("Unknown function returned");
            }
        }
    }

    @ParameterizedTest
    @MethodSource("listFunctionVersionArgs")
    void shouldListSpecificVersionOfPublicFunction(
            Object tokenSupplier,
            Set<UUID> expectedFunctionIds,
            Set<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        // Create a token for Account Admin of TEST_NCA_ID to list functions.
        var token = TestUtil.getToken(tokenSupplier);

        // Retrieve public functions that were created under TEST_PUBLIC_FUNCTION_NCA_ID account.
        var requestEntity = RequestEntity
                .get(URI.create("/v2/nvcf/functions/" + TEST_PUBLIC_FUNCTION_ID_1
                                        + "/versions/" + TEST_PUBLIC_FUNCTION_VERSION_ID_1))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();

        var functionDto = responseBody.function();
        assertThat(functionDto.id()).isEqualTo(TEST_PUBLIC_FUNCTION_ID_1);
        assertThat(functionDto.versionId()).isEqualTo(TEST_PUBLIC_FUNCTION_VERSION_ID_1);
        assertThat(functionDto.ncaId()).isEqualTo(TEST_PUBLIC_FUNCTION_NCA_ID);
        assertThat(functionDto.ownedByDifferentAccount()).isTrue();
        assertThat(functionDto.inferenceUrl()).isNull();
        assertThat(functionDto.inferencePort()).isNull();
        assertThat(functionDto.containerArgs()).isBlank();
        assertThat(functionDto.containerImage()).isNull();
        assertThat(functionDto.containerEnvironment()).isNull();
        assertThat(functionDto.helmChart()).isNull();
        assertThat(functionDto.helmChartServiceName()).isBlank();
        assertThat(functionDto.models()).isNull();
        assertThat(functionDto.resources()).isNull();
        assertThat(functionDto.activeInstances()).isNull();
        assertThat(functionDto.description()).isNotEmpty();
        assertThat(functionDto.tags()).isNotEmpty();
        assertThat(functionDto.health()).isNotNull();
        assertThat(responseBody.function().health().getProtocol()).isEqualTo(HTTP);
        assertThat(responseBody.function().health().getUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
        assertThat(responseBody.function().health().getExpectedStatusCode())
                .isEqualTo(EXPECTED_STATUS_CODE);
    }

    @SneakyThrows
    @Test
    void shouldFailToDeleteOwnPublicFunction() {
        // Account Admin cannot perform this operation.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DELETE_FUNCTION), 100);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(), authorizedParties1);

        // Try deleting the public function.
        var requestEntity = RequestEntity
                .delete(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                           + "/versions/" + TEST_VERSION_ID_1))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot delete a public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    @SneakyThrows
    @Test
    void shouldListPublicAndPrivateVersionsOfFunction() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_LIST_FUNCTIONS), 100);

        // Create two versions of function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a version level authorized party
        // with TEST_VERSION_ID_1 to make it public. TEST_VERSION_ID_2 will stay private.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties1);

        // Try listing both versions of TEST_FUNCTION_ID.
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/functions/"
                                                                 + TEST_FUNCTION_ID + "/versions"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListFunctionsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var dtos = responseBody.functions();
        assertThat(dtos).hasSize(2);

        for (var dto: dtos) {
            assertThat(dto.id()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(dto.versionId()).isIn(Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2));
            assertThat(dto.ncaId()).isEqualTo(TEST_NCA_ID);
            // we should not provide details in list functions request
            assertThat(dto.activeInstances()).isNull();
        }
    }

}
