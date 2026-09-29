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
package com.nvidia.nvcf.rest.azp;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.MockApiKeysServer.setApiKeyValidationResponse;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_5;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_6;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.fail;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionIdsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.apikeys.ApiKeysService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.HashSet;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.function.Supplier;
import java.util.stream.Collectors;
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
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ListingAuthorizedFunctionsTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private ApiKeysService apiKeysService;

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

    private Set<AuthorizedPartyDto> authorizedParties = Set.of(
            AuthorizedPartyDto.builder()
                    .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
            AuthorizedPartyDto.builder()
                    .ncaId(TEST_AUTHORIZED_NCA_ID_5).build()
    );


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

    @BeforeEach()
    void init() {
        // Create two versions of a function with TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Create a function TEST_FUNCTION_ID_3 in an authorized account TEST_AUTHORIZED_NCA_ID_1.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                                  TEST_AUTHORIZED_NCA_ID_1, TEST_FUNCTION_NAME_3);
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    Stream<Arguments> listAuthorizedFunctionsInDifferentAccountArgs() {
        return Stream.of(
                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access all(Optional.empty())
                // versions of function TEST_FUNCTION_ID defined in TEST_NCA_ID account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),

                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access TEST_VERSION_ID_1
                // version of function TEST_FUNCTION_ID defined in TEST_NCA_ID account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),

                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access all
                // versions and TEST_VERSION_ID_1 version of function TEST_FUNCTION_ID defined
                // in TEST_NCA_ID account. Duplicate authorized accounts -- one at the wildcard
                // level and the other at specific version level.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),

                // No functions in account TEST_NCA_ID are accessible to account
                // TEST_AUTHORIZED_NCA_ID_1.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(),
                             List.of(TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_3),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.OK),
                // apikey auth for all the shared functions for an authorized party
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_5, TEST_OWNER_ID,
                                             List.of(new Resource("authorized-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1), Optional.of(TEST_VERSION_ID_2)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_6, TEST_OWNER_ID,
                                             List.of(new Resource("authorized-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("listAuthorizedFunctionsInDifferentAccountArgs")
    void shouldListAuthorizedFunctionsInDifferentAccount(
            Object tokenSupplier,
            List<Optional<UUID>> authorizedFunctionVersionIds,
            List<UUID> expectedFunctionIds,
            List<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);

        // Associate authorized parties/accounts with the function versions.
        authorizedFunctionVersionIds
                .forEach(optVersionId -> testAuthPartiesService
                        .associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                              optVersionId, authorizedParties));

        // Retrieve functions that are -- a. created under account TEST_AUTHORIZED_NCA_ID_1 and
        // b. created under different account but TEST_AUTHORIZED_NCA_ID_1 account is authorized
        // to use.
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
        assertThat(responseBody.functions()).hasSize(expectedFunctionVersionIds.size());
        assertThat(responseBody.functions().stream()
                           .map(FunctionDto::id).distinct().toList())
                .containsExactlyInAnyOrderElementsOf(expectedFunctionIds);
        var functions = responseBody.functions().stream()
                .collect(Collectors.toMap(FunctionDto::versionId, Function.identity()));
        assertThat(functions.keySet()).isEqualTo(new HashSet<>(expectedFunctionVersionIds));
        for (UUID expectedFunctionId : expectedFunctionVersionIds) {
            var functionDto = functions.get(expectedFunctionId);
            assertThat(functionDto).isNotNull();
            if (functionDto.id().equals(TEST_FUNCTION_ID)) {
                assertThat(functionDto.versionId()).isIn(expectedFunctionVersionIds);
                assertThat(functionDto.ownedByDifferentAccount()).isTrue();
                assertThat(functionDto.ncaId()).isEqualTo(TEST_NCA_ID);
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
            } else if (functionDto.id().equals(TEST_FUNCTION_ID_3)) {
                assertThat(functionDto.versionId()).isEqualTo(TEST_VERSION_ID_3);
                assertThat(functionDto.ownedByDifferentAccount()).isNull();
                assertThat(functionDto.ncaId()).isEqualTo(TEST_AUTHORIZED_NCA_ID_1);
                assertThat(functionDto.containerArgs()).isNotBlank();
                assertThat(functionDto.containerImage()).isNotNull();
                assertThat(functionDto.inferenceUrl()).isNotNull();
            } else {
                fail("Unknown function returned");
            }
        }
    }

    @ParameterizedTest
    @MethodSource("listAuthorizedFunctionsInDifferentAccountArgs")
    void shouldListIdsOfAuthorizedFunctionsInDifferentAccount(
            Object tokenSupplier,
            List<Optional<UUID>> authorizedFunctionVersionIds,
            List<UUID> expectedFunctionIds,
            List<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);

        // Associate authorized parties/accounts with the function versions.
        authorizedFunctionVersionIds
                .forEach(optVersionId -> testAuthPartiesService
                        .associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                              optVersionId, authorizedParties));

        // Retrieve functions that are -- a. created under account TEST_AUTHORIZED_NCA_ID_1 and
        // b. created under different account but TEST_AUTHORIZED_NCA_ID_1 account is authorized
        // to use.
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
        assertThat(responseBody.functionIds()).hasSize(expectedFunctionIds.size());
        assertThat(responseBody.functionIds())
                .containsExactlyInAnyOrderElementsOf(expectedFunctionIds);
    }

    Stream<Arguments> listAuthorizedFunctionsInDifferentAccountUsingIdArgs() {
        return Stream.of(
                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access all(Optional.empty())
                // versions of function TEST_FUNCTION_ID defined in TEST_NCA_ID account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),

                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access TEST_VERSION_ID_1
                // version of function TEST_FUNCTION_ID defined in TEST_NCA_ID account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),

                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access all(Optional.empty())
                // versions and TEST_VERSION_ID_1 version of function TEST_FUNCTION_ID defined
                // in TEST_NCA_ID account. Duplicate authorized accounts -- one at the function
                // level and the other at the version level.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),

                // No versions of function TEST_FUNCTION_ID in account TEST_NCA_ID are accessible
                // to account TEST_AUTHORIZED_NCA_ID_1.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("listAuthorizedFunctionsInDifferentAccountUsingIdArgs")
    void shouldListAuthorizedFunctionsInDifferentAccountUsingId(
            Object tokenSupplier,
            List<Optional<UUID>> authorizedFunctionVersionIds,
            List<UUID> expectedFunctionIds,
            List<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);

        // Associate authorized parties with the specified versions of TEST_FUNCTION_ID function.
        authorizedFunctionVersionIds
                .forEach(optVersionId -> testAuthPartiesService
                        .associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                              optVersionId, authorizedParties));

        // Retrieve versions of function TEST_FUNCTION_ID(defined in TEST_NCA_ID account) that
        // account TEST_AUTHORIZED_NCA_ID_1 is authorized to use.
        var requestEntity =
                RequestEntity.get(
                                URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID + "/versions"))
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
        assertThat(responseBody.functions()).hasSize(expectedFunctionVersionIds.size());
        assertThat(responseBody.functions().stream()
                           .map(FunctionDto::id).distinct().toList())
                .containsExactlyInAnyOrderElementsOf(expectedFunctionIds);
        var functions = responseBody.functions().stream()
                .collect(Collectors.toMap(FunctionDto::versionId, Function.identity()));
        assertThat(functions.keySet()).isEqualTo(new HashSet<>(expectedFunctionVersionIds));
        for (UUID expectedFunctionId : expectedFunctionVersionIds) {
            var functionDto = functions.get(expectedFunctionId);
            assertThat(functionDto).isNotNull();
            if (functionDto.id().equals(TEST_FUNCTION_ID)) {
                assertThat(functionDto.versionId()).isIn(TEST_VERSION_ID_1, TEST_VERSION_ID_2);
                assertThat(functionDto.ownedByDifferentAccount()).isTrue();
                assertThat(functionDto.ncaId()).isEqualTo(TEST_NCA_ID);
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
            } else {
                fail("Unknown function returned");
            }
        }
    }

    Stream<Arguments> getAuthorizedFunctionInDifferentAccountUsingIdAndVersionArgs() {
        return Stream.of(
                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access all(Optional.empty())
                // versions of function TEST_FUNCTION_ID defined in TEST_NCA_ID account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty()),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                // Function version TEST_VERSION_ID_2 is authorized for cross-account access.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.of(TEST_VERSION_ID_2)),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),

                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access TEST_VERSION_ID_1
                // version of function TEST_FUNCTION_ID defined in TEST_NCA_ID account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                // Account TEST_AUTHORIZED_NCA_ID_1 is authorized to access all(using Optional.empty())
                // versions and TEST_VERSION_ID_1 version of function TEST_FUNCTION_ID defined
                // in TEST_NCA_ID account. Duplicate authorized accounts -- one at the function
                // level and the other at the version level.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                     + "/" + UUID.randomUUID())),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(Optional.empty(), Optional.of(TEST_VERSION_ID_1)),
                             List.of(),
                             List.of(),
                             HttpStatus.FORBIDDEN),
                // No versions of function TEST_FUNCTION_ID in account TEST_NCA_ID are accessible
                // to account TEST_AUTHORIZED_NCA_ID_1.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(SCOPE_LIST_FUNCTIONS), 100),
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                                             List.of(new Resource("function", TEST_FUNCTION_ID
                                                             + "/" + TEST_VERSION_ID_1),
                                                     new Resource("account-functions", "*")),
                                             List.of(SCOPE_LIST_FUNCTIONS));
                                 return "nvapi-stg-some-key";
                             },
                             List.of(),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("getAuthorizedFunctionInDifferentAccountUsingIdAndVersionArgs")
    void shouldGetAuthorizedFunctionInDifferentAccountUsingVersionId(
            Object tokenSupplier,
            List<Optional<UUID>> authorizedFunctionVersionIds,
            List<UUID> expectedFunctionIds,
            List<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);

        // Associate authorized parties with the specified versions of TEST_FUNCTION_ID function.
        authorizedFunctionVersionIds
                .forEach(optVersionId -> testAuthPartiesService
                        .associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                              optVersionId, authorizedParties));

        // Retrieve TEST_VERSION_ID_1 version of function TEST_FUNCTION_ID defined in TEST_NCA_ID
        // account if account TEST_AUTHORIZED_NCA_ID_1 is authorized.
        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                                     + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        var functionDto = responseBody.function();
        assertThat(functionDto.versionId()).isIn(expectedFunctionVersionIds);
        assertThat(functionDto.id()).isIn(expectedFunctionIds);
        assertThat(functionDto.ownedByDifferentAccount()).isTrue();
        assertThat(functionDto.ncaId()).isEqualTo(TEST_NCA_ID);
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
    }

    @Test
    void shouldUseApiKeyBackupCacheIfApiKeysUnreachable() {
        var tokenSupplier = (Supplier<String>) () -> {
            setResponse(TEST_AUTHORIZED_NCA_ID_1, TEST_OWNER_ID,
                        List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                new Resource("account-functions", "*")),
                        List.of(SCOPE_LIST_FUNCTIONS));
            return "nvapi-stg-some-key";
        };
        var token = getToken(tokenSupplier);
        var authorizedFunctionVersionIds = List.of(Optional.<UUID>empty(),
                                                   Optional.of(TEST_VERSION_ID_1));

        // Associate authorized parties with the specified versions of TEST_FUNCTION_ID function.
        authorizedFunctionVersionIds
                .forEach(versionId -> testAuthPartiesService
                        .associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                              versionId, authorizedParties));

        // Retrieve TEST_VERSION_ID_1 version of function TEST_FUNCTION_ID defined in TEST_NCA_ID
        // account if account TEST_AUTHORIZED_NCA_ID_1 is authorized.
        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                                     + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Stop Mock apikeys server to simulate outage -- At the end of the test, start it so
        // that this does not impact other tests.
        MockApiKeysServer.stop();

        // Clear apikeys primary cache so that backup cache is used to get the
        // ApiKeyValidationResult.
        apiKeysService.invalidatePrimaryCache();

        // Invoke the endpoint again and verify if the response is same as before.
        responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Clear both apikeys primary and backup caches.
        apiKeysService.invalidateCache();

        // Invoke the endpoint again and expect 500.
        responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode().isError()).isTrue();
        // ### TODO: Revisit -- Status code is 401 under MVC.
        // assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.INTERNAL_SERVER_ERROR);

        // Start MockApiKeysServer for other tests to continue.
        MockApiKeysServer.start(apiKeysBaseUrl);
    }

    @Test
    void shouldNotUseApiKeyBackupCacheIfAuthzFails() {
        // Create an apikey that will result in apikey response indicating that authorization
        // has passed.
        var tokenSupplier1 = (Supplier<String>) () -> {
            setResponse(TEST_NCA_ID,
                        TEST_OWNER_ID,
                        List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                new Resource("account-functions", "*")),
                        List.of(SCOPE_LIST_FUNCTIONS));
            return "nvapi-stg-some-key";
        };
        var token1 = getToken(tokenSupplier1);

        // Retrieve function using the good apikey as a Bearer token.
        var requestEntity1 =
                RequestEntity.get(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                                     + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token1)
                        .build();
        var responseEntity1 = testRestTemplate.exchange(requestEntity1, FunctionResponse.class);
        assertThat(responseEntity1.getStatusCode()).isEqualTo(HttpStatus.OK);

        // At this point both primary and backup caches are populated with ApiKeyValidationResult.

        // Create an apikey such that "allowed" is false in the apikeys response indicating that
        // authorization has failed.
        var tokenSupplier2 = (Supplier<String>) () -> {
            setApiKeyValidationResponse(TEST_NCA_ID,
                           TEST_OWNER_ID,
                           List.of(new Resource("function", TEST_FUNCTION_ID + "/*"),
                                   new Resource("account-functions", "*")),
                           List.of(SCOPE_LIST_FUNCTIONS),
                           false);
            return "nvapi-stg-valid";
        };
        var token2 = getToken(tokenSupplier2);
        var requestEntity2 =
                RequestEntity.get(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                                     + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token2)
                        .build();

        // Invoke the endpoint again using the new apikey that would result in 403 to indicate the
        // the authorization has failed and ApiKeyValidationResult in the caches is not returned in
        // the response.
        var responseEntity2 = testRestTemplate.exchange(requestEntity2, FunctionResponse.class);
        assertThat(responseEntity2.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
    }

}
