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
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_4;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_5;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_6;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.google.common.collect.Sets;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesResponse;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.azp.dto.PatchAuthorizedPartyRequest;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
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
class XAccountPatchAuthorizedPartiesTest {
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

    Stream<Arguments> patchAuthorizedPartiesArgs() {
        var validAuthParty1 = AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_6).build();
        var validAuthParty2 = AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_4)
                // .clientId(TEST_AUTHORIZED_CLIENT_ID_4)
                .build();
        var invalidAuthParty = AuthorizedPartyDto.builder().ncaId(TEST_NCA_ID).build();

        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             300),
                             validAuthParty1,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             validAuthParty2,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             invalidAuthParty,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             invalidAuthParty,
                             TEST_NCA_ID_3,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             validAuthParty1,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             validAuthParty1,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID_3,
                             TEST_VERSION_ID_3,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(),
                                                             100),
                             validAuthParty1,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             300),
                             AuthorizedPartyDto.builder().ncaId("*").build(), // Wildcard NCA ID.
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(null, validAuthParty1, TEST_NCA_ID,
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.UNAUTHORIZED)
        );
    }

    Stream<Arguments> addExistingAuthorizedPartiesArgs() {
        var existingAuthParty = AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_2)
                // .clientId(TEST_AUTHORIZED_CLIENT_ID_2)
                .build();

        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             existingAuthParty,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.CONFLICT)
        );
    }

    @ParameterizedTest
    @MethodSource({"patchAuthorizedPartiesArgs","addExistingAuthorizedPartiesArgs"})
    void addAuthorizedPartyToFunction(
            Object tokenSupplier, AuthorizedPartyDto authorizedParty, String ncaId,
            UUID functionId, UUID versionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create two versions of a function TEST_FUNCTION_ID in account TEST_NCA_ID
        // and associate authorized parties.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);
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

        // Create TEST_FUNCTION_ID_3 with no authorized parties.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        // Add authorized party for function.
        var requestBody = PatchAuthorizedPartyRequest.builder()
                .authorizedParty(authorizedParty).build();
        var requestEntity =
                RequestEntity
                        .patch(URI.create("/v2/nvcf/accounts/" + ncaId
                                                  + "/authorizations/functions/" + functionId + "/add"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var expectedFunctionAuthDtos = Sets.union(authorizedParties, Set.of(authorizedParty));
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isNull();
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties())
                .hasSize(expectedFunctionAuthDtos.size());
        responseBody.function().authorizedParties().forEach(dto -> {
            assertThat(dto).isIn(expectedFunctionAuthDtos);
        });

        // Check if DB is updated.
        var expectedFunctionAuthAccounts = expectedFunctionAuthDtos.stream()
                .map(AuthorizedPartyDto::ncaId).collect(Collectors.toSet());
        testAuthPartiesService
                .verifyAuthAccountsOnFunctionEntities(ncaId,
                                                      functionId,
                                                      Optional.empty(),
                                                      expectedFunctionAuthAccounts,
                                                      null);
    }

    @ParameterizedTest
    @MethodSource({"patchAuthorizedPartiesArgs","addExistingAuthorizedPartiesArgs"})
    void addAuthorizedPartyToFunctionVersion(
            Object tokenSupplier, AuthorizedPartyDto authorizedParty, String ncaId,
            UUID functionId, UUID versionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create two versions of a function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);

        // Associate authorized parties at function level that are inherited by all versions.
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

        // Create TEST_FUNCTION_ID_3 with no authorized parties.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        // Associate authorized parties specifically for function version TEST_VERSION_ID_1.
        var authorizedParties2 = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_3).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties2);

        // Add authorized party for function version.
        var requestBody = PatchAuthorizedPartyRequest.builder()
                .authorizedParty(authorizedParty).build();
        var requestEntity =
                RequestEntity
                        .patch(URI.create("/v2/nvcf/accounts/" + ncaId
                                                  + "/authorizations/functions/" + functionId
                                                  + "/versions/" + versionId + "/add"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var expectedVersionAuthDtos = Sets.union(authorizedParties2, Set.of(authorizedParty));
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isEqualTo(versionId);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties())
                .hasSize(expectedVersionAuthDtos.size());
        responseBody.function().authorizedParties().forEach(dto -> {
            assertThat(dto).isIn(expectedVersionAuthDtos);
        });

        // Check if DB is updated.
        var expectedFunctionAuthAccounts = authorizedParties1.stream()
                .map(AuthorizedPartyDto::ncaId).collect(Collectors.toSet());
        var expectedVersionAuthAccounts = expectedVersionAuthDtos.stream()
                .map(AuthorizedPartyDto::ncaId).collect(Collectors.toSet());
        testAuthPartiesService
                .verifyAuthAccountsOnFunctionEntities(TEST_NCA_ID,
                                                      functionId,
                                                      Optional.of(versionId),
                                                      expectedFunctionAuthAccounts,
                                                      expectedVersionAuthAccounts);
    }

    Stream<Arguments> removeNonExistingAuthorizedPartiesArgs() {
        var nonExistingAuthParty = AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_2)
                // .clientId(TEST_AUTHORIZED_CLIENT_ID_2)
                .build();

        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_AUTHORIZE_CLIENTS),
                                                             100),
                             nonExistingAuthParty,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource({"patchAuthorizedPartiesArgs", "removeNonExistingAuthorizedPartiesArgs"})
    void removeAuthorizedPartyFromFunction(
            Object tokenSupplier, AuthorizedPartyDto authorizedParty, String ncaId,
            UUID functionId, UUID versionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create two versions of a function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        var authorizedParties = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_4).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_6).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties);

        // Create TEST_FUNCTION_ID_3 with no authorized parties.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        // Remove authorized party for function.
        var requestBody = PatchAuthorizedPartyRequest.builder()
                .authorizedParty(authorizedParty).build();
        var requestEntity =
                RequestEntity
                        .patch(URI.create("/v2/nvcf/accounts/" + ncaId
                                                  + "/authorizations/functions/" + functionId
                                                  + "/remove"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var expectedFunctionAuthDtos = Sets.difference(authorizedParties, Set.of(authorizedParty));
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isNull();
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties())
                .hasSize(expectedFunctionAuthDtos.size());
        responseBody.function().authorizedParties().forEach(dto -> {
            assertThat(dto).isIn(expectedFunctionAuthDtos);
        });

        // Check if DB is updated.
        var expectedFunctionAuthAccounts = expectedFunctionAuthDtos.stream()
                .map(AuthorizedPartyDto::ncaId).collect(Collectors.toSet());
        testAuthPartiesService.verifyAuthAccountsOnFunctionEntities(
                ncaId,
                functionId,
                Optional.empty(),
                expectedFunctionAuthAccounts,
                null);
    }

    @ParameterizedTest
    @MethodSource({"patchAuthorizedPartiesArgs", "removeNonExistingAuthorizedPartiesArgs"})
    void removeAuthorizedPartyFromFunctionVersion(
            Object tokenSupplier, AuthorizedPartyDto authorizedParty, String ncaId,
            UUID functionId, UUID versionId, HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create two versions of a function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME);
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_2);
        // Associate authorized parties at function level.
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
        // Associate authorized parties specifically for version TEST_VERSION_ID_1.
        var authorizedParties2 = Set.of(
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_4).build(),
                AuthorizedPartyDto.builder()
                        .ncaId(TEST_AUTHORIZED_NCA_ID_6).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.of(TEST_VERSION_ID_1),
                                                    authorizedParties2);

        // Create TEST_FUNCTION_ID_3 with no authorized parties.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                                  TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        // Remove authorized party for function version.
        var requestBody = PatchAuthorizedPartyRequest.builder()
                .authorizedParty(authorizedParty).build();
        var requestEntity =
                RequestEntity
                        .patch(URI.create("/v2/nvcf/accounts/" + ncaId
                                                  + "/authorizations/functions/" + functionId
                                                  + "/versions/" + versionId + "/remove"))
                        .header("Authorization", "Bearer " + token)
                        .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AuthorizedPartiesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var expectedVersionAuthDtos = Sets.difference(authorizedParties2, Set.of(authorizedParty));
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function()).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().versionId()).isEqualTo(versionId);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().authorizedParties())
                .hasSize(expectedVersionAuthDtos.size());
        responseBody.function().authorizedParties().forEach(dto -> {
            assertThat(dto).isIn(expectedVersionAuthDtos);
        });

        // Check if DB is updated.
        var expectedFunctionAuthAccounts = authorizedParties1.stream()
                .map(AuthorizedPartyDto::ncaId).collect(Collectors.toSet());
        var expectedVersionAuthAccounts = expectedVersionAuthDtos.stream()
                .map(AuthorizedPartyDto::ncaId).collect(Collectors.toSet());
        testAuthPartiesService.verifyAuthAccountsOnFunctionEntities(
                ncaId,
                functionId,
                Optional.of(versionId),
                expectedFunctionAuthAccounts,
                expectedVersionAuthAccounts);
    }
}
