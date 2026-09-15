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
import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_AUTHORIZE_CLIENTS;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_5;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_6;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
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
import com.nvidia.nvcf.rest.azp.dto.PatchAuthorizedPartyRequest;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
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
class XAccountWildcardAuthorizedPartyTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private AuthorizedPartiesService authPartiesService;

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

    // Function level authz parties related tests

    Stream<Arguments> argsAuthTokens() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100))
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldMakePrivateFunctionPublic(Object tokenSupplier) {
        // Super Admin can perform this operation.
        var token = getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Try associating wildcard authz account at the function level to make all the versions
        // public.
        var authorizedParties1 = List.of(
                AuthorizedPartyDto.builder().ncaId("*").build()
        );
        var requestBody = AuthorizedPartiesRequest.builder()
                .authorizedParties(authorizedParties1).build();
        var requestEntity =
                RequestEntity
                        .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                 + "/authorizations/functions/" + TEST_FUNCTION_ID))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().authorizedParties()).hasSize(1);

        var azps = responseBody.function().authorizedParties();
        assertThat(azps.getFirst().ncaId()).isEqualTo("*");
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isNull();
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldFailToAssociateAuthPartiesToPublicFunction(Object tokenSupplier) {
        var token = getToken(tokenSupplier);

        // Create two function versions pf TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedPartiesDtos1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedPartiesDtos1);

        // Now try using POST endpoint to override the currently associated wildcard
        // auth party on the public functions.
        var authorizedPartiesDtos2 = List.of(
                AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_5).build(),
                AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_6).build()
        );

        var requestBody = AuthorizedPartiesRequest.builder()
                .authorizedParties(authorizedPartiesDtos2).build();
        var requestEntity =
                RequestEntity
                        .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                 + "/authorizations/functions/" + TEST_FUNCTION_ID))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot create authorized parties for a public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    Stream<Arguments> argsListAuthPartiesOfPublicFunction() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             List.of(TEST_VERSION_ID_1, TEST_VERSION_ID_2))
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsListAuthPartiesOfPublicFunction")
    void shouldListAuthPartiesOfPublicFunction(
            Object tokenSupplier,
            List<UUID> expectedVersionIds) {
        // Super Admin can perform this operation.
        var token = getToken(tokenSupplier);

        // Create two function versions pf TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        // Try listing authorized parties.
        var requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                + "/authorizations/functions/" + TEST_FUNCTION_ID))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListAuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functions()).hasSize(expectedVersionIds.size());

        var functions = responseBody.functions();
        functions.forEach(function -> {
            assertThat(function.authorizedParties()).hasSize(1);
            assertThat(function.authorizedParties().getFirst().ncaId()).isEqualTo("*");
            assertThat(function.id()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(function.versionId()).isIn(expectedVersionIds);
            assertThat(function.ncaId()).isEqualTo(TEST_NCA_ID);
        });
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldFailToAddAuthPartiesToPublicFunction(Object tokenSupplier) {
        // Super Admin cannot perform this operation.
        var token = getToken(tokenSupplier);

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

        // Try adding more authorized parties to a public function.
        var authorizedParty = AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_6).build();
        var requestBody = PatchAuthorizedPartyRequest.builder()
                .authorizedParty(authorizedParty).build();
        var requestEntity =
                RequestEntity
                        .patch(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                  + "/authorizations/functions/" +
                                                  TEST_FUNCTION_ID + "/add"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot add authorized parties for a public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldFailToRemoveAuthPartiesFromPublicFunction(Object tokenSupplier) {
        // Super Admin cannot perform this operation.
        var token = getToken(tokenSupplier);

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

        // Try removing authorized parties.
        var authorizedParty = AuthorizedPartyDto.builder().ncaId("*").build();
        var requestBody = PatchAuthorizedPartyRequest.builder()
                .authorizedParty(authorizedParty).build();
        var requestEntity =
                RequestEntity
                        .patch(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                  + "/authorizations/functions/" +
                                                  TEST_FUNCTION_ID + "/remove"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot remove authorized parties for a public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldMakePublicFunctionPrivate(Object tokenSupplier) {
        // Super Admin can perform this operation.
        var token = getToken(tokenSupplier);

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

        // Check if the function is public.
        assertThat(authPartiesService.isFunctionPublic(TEST_FUNCTION_ID)).isTrue();

        // Try making it private by deleting the associated authorized party.
        var requestEntity =
                RequestEntity
                        .delete(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                   + "/authorizations/functions/" + TEST_FUNCTION_ID))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().authorizedParties()).isNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isNull();
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);

        // Check if the function is now private.
        authPartiesService.clearPublicFunctionCache();
        assertThat(authPartiesService.isFunctionPublic(TEST_FUNCTION_ID)).isFalse();
    }

    // Specific function version related tests

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldMakePrivateFunctionVersionPublic(Object tokenSupplier) {
        // Super Admin can perform this operation.
        var token = getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Try associating wildcard authz account at the version level to make specific
        // version public.
        var authorizedParties1 = List.of(
                AuthorizedPartyDto.builder().ncaId("*").build()
        );
        var requestBody = AuthorizedPartiesRequest.builder()
                .authorizedParties(authorizedParties1).build();
        var requestEntity =
                RequestEntity
                        .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                 + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                 + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);

        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().authorizedParties()).hasSize(1);

        var azps = responseBody.function().authorizedParties();
        assertThat(azps.getFirst().ncaId()).isEqualTo("*");
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldFailToAssociateAuthPartiesWithPublicFunctionVersion(Object tokenSupplier) {
        var token = getToken(tokenSupplier);

        // Create two function versions pf TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedPartiesDtos1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedPartiesDtos1);

        // Now try using POST endpoint to override the currently associated wildcard
        // auth party on the public functions.
        var authorizedPartiesDtos2 = List.of(
                AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_5).build(),
                AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_6).build()
        );

        var requestBody = AuthorizedPartiesRequest.builder()
                .authorizedParties(authorizedPartiesDtos2).build();
        var requestEntity =
                RequestEntity
                        .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                 + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                 + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot create authorized parties for a public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsListAuthPartiesOfPublicFunction")
    void shouldListAuthPartiesForPublicFunctionVersion(Object tokenSupplier) {
        // Super Admin can perform this operation.
        var token = getToken(tokenSupplier);

        // Create two function versions pf TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as an authorized party with a specific
        // version to make it public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties1);

        // Try listing authorized parties for specific version of the public function
        var requestEntity =
                RequestEntity
                        .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();

        var function = responseBody.function();
        assertThat(function.authorizedParties()).hasSize(1);
        assertThat(function.authorizedParties().getFirst().ncaId()).isEqualTo("*");
        assertThat(function.id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(function.versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(function.ncaId()).isEqualTo(TEST_NCA_ID);
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldFailToAddAuthPartiesToPublicFunctionVersion(Object tokenSupplier) {
        // Super Admin cannot perform this operation.
        var token = getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as an authorized party with a specific
        // version to make it public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties1);

        // Try adding authorized parties to specific version of public function
        var authorizedParty = AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_6).build();
        var requestBody = PatchAuthorizedPartyRequest.builder()
                .authorizedParty(authorizedParty).build();
        var requestEntity =
                RequestEntity
                        .patch(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                  + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                  + "/versions/" + TEST_VERSION_ID_1 + "/add"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot add authorized parties for a public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldFailToRemoveAuthPartiesFromPublicFunctionVersion(Object tokenSupplier) {
        // Super Admin cannot perform this operation.
        var token = getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as an authorized party with a specific
        // version to make it public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID,TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                     authorizedParties1);

        // Try removing authorized parties to specific version of public function
        var authorizedParty = AuthorizedPartyDto.builder().ncaId("*").build();
        var requestBody = PatchAuthorizedPartyRequest.builder()
                .authorizedParty(authorizedParty).build();
        var requestEntity =
                RequestEntity
                        .patch(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                  + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                  + "/versions/" + TEST_VERSION_ID_1 + "/remove"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var mesg = "Cannot remove authorized parties for a public function";
        var responseBody = responseEntity.getBody();
        var problemDetail = jsonMapper.readValue(responseBody, ProblemDetail.class);
        assertThat(problemDetail.getDetail()).contains(mesg);
        assertThat(problemDetail.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(problemDetail.getType().toString()).endsWith("bad-request");
        assertThat(problemDetail.getTitle()).isEqualTo("Bad Request");
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("argsAuthTokens")
    void shouldMakePublicFunctionVersionPrivate(Object tokenSupplier) {
        // Super Admin can perform this operation.
        var token = getToken(tokenSupplier);

        // Create a test function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as an authorized party with a specific
        // version to make it public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties1);

        // Check if the function is public.
        assertThat(authPartiesService
                           .isFunctionPublic(TEST_FUNCTION_ID, TEST_VERSION_ID_1)).isTrue();

        // Try making it private by deleting the associated authorized party
        var requestEntity =
                RequestEntity
                        .delete(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                   + "/authorizations/functions/" + TEST_FUNCTION_ID
                                                   + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().authorizedParties()).isNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().versionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);

        // Check if the function is now private.
        authPartiesService.clearPublicFunctionCache();
        assertThat(authPartiesService
                           .isFunctionPublic(TEST_FUNCTION_ID, TEST_VERSION_ID_1)).isFalse();
    }
}
