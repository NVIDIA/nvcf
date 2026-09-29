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
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_5;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
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
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
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
class XAccountListingAuthorizedFunctionsTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

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
                // All versions are authorized for cross-account access using Optional.emtpy().
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             Optional.empty(),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2, TEST_VERSION_ID_3),
                             HttpStatus.OK),
                // Function version TEST_VERSION_ID_1 is authorized for cross-account access.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             Optional.of(TEST_VERSION_ID_1),
                             List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_3),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_3),
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("listAuthorizedFunctionsInDifferentAccountArgs")
    void shouldListAuthorizedFunctionsInDifferentAccount(
            Object tokenSupplier,
            Optional<UUID> authorizedFunctionVersionId,
            List<UUID> expectedFunctionIds,
            List<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);

        // Associate authorized parties/accounts with the function version.
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    authorizedFunctionVersionId, authorizedParties);

        // Retrieve functions that are -- a. created under account TEST_AUTHORIZED_NCA_ID_1 and
        // b. created under different account but TEST_AUTHORIZED_NCA_ID_1 account is authorized
        // to use.
        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/accounts/" + TEST_AUTHORIZED_NCA_ID_1
                                                     + "/functions"))
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
                if (authorizedFunctionVersionId.isEmpty()) {
                    assertThat(functionDto.versionId()).isIn(expectedFunctionVersionIds);
                } else if (authorizedFunctionVersionId.get().equals(TEST_VERSION_ID_1)){
                    assertThat(functionDto.versionId()).isEqualTo(TEST_VERSION_ID_1);
                }
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
                assertThat(functionDto.inferenceUrl()).isNotNull();
                assertThat(functionDto.containerArgs()).isNotBlank();
                assertThat(functionDto.containerImage()).isNotNull();
            } else {
                fail("Unknown function returned");
            }
        }
    }

    @ParameterizedTest
    @MethodSource("listAuthorizedFunctionsInDifferentAccountArgs")
    void shouldListIdsOfAuthorizedFunctionsInDifferentAccount(
            Object tokenSupplier,
            Optional<UUID> authorizedFunctionVersionId,
            List<UUID> expectedFunctionIds,
            List<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);

        // Associate authorized parties at appropriate level based on passed in optional parameter.
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    authorizedFunctionVersionId, authorizedParties);

        // Retrieve functions that are -- a. created under account TEST_AUTHORIZED_NCA_ID_1 and
        // b. created under different account but TEST_AUTHORIZED_NCA_ID_1 account is authorized
        // to use.
        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/accounts/" + TEST_AUTHORIZED_NCA_ID_1 + "/functions/ids"))
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
                // All versions are authorized for cross-account access using Optional.empty().
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_FUNCTION_ID,
                             Optional.empty(),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             HttpStatus.OK),
                // Function version TEST_VERSION_ID_1 is authorized for cross-account access.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_FUNCTION_ID,
                             Optional.of(TEST_VERSION_ID_1),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                // Non-existent function id.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             UUID.randomUUID(),
                             Optional.of(TEST_VERSION_ID_1),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("listAuthorizedFunctionsInDifferentAccountUsingIdArgs")
    void shouldListAuthorizedFunctionsInDifferentAccountUsingId(
            Object tokenSupplier,
            UUID functionId,
            Optional<UUID> authorizedFunctionVersionId,
            List<UUID> expectedFunctionIds,
            List<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Associate authorized parties with the function using the specified version.
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    authorizedFunctionVersionId, authorizedParties);

        // Retrieve versions of function TEST_FUNCTION_ID(defined in TEST_NCA_ID account) that
        // account TEST_AUTHORIZED_NCA_ID_1 is authorized to use.
        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/accounts/" + TEST_AUTHORIZED_NCA_ID_1
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
        assertThat(responseBody.functions()).hasSize(expectedFunctionVersionIds.size());
        assertThat(responseBody.functions().stream()
                           .map(f -> f.id()).distinct().collect(Collectors.toList()))
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
                // All versions are authorized for cross-account access using Optional.empty().
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             Optional.empty(),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                // Function version TEST_VERSION_ID_1 is authorized for cross-account access.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             Optional.of(TEST_VERSION_ID_1),
                             List.of(TEST_FUNCTION_ID),
                             List.of(TEST_VERSION_ID_1),
                             HttpStatus.OK),
                // Function version TEST_VERSION_ID_2 is authorized for cross-account access.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_AUTHORIZED_CLIENT_ID_1,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             Optional.of(TEST_VERSION_ID_2),
                             List.of(),
                             List.of(),
                             HttpStatus.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("getAuthorizedFunctionInDifferentAccountUsingIdAndVersionArgs")
    void shouldGetAuthorizedFunctionInDifferentAccountUsingVersionId(
            Object tokenSupplier,
            Optional<UUID> authorizedFunctionVersionId,
            List<UUID> expectedFunctionIds,
            List<UUID> expectedFunctionVersionIds,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);

        // Associate authorized parties at the appropriate level based on passed in optional param.
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    authorizedFunctionVersionId, authorizedParties);

        // Retrieve versions of function TEST_FUNCTION_ID(defined in TEST_NCA_ID account) and
        // account TEST_AUTHORIZED_NCA_ID_1 is authorized to use.
        var requestEntity =
                RequestEntity.get(URI.create("/v2/nvcf/accounts/" + TEST_AUTHORIZED_NCA_ID_1
                                                     + "/functions/" + TEST_FUNCTION_ID
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

}
