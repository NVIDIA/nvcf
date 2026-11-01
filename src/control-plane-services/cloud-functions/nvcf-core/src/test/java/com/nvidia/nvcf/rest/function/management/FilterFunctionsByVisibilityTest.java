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

import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
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
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionIdsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.TestUtil;
import jakarta.annotation.Nullable;
import java.net.URI;
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
class FilterFunctionsByVisibilityTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private TestAccountService testAccountService;

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
        MockEssServer.start(essBaseUrl);
        MockApiKeysServer.start(apiKeysBaseUrl);
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
                                                  TEST_PUBLIC_FUNCTION_VERSION_ID_1, TEST_PUBLIC_FUNCTION_NCA_ID,
                                                  TEST_PUBLIC_FUNCTION_NAME_V1);
        testAuthPartiesService.createTestFunction(TEST_PUBLIC_FUNCTION_ID_1,
                                                  TEST_PUBLIC_FUNCTION_VERSION_ID_2, TEST_PUBLIC_FUNCTION_NCA_ID,
                                                  TEST_PUBLIC_FUNCTION_NAME_V2);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_PUBLIC_FUNCTION_NCA_ID,
                                                    TEST_PUBLIC_FUNCTION_ID_1, Optional.empty(),
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


    Stream<Arguments> filterFunctionsByVisibilityArgs() {
        return Stream.of(
                Arguments.of(List.of(TEST_PUBLIC_FUNCTION_ID_1),
                             Set.of(TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2),
                             "?visibility=public",
                             HttpStatus.OK),
                Arguments.of(List.of(TEST_FUNCTION_ID),
                             Set.of(TEST_VERSION_ID_1),
                             "?visibility=private",
                             HttpStatus.OK),
                Arguments.of(List.of(TEST_FUNCTION_ID_2),
                             Set.of(TEST_VERSION_ID_2),
                             "?visibility=authorized",
                             HttpStatus.OK),
                Arguments.of(List.of(TEST_PUBLIC_FUNCTION_ID_1,
                                     TEST_FUNCTION_ID,
                                     TEST_FUNCTION_ID_2),
                             Set.of(TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2,
                                    TEST_VERSION_ID_1,
                                    TEST_VERSION_ID_2),
                             null,
                             HttpStatus.OK),
                Arguments.of(List.of(TEST_PUBLIC_FUNCTION_ID_1,
                                     TEST_FUNCTION_ID,
                                     TEST_FUNCTION_ID_2),
                             Set.of(TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                    TEST_PUBLIC_FUNCTION_VERSION_ID_2,
                                    TEST_VERSION_ID_1,
                                    TEST_VERSION_ID_2),
                             "?visibility=",
                             HttpStatus.OK),
                Arguments.of(List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2),
                             Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             "?visibility=private,authorized",
                             HttpStatus.OK),
                Arguments.of(List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2),
                             Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             "?visibility=private&visibility=authorized",
                             HttpStatus.OK),
                Arguments.of(List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2),
                             Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             "?visibility=private&visibility=AUTHORIZED",
                             HttpStatus.OK),
                Arguments.of(List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2),
                             Set.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2),
                             "?visibility=private,AUTHORIZED",
                             HttpStatus.OK),
                Arguments.of(List.of(),
                             Set.of(),
                             "?visibility=invalid",
                             HttpStatus.BAD_REQUEST),
                Arguments.of(List.of(),
                             Set.of(),
                             "?visibility=private,invalid",
                             HttpStatus.BAD_REQUEST),
                Arguments.of(List.of(),
                             Set.of(),
                             "?visibility=private&visibility=invalid",
                             HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("filterFunctionsByVisibilityArgs")
    void shouldFilterFunctionIdsByVisibility(
            @Nullable List<UUID> expectedFunctions,
            @Nullable Set<UUID> expectedVersions,
            @Nullable String visibility,
            HttpStatus expectedStatus) {
        // Get a token
        Object tokenSupplier = (Supplier<String>) () -> {
            setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                        List.of(new Resource("function", TEST_FUNCTION_ID_2 + "/*"),
                                new Resource("account-functions", "*")),
                        List.of(SCOPE_LIST_FUNCTIONS));
            return "nvapi-stg-some-key";
        };
        var token = TestUtil.getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Create a test function TEST_FUNCTION_ID_2 in account TEST_NCA_ID_2.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        // Authorize TEST_NCA_ID account for TEST_FUNCTION_ID_2 created under TEST_NCA_ID_2.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(TEST_NCA_ID).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID_2, TEST_FUNCTION_ID_2,
                                                    Optional.empty(), authorizedParties1);

        // Try retrieving function ids.
        var uri = "/v2/nvcf/functions/ids";
        if (visibility != null) {
            uri += visibility;
        }
        var requestEntity = RequestEntity.get(URI.create(uri))
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
        assertThat(responseBody.functionIds()).hasSize(expectedFunctions.size());
        assertThat(responseBody.functionIds())
                .containsExactlyInAnyOrderElementsOf(expectedFunctions);
    }

    @ParameterizedTest
    @MethodSource("filterFunctionsByVisibilityArgs")
    void shouldFilterFunctionsByVisibility(
            @Nullable List<UUID> expectedFunctions,
            @Nullable Set<UUID> expectedVersions,
            @Nullable String visibility,
            HttpStatus expectedStatus) {
        // Get a token
        var tokenSupplier = (Supplier<String>) () -> {
            setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                        List.of(new Resource("function", TEST_FUNCTION_ID_2 + "/*"),
                                new Resource("account-functions", "*")),
                        List.of(SCOPE_LIST_FUNCTIONS));
            return "nvapi-stg-some-key";
        };
        var token = TestUtil.getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Create a test function TEST_FUNCTION_ID_2 in account TEST_NCA_ID_2.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        // Authorize TEST_NCA_ID account for TEST_FUNCTION_ID_2 created under TEST_NCA_ID_2.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(TEST_NCA_ID).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID_2, TEST_FUNCTION_ID_2,
                                                    Optional.empty(), authorizedParties1);

        var uri = "/v2/nvcf/functions";
        if (visibility != null) {
            uri += visibility;
        }
        var requestEntity =
                RequestEntity.get(URI.create(uri))
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
        assertThat(responseBody.functions()).hasSize(expectedVersions.size());
        assertThat(responseBody.functions().stream()
                           .map(FunctionDto::id).distinct().collect(Collectors.toList()))
                .containsExactlyInAnyOrderElementsOf(expectedFunctions);
        var functions = responseBody.functions().stream()
                .collect(Collectors.toMap(FunctionDto::versionId, Function.identity()));
        assertThat(functions.keySet()).containsExactlyInAnyOrderElementsOf(expectedVersions);

        for (UUID expectedVersionId: expectedVersions) {
            var functionDto = functions.get(expectedVersionId);
            assertThat(functionDto).isNotNull();
            assertThat(functionDto.createdAt()).isNotNull();
            // we should not provide details in list functions request
            assertThat(functionDto.activeInstances()).isNull();
            if (functionDto.id().equals(TEST_FUNCTION_ID)) { // Private functions
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
                assertThat(functionDto.versionId()).isIn(expectedVersions);
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
}
