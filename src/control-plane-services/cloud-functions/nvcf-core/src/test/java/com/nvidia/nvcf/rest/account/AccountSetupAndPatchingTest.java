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
package com.nvidia.nvcf.rest.account;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_ACCOUNT_SETUP;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.DEFAULT_MAX_FUNCTIONS_ALLOWED;
import static com.nvidia.nvcf.util.TestConstants.DEFAULT_MAX_TASKS_ALLOWED;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.apache.commons.lang3.StringUtils.EMPTY;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;
import static org.springframework.http.HttpStatus.FORBIDDEN;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.client.ClientsRepository;
import com.nvidia.nvcf.persistence.client.entity.ClientEntity;
import com.nvidia.nvcf.rest.account.dto.AccountDetailsResponse;
import com.nvidia.nvcf.rest.account.dto.AccountResponse;
import com.nvidia.nvcf.rest.account.dto.AccountUpdateRequest;
import com.nvidia.nvcf.rest.account.dto.CreateAccountResponse;
import com.nvidia.nvcf.rest.account.dto.PatchAccountRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.client.ClientService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.function.Supplier;
import java.util.stream.Collectors;
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
class AccountSetupAndPatchingTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private AccountService accountService;

    @Autowired
    private ClientService clientService;

    @Autowired
    private ClientsRepository clientsRepository;

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

        testAccountService.cleanupAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        MockEssServer.stop();
        MockApiKeysServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testAccountService.cleanupAccountsClientsAndRegistries();
    }

    Stream<Arguments> createAccountArgs() {
        return Stream.of(
                Arguments.of(null, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100), HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 600),
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("createAccountArgs")
    void shouldCreateAccount(String token, HttpStatus expectedStatus) {
        var requestBody = testAccountService
                .buildCreateAccountRequest(TEST_ACCOUNT_NAME, TEST_CLIENT_3);
        var builder = RequestEntity
                .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                    () -> accountService.assertAccountExistsOrThrow(TEST_NCA_ID));

            // Clear out the Caffeine cache manually so the test can pass by checking real db
            // Caffeine cache will invalidate itself after 60m
            clientService.clearClientCache();
            assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                    () -> clientService.lookupClientOrThrow(TEST_CLIENT_ID));
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.account()).isNotNull();
        assertThat(responseBody.account().name()).isEqualTo(TEST_ACCOUNT_NAME);
        assertThat(responseBody.account().ncaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(responseBody.account().adminClientIds().getFirst()).isEqualTo(TEST_CLIENT_3);

        var accountEntity = accountService.getAccount(TEST_NCA_ID_3);
        assertThat(accountEntity).isNotNull();
        assertThat(accountEntity.getName()).isEqualTo(TEST_ACCOUNT_NAME);
        assertThat(accountEntity.getNcaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(accountEntity.getClientIds()).isNotNull();
        assertThat(accountEntity.getClientIds().stream().toList().getFirst())
                .isEqualTo(TEST_CLIENT_3);

        var clientVo = clientService.lookupClientOrThrow(TEST_CLIENT_3);
        assertThat(clientVo).isNotNull();
        assertThat(clientVo.getName()).isEqualTo(TEST_ACCOUNT_NAME);
        assertThat(clientVo.getNcaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(clientVo.getClientId()).isEqualTo(TEST_CLIENT_3);
    }

    Stream<Arguments> updateMaxAllowedFunctions() {
        return Stream.of(
                Arguments.of(150, 150, HttpStatus.OK),
                Arguments.of(0, 0, HttpStatus.OK),
                Arguments.of(-50, -50, HttpStatus.BAD_REQUEST),
                Arguments.of(150, null, HttpStatus.OK),
                Arguments.of(null, null, HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("updateMaxAllowedFunctions")
    void shouldUpdateMaxAllowedFunctions(
            Integer maxFunctionsAllowed,
            Integer maxTasksAllowed,
            HttpStatus expectedStatus) {
        // Create accounts so that one of them can be updated.
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var requestBody = AccountUpdateRequest.builder()
                .maxFunctionsAllowed(maxFunctionsAllowed)
                .maxTasksAllowed(maxTasksAllowed)
                .build();
        var requestEntity = RequestEntity.patch(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " +
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP),
                                                        100))
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AccountDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        if (HttpStatus.OK == expectedStatus) {
            assertThat(responseEntity.getBody().account().maxFunctionsAllowed())
                    .isEqualTo(maxFunctionsAllowed != null ? maxFunctionsAllowed
                                       : DEFAULT_MAX_FUNCTIONS_ALLOWED);
            assertThat(responseEntity.getBody().account().maxTasksAllowed())
                    .isEqualTo(maxTasksAllowed != null ? maxTasksAllowed
                                       : DEFAULT_MAX_TASKS_ALLOWED);
            assertThat(responseBody.account().lastUpdatedAt()).isNotNull();
        }
    }

    Stream<Arguments> updateMaxAllowedTelemetries() {
        return Stream.of(
                Arguments.of(10, HttpStatus.OK),
                Arguments.of(0, HttpStatus.OK),
                Arguments.of(25, HttpStatus.OK),  // Default max allowed value
                Arguments.of(50, HttpStatus.OK),  // Update max allowed value
                Arguments.of(51, HttpStatus.BAD_REQUEST),  // Exceeds max telemetries
                Arguments.of(-1, HttpStatus.BAD_REQUEST),  // Negative value
                Arguments.of(null, HttpStatus.BAD_REQUEST)  // Null value
        );
    }

    @ParameterizedTest
    @MethodSource("updateMaxAllowedTelemetries")
    void shouldUpdateMaxAllowedTelemetries(
            Integer maxTelemetriesAllowed,
            HttpStatus expectedStatus) {
        // Create accounts so that one of them can be updated.
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var requestBody = AccountUpdateRequest.builder()
                .maxTelemetriesAllowed(maxTelemetriesAllowed)
                .build();
        var requestEntity = RequestEntity.patch(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                                                     List.of(SCOPE_ACCOUNT_SETUP),
                                                                                     100))
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AccountDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        if (HttpStatus.OK == expectedStatus) {
            assertThat(responseEntity.getBody().account().maxTelemetriesAllowed())
                    .isEqualTo(maxTelemetriesAllowed);
            assertThat(responseBody.account().lastUpdatedAt()).isNotNull();
        }
    }

    Stream<Arguments> updateMaxAllowedRegistryCredentials() {
        return Stream.of(
                Arguments.of(15, HttpStatus.OK),
                Arguments.of(0, HttpStatus.OK),
                Arguments.of(25, HttpStatus.OK),  // Default max allowed value
                Arguments.of(50, HttpStatus.OK),  // Update max allowed value
                Arguments.of(51, HttpStatus.BAD_REQUEST),  // Exceeds max registry credentials
                Arguments.of(-1, HttpStatus.BAD_REQUEST),  // Negative value
                Arguments.of(null, HttpStatus.BAD_REQUEST)  // Null value
        );
    }

    @ParameterizedTest
    @MethodSource("updateMaxAllowedRegistryCredentials")
    void shouldUpdateMaxAllowedRegistryCredentials(
            Integer maxRegistryCredentialsAllowed, HttpStatus expectedStatus) {
        // Create accounts so that one of them can be updated.
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var requestBody = AccountUpdateRequest.builder()
                .maxRegistryCredentialsAllowed(maxRegistryCredentialsAllowed)
                .build();
        var requestEntity = RequestEntity.patch(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                                                     List.of(SCOPE_ACCOUNT_SETUP),
                                                                                     100))
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AccountDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        if (HttpStatus.OK == expectedStatus) {
            assertThat(responseEntity.getBody().account().maxRegistryCredentialsAllowed())
                    .isEqualTo(maxRegistryCredentialsAllowed);
            assertThat(responseBody.account().lastUpdatedAt()).isNotNull();
        }
    }

    Stream<Arguments> updateAccountArgs() {
        var jwtCases = Stream.of(
                Arguments.of(null, TEST_NCA_ID, HttpStatus.UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             TEST_NCA_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             TEST_NCA_ID,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             "unknown-account-id",
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_NCA_ID,
                             HttpStatus.OK)
        );
        var apiKeyCases = Stream.of(
                Arguments.of("nvapi-stg-key", TEST_NCA_ID, HttpStatus.FORBIDDEN),
                // Matching NCA Id with correct scope - should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             HttpStatus.OK),
                // Mismatched NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID_2,
                             FORBIDDEN),
                // Non-existent NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-account", TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             FORBIDDEN),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID_3, TEST_OWNER_ID,
                                             List.of(),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             FORBIDDEN),
                // Invalid scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID_3, TEST_OWNER_ID,
                                             List.of(),
                                             List.of("invalid-scope"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             FORBIDDEN)
        );
        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("updateAccountArgs")
    void shouldUpdateAccount(
            Object tokenSupplier,
            String ncaId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create accounts so that one of them can be updated.
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var updatedName = TEST_ACCOUNT_NAME + "-updated";
        var requestBody = AccountUpdateRequest.builder()
                .name(updatedName)
                .build();

        var builder = RequestEntity
                .patch(URI.create("/v2/nvcf/accounts/" + ncaId))
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, AccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.account()).isNotNull();
        assertThat(responseBody.account().name()).isEqualTo(updatedName);
        assertThat(responseBody.account().ncaId()).isEqualTo(ncaId);

        var accountEntity = accountService.getAccount(ncaId);
        assertThat(accountEntity).isNotNull();
        assertThat(accountEntity.getNcaId()).isEqualTo(ncaId);
        assertThat(accountEntity.getName()).isEqualTo(updatedName);
    }

    @Test
    void shouldCreateAccountWithoutOAuth2Client() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(SCOPE_ACCOUNT_SETUP), 100);

        // Create TEST_NCA_ID_3 without any clients associated with it.
        var requestBody = testAccountService.buildCreateAccountRequest(TEST_ACCOUNT_NAME_3, null);
        var builder = RequestEntity
                .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.account()).isNotNull();
        assertThat(responseBody.account().name()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(responseBody.account().ncaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(responseBody.account().adminClientIds()).isNull();

        // Assert account was created.
        var accountEntity = accountService.getAccount(TEST_NCA_ID_3);
        assertThat(accountEntity).isNotNull();
        assertThat(accountEntity.getName()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(accountEntity.getNcaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(accountEntity.getClientIds()).isNull();

        // Assert client mapping was not created.
        assertThat(clientsRepository.findAll()).isEmpty();
    }

    @Test
    void shouldFailToDisassociateNonExistentClient() {
        testAccountService.createDefaultAccountsClientsAndRegistries();

        // Assert clients were created.
        assertThat(clientsRepository.findAll()).isNotEmpty();
        var clientIds = clientsRepository.findAll().stream()
                .map(ClientEntity::getClientId)
                .collect(Collectors.toSet());

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(SCOPE_ACCOUNT_SETUP), 100);

        // Create TEST_NCA_ID_3 without any clients associated with it.
        var requestBody = testAccountService.buildCreateAccountRequest(TEST_ACCOUNT_NAME_3, null);
        var requestEntity = RequestEntity
                .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.account()).isNotNull();
        assertThat(responseBody.account().name()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(responseBody.account().ncaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(responseBody.account().adminClientIds()).isNull();

        // Assert TEST_NCA_ID_3 was created.
        var accountEntity = accountService.getAccount(TEST_NCA_ID_3);
        assertThat(accountEntity).isNotNull();
        assertThat(accountEntity.getName()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(accountEntity.getNcaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(accountEntity.getClientIds()).isNull();

        // Assert we still have same clients in the DB
        var clientIdsNow = clientsRepository.findAll().stream()
                .map(ClientEntity::getClientId)
                .collect(Collectors.toSet());
        assertThat(clientIdsNow).containsExactlyInAnyOrderElementsOf(clientIds);

        // Make sure that TEST_CLIENT_ID exists as we are going to disassociate it from
        // TEST_NCA_ID_3 account created above.
        assertThat(clientIdsNow).contains(TEST_CLIENT_ID);

        // Disassociate client that is not associated with TEST_NCA_ID_3.
        var patchRequestBody = PatchAccountRequest.builder().adminClientId(TEST_CLIENT_ID).build();
        var patchRequestEntity = RequestEntity
                .patch(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3 + "/clients/disassociate"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(patchRequestBody);
        var patchResponseEntity = testRestTemplate.exchange(patchRequestEntity, AccountResponse.class);
        assertThat(patchResponseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldFailWhenCreatingAccountWithExistingClientId() {
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP), 100);
        var requestBody = testAccountService
                .buildCreateAccountRequest("nvcf-test-account", TEST_CLIENT_ID);
        var requestEntity = RequestEntity
                .post(URI.create("/v2/nvcf/accounts/" + "nvcf-test-nca-id"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.CONFLICT);
    }

    Stream<Arguments> updateAccountArgsWithExistingClientId() {
        return Stream.of(
                Arguments.of(TEST_ACCOUNT_NAME + "-updated",
                             HttpStatus.OK),
                Arguments.of(null,
                             HttpStatus.BAD_REQUEST));
    }

    @ParameterizedTest
    @MethodSource("updateAccountArgsWithExistingClientId")
    void shouldUpdateAccountWithExistingClientId(
            String updatedAccountName,
            HttpStatus expectedStatus) {
        // Create accounts so that one of them can be updated.
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP), 100);
        var requestBody = AccountUpdateRequest.builder()
                .name(updatedAccountName)
                .build();

        var requestEntity = RequestEntity.patch(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        assertThat(responseEntity.getBody()).isNotNull();
        assertThat(responseEntity.getBody().account().name()).isNotBlank();

        var accountEntity = accountService.getAccount(TEST_NCA_ID);
        if (StringUtils.isBlank(updatedAccountName)) {
            assertThat(accountEntity.getName()).isEqualTo(TEST_ACCOUNT_NAME);
        } else {
            assertThat(accountEntity.getName()).isEqualTo(updatedAccountName);
        }
    }

    @Test
    void shouldFailWhenCreatingAccountWithExistingNcaId() {
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP), 100);
        var requestBody = testAccountService
                .buildCreateAccountRequest("nvcf-test-account", "nvcf-test-client-id");

        // Account with nca_id=test-nca-id has been created already.
        var requestEntity = RequestEntity
                .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.CONFLICT);
    }

    Stream<Arguments> invalidPayloadArgs() {
        return Stream.of(
                // Null
                Arguments.of(null, TEST_CLIENT_ID),
                // Empty
                Arguments.of(EMPTY, TEST_CLIENT_ID),
                // Blank
                Arguments.of("  ", TEST_CLIENT_ID),
                // name len is < 4
                Arguments.of("abc", TEST_CLIENT_ID),
                // Regex violation
                Arguments.of("invalid-chars-!%$#^&*-in-name", TEST_CLIENT_ID),
                // name len > 36
                Arguments.of("name-is-more-than-thirty-six-characters", TEST_CLIENT_ID)
        );
    }

    @ParameterizedTest
    @MethodSource("invalidPayloadArgs")
    void validateRequestPayload(String name, String adminClientId) {

        var requestBody = testAccountService.buildCreateAccountRequest(name, adminClientId);
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP), 100);
        var requestEntity = RequestEntity
                .post(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID))
                .header("Authorization", "Bearer " + token)
                .contentType(MediaType.APPLICATION_JSON)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, Map.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertThat(responseEntity.getBody()).isNotNull();
    }

    Stream<Arguments> ncaIdArgs() {
        return Stream.of(
                Arguments.of("aabababa*!@@", HttpStatus.OK),
                Arguments.of("AAAababab12123a_*", HttpStatus.OK),
                Arguments.of("332aabababa*@@", HttpStatus.OK),
                Arguments.of("!332aabababa*@@", HttpStatus.BAD_REQUEST),
                Arguments.of("!___&332aabababa@*", HttpStatus.BAD_REQUEST)
        );
    }
    @ParameterizedTest
    @MethodSource("ncaIdArgs")
    void shouldFailWhenSettingUpAccountWithInvalidNcaId(String ncaId, HttpStatus expected) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP), 100);
        var requestBody = testAccountService
                .buildCreateAccountRequest("nvcf-test-account", "nvcf-test-client-id");

        // Account with nca_id=test-nca-id has been created already.
        var requestEntity = RequestEntity
                .post(URI.create("/v2/nvcf/accounts/" + ncaId))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expected);
    }
}
