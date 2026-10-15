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
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_AUTHORIZE_CLIENTS;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_CLIENT_ID_4;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_4;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_5;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesRequest;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesResponse;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.azp.dto.ListAuthorizedPartiesResponse;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.net.URI;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.tuple.ImmutablePair;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
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
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class XAccountAuthorizedPartiesControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

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
        MockIcmsServer.start(9096, jsonMapper);
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockIcmsServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockApiKeysServer.resetToDefault();
    }

    Stream<Arguments> authorizedPartiesAllVersionsArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("other-admin-id",
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID_2,
                             TEST_FUNCTION_ID,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             "badNcaId",
                             TEST_FUNCTION_ID,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID_2,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(null, TEST_NCA_ID, TEST_FUNCTION_ID, HttpStatus.UNAUTHORIZED)
        );
    }

    @ParameterizedTest
    @MethodSource({"authorizedPartiesAllVersionsArgs"})
    void createAuthorizedParties(
            Object tokenSupplier, String ncaId, UUID functionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create two versions of a function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Create a different function in the same account.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        var authorizedParties = List.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_5).build()
        );
        var requestBody = AuthorizedPartiesRequest.builder()
                .authorizedParties(authorizedParties).build();

        var requestEntity =
                RequestEntity
                        .post(URI.create("/v2/nvcf/accounts/" + ncaId +
                                                 "/authorizations/functions/" + functionId))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isNull();
        assertThat(responseBody.function().ncaId()).isEqualTo(ncaId);
        assertThat(responseBody.function().authorizedParties())
                .containsExactlyInAnyOrderElementsOf(authorizedParties);

        // Check if DB is updated.
        var expectedFunctionAuthAccounts = authorizedParties.stream()
                .map(AuthorizedPartyDto::ncaId)
                .collect(Collectors.toSet());
        testAuthPartiesService
                .verifyAuthAccountsOnFunctionEntities(ncaId,
                                                      functionId,
                                                      Optional.empty(),
                                                      expectedFunctionAuthAccounts,
                                                      null);
    }

    @ParameterizedTest
    @MethodSource({"authorizedPartiesAllVersionsArgs"})
    void deleteAuthorizedParties(
            Object tokenSupplier, String ncaId, UUID functionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create two versions of a function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Create another function in TEST_NCA_ID account.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        var authorizedParties = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_5).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties);

        // Delete authorized parties for function.
        var requestEntity =
                RequestEntity
                        .delete(URI.create("/v2/nvcf/accounts/" + ncaId +
                                                   "/authorizations/functions/" + functionId))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isNull();
        assertThat(responseBody.function().ncaId()).isEqualTo(ncaId);
        assertThat(responseBody.function().authorizedParties()).isNull();

        // Check if DB is updated.
        testAuthPartiesService
                .verifyAuthAccountsOnFunctionEntities(ncaId,
                                                      functionId,
                                                      Optional.empty(),
                                                      null,
                                                      null);
    }

    @ParameterizedTest
    @MethodSource({"authorizedPartiesAllVersionsArgs"})
    void getAuthorizedParties(
            Object tokenSupplier, String ncaId, UUID functionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create two versions of a function with TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Create another function in TEST_NCA_ID account.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        var authorizedParties = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_5).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties);

        var requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + ncaId +
                                                "/authorizations/functions/" + functionId))
                        .header("Authorization", "Bearer " + token).build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListAuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        if (responseBody.functions().isEmpty()) {
            log.info("Empty list in the response");
            return;
        }
        assertThat(responseBody.functions()).hasSize(2);
        responseBody.functions().stream()
                .peek(dto -> {
                    assertThat(dto.ncaId()).isEqualTo(TEST_NCA_ID);
                    assertThat(dto.id()).isEqualTo(functionId);
                    assertThat(dto.versionId()).isIn(TEST_VERSION_ID_1, TEST_VERSION_ID_2);
                }).count();

        var authParties1 = responseBody.functions().getFirst().authorizedParties();
        assertThat(authParties1)
                .hasSize(3)
                .containsExactlyInAnyOrderElementsOf(authorizedParties);

        var authParties2 = responseBody.functions().get(1).authorizedParties();
        assertThat(authParties2)
                .hasSize(3)
                .containsExactlyInAnyOrderElementsOf(authorizedParties);
    }

    Stream<Arguments> argsForNoAuthorizedParties() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100))
        );
    }

    @ParameterizedTest
    @MethodSource("argsForNoAuthorizedParties")
    void noAuthorizedParties(Object tokenSupplier) {
        var token = getToken(tokenSupplier);

        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        var requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID +
                                                "/authorizations/functions/" + TEST_FUNCTION_ID))
                        .header("Authorization", "Bearer " + token).build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListAuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).isNotNull();
        assertThat(responseBody.functions()).hasSize(2);
        responseBody.functions().forEach(dto -> {
            assertThat(dto.id()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(dto.versionId()).isIn(TEST_VERSION_ID_1, TEST_VERSION_ID_2);
            assertThat(dto.ncaId()).isEqualTo(TEST_NCA_ID);
            assertThat(dto.authorizedParties()).isEmpty();
        });
    }

    Stream<Arguments> argsForListAuthorizedParties() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100))
        );
    }

    @ParameterizedTest
    @MethodSource("argsForListAuthorizedParties")
    void listAuthorizedParties(Object tokenSupplier) {
        var token = getToken(tokenSupplier);

        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        // No authorized parties
        var requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID +
                                                "/authorizations/functions/" + TEST_FUNCTION_ID))
                        .header("Authorization", "Bearer " + token).build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListAuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).isNotNull();
        assertThat(responseBody.functions()).hasSize(3);
        responseBody.functions().forEach(dto -> {
            assertThat(dto.id()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(dto.versionId()).isNotNull();
            assertThat(dto.ncaId()).isEqualTo(TEST_NCA_ID);
            assertThat(dto.authorizedParties()).isEmpty();
        });

        // Associate authorized parties for all versions of a function. Use the generic endpoint
        // to list authorized parties for all the versions of a function.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID +
                                                "/authorizations/functions/" + TEST_FUNCTION_ID))
                        .header("Authorization", "Bearer " + token).build();
        responseEntity =
                testRestTemplate.exchange(requestEntity, ListAuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).isNotNull();
        assertThat(responseBody.functions()).hasSize(3);
        responseBody.functions().forEach(dto -> {
            assertThat(dto.id()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(dto.versionId()).isNotNull();
            assertThat(dto.ncaId()).isEqualTo(TEST_NCA_ID);
            assertThat(dto.authorizedParties()).hasSize(2);
        });

        // Associate authorized parties with a specific version of a function. Use the generic
        // endpoint to list authorized parties for all the versions of a function.
        var authorizedParties2 = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_3).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_5).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties2);

        requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID +
                                                "/authorizations/functions/" + TEST_FUNCTION_ID))
                        .header("Authorization", "Bearer " + token).build();
        responseEntity =
                testRestTemplate.exchange(requestEntity, ListAuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).isNotNull();
        assertThat(responseBody.functions()).hasSize(3);
        responseBody.functions().forEach(dto -> {
            assertThat(dto.id()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(dto.versionId()).isNotNull();
            assertThat(dto.ncaId()).isEqualTo(TEST_NCA_ID);
            if (dto.versionId().equals(TEST_VERSION_ID_1)) {
                assertThat(dto.authorizedParties()).hasSize(4);
            } else {
                assertThat(dto.authorizedParties()).hasSize(2);
            }
        });
    }

    @ParameterizedTest
    @MethodSource("argsForListAuthorizedParties")
    void listAuthorizedPartiesForVersion(Object tokenSupplier) {
        var token = getToken(tokenSupplier);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        // No authorized parties. List authorized parties for a specific version of a function.
        var requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token).build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties()).isEmpty();

        // Associate authorized parties for all versions of a function. List authorized parties
        // for a specific version of a function.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_5).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token).build();
        responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties()).hasSize(3);

        // Associate authorized parties with a specific version of a function.
        var authorizedParties2 = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_3).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_4).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties2);

        // List authorized parties for the specific version of the function. Response
        // should contain authorized parties for the specific version of the function
        // and the inherited authorized parties from the wildcard.
        requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token).build();
        responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties()).hasSize(5);
    }

    Stream<Arguments> authorizedPartiesForFunctionVersionArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt("some-other-admin",
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             "badNcaId",
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID_2,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_3,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             UUID.randomUUID(),
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(null, TEST_NCA_ID, TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-some-key", TEST_NCA_ID, TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                Arguments.of("nvapi-stg-some-key", TEST_NCA_ID, TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_1, HttpStatus.FORBIDDEN)
        );
    }

    @ParameterizedTest
    @MethodSource("authorizedPartiesForFunctionVersionArgs")
    void createAuthorizedPartiesForFunctionVersion(
            Object tokenSupplier, String ncaId, UUID functionId,
            UUID functionVersionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        var authorizedParties = List.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_5).build()
        );
        var requestBody = AuthorizedPartiesRequest.builder()
                .authorizedParties(authorizedParties).build();

        var requestEntity =
                RequestEntity
                        .post(URI.create("/v2/nvcf/accounts/" + ncaId
                                                 + "/authorizations/functions/" + functionId
                                                 + "/versions/" + functionVersionId))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isEqualTo(functionVersionId);
        assertThat(responseBody.function().ncaId()).isEqualTo(ncaId);
        assertThat(responseBody.function().authorizedParties())
                .containsExactlyInAnyOrderElementsOf(authorizedParties);

        // Check if DB is updated.
        var expectedVersionAuthAccounts = authorizedParties.stream()
                .map(AuthorizedPartyDto::ncaId).collect(Collectors.toSet());
        testAuthPartiesService
                .verifyAuthAccountsOnFunctionEntities(TEST_NCA_ID,
                                                      functionId,
                                                      Optional.of(functionVersionId),
                                                      null,
                                                      expectedVersionAuthAccounts);
    }

    @ParameterizedTest
    @MethodSource("authorizedPartiesForFunctionVersionArgs")
    void deleteAuthorizedPartiesForFunctionVersion(
            Object tokenSupplier, String ncaId, UUID functionId,
            UUID functionVersionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create two versions of a function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_5).build()
        );
        // Create authorized parties for all versions of TEST_FUNCTION_ID.
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        // Associate authorized parties with a function version TEST_VERSION_ID_1.
        var authorizedParties2 = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_3).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_4).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties2);

        // Delete authorized parties for function version.
        var requestEntity =
                RequestEntity
                        .delete(URI.create("/v2/nvcf/accounts/" + ncaId
                                                   + "/authorizations/functions/" + functionId
                                                   + "/versions/" + functionVersionId))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isEqualTo(functionVersionId);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties()).isNull();

        // Check if DB is updated.
        var expectedFunctionAuthAccounts = authorizedParties1.stream()
                .map(AuthorizedPartyDto::ncaId).collect(Collectors.toSet());
        testAuthPartiesService
                .verifyAuthAccountsOnFunctionEntities(ncaId,
                                                      functionId,
                                                      Optional.of(functionVersionId),
                                                      expectedFunctionAuthAccounts,
                                                      null);
    }

    Stream<Arguments> authorizedPartiesValidationArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             500),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_1,
                                                      TEST_AUTHORIZED_NCA_ID_1),
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_2,
                                                      TEST_AUTHORIZED_NCA_ID_2),
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_3,
                                                      TEST_AUTHORIZED_NCA_ID_3),
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_4,
                                                      TEST_AUTHORIZED_NCA_ID_4))),
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(null, TEST_AUTHORIZED_NCA_ID_3),
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_4,
                                                      TEST_AUTHORIZED_NCA_ID_4))),
                             HttpStatus.OK),
                // bad nca id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             "badNcaId",
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(null, TEST_AUTHORIZED_NCA_ID_3),
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_4,
                                                      TEST_AUTHORIZED_NCA_ID_4))),
                             HttpStatus.NOT_FOUND),
                // Invalid Account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_1,
                                                      "test-invalid-nca-id-1"))),
                             HttpStatus.BAD_REQUEST),
                // No authorized parties specified
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of()),
                             HttpStatus.BAD_REQUEST),
                // Authorized party includes the account id that owns the function.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             500),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_3, TEST_NCA_ID),
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_4,
                                                      TEST_AUTHORIZED_NCA_ID_4))),
                             HttpStatus.BAD_REQUEST),
                // Multiple Clients mapped to same account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_3,
                                                      TEST_AUTHORIZED_NCA_ID_4),
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_4,
                                                      TEST_AUTHORIZED_NCA_ID_4))),
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_3, null))),
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_3, ""))),
                             HttpStatus.BAD_REQUEST),
                // Valid request object to make function public
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(null, "*"))),
                             HttpStatus.OK),
                // Cannot specify other authorized parties with wildcard account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             testAuthPartiesService.authorizedParties(List.of(
                                     ImmutablePair.of(null, "*"),
                                     ImmutablePair.of(TEST_AUTHORIZED_CLIENT_ID_2,
                                                      TEST_AUTHORIZED_NCA_ID_2))),
                             HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("authorizedPartiesValidationArgs")
    void authorizedPartiesRequestValidation(
            String token, String ncaId, UUID functionId, UUID functionVersionId,
            List<AuthorizedPartyDto> authorizedParties,
            HttpStatus expectedStatus) {
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        var requestBody = AuthorizedPartiesRequest.builder()
                .authorizedParties(authorizedParties).build();

        var requestEntity =
                RequestEntity
                        .post(URI.create("/v2/nvcf/accounts/" + ncaId
                                                 + "/authorizations/functions/" + functionId
                                                 + "/versions/" + functionVersionId))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isEqualTo(functionVersionId);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties())
                .containsExactlyInAnyOrderElementsOf(authorizedParties);
    }
}
