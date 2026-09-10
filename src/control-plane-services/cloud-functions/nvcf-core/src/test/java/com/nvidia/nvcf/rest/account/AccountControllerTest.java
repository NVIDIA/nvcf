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
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_MANAGE_TELEMETRIES;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_ACCOUNT_SETUP;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_5;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOG_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.DEFAULT_MAX_FUNCTIONS_ALLOWED;
import static com.nvidia.nvcf.util.TestConstants.DEFAULT_MAX_TASKS_ALLOWED;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;
import static org.springframework.http.HttpStatus.BAD_REQUEST;
import static org.springframework.http.HttpStatus.CONFLICT;
import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.NOT_FOUND;
import static org.springframework.http.HttpStatus.NO_CONTENT;
import static org.springframework.http.HttpStatus.OK;
import static org.springframework.http.HttpStatus.UNAUTHORIZED;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.client.ClientsRepository;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.account.dto.AccountDetailsResponse;
import com.nvidia.nvcf.rest.account.dto.AccountResponse;
import com.nvidia.nvcf.rest.account.dto.ListAccountResponse;
import com.nvidia.nvcf.rest.account.dto.PatchAccountRequest;
import com.nvidia.nvcf.rest.azp.TestAuthorizedPartiesService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.telemetry.TestTelemetryService;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.client.ClientService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.telemetry.TelemetryMapperService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.NvcfConstants;
import com.nvidia.nvcf.util.TestUtil;
import java.net.URI;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
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
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
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
class AccountControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private ClientsRepository clientsRepository;

    @Autowired
    private AccountService accountService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private ClientService clientService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Autowired
    private TelemetryMapperService telemetryMapperService;

    @Autowired
    private EssService essService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

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
        testTelemetryService.deleteAllTelemetries();
        testAccountService.cleanupAccountsClientsAndRegistries();
    }

    Stream<Arguments> getAccountsArgs() {
        var jwtCases = Stream.of(
                Arguments.of(null, 8, UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             8,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             8,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             8, // With JWT, the response will contain info about all accounts.
                             OK)
        );
        var apiKeyCases = Stream.of(
                Arguments.of("nvapi-stg-key", 1, FORBIDDEN),
                // Valid scope -- should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             1,  // With apikey, the response will only contain info about TEST_NCA_ID.
                             OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-account", TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             0,  // With apikey, the response will be empty list.
                             OK),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             1,
                             FORBIDDEN),
                // Invalid scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of("invalid-scope"));
                                 return "nvapi-stg-some-key";
                             },
                             1,
                             FORBIDDEN)
                );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("getAccountsArgs")
    void shouldGetAccounts(
            Object tokenSupplier,
            int expectedNumberOfAccounts,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var builder = RequestEntity.get(
                URI.create("/v2/nvcf/accounts"));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.cloudAccounts()).hasSize(expectedNumberOfAccounts);
        if (expectedNumberOfAccounts > 0) {
            assertThat(responseBody.cloudAccounts().getFirst().name()).isNotBlank();
            assertThat(
                    responseBody.cloudAccounts().getFirst().adminClientIds()
                            .getFirst()).isNotBlank();
            assertThat(responseBody.cloudAccounts().getFirst().ncaId()).isNotBlank();
            assertThat(responseBody.cloudAccounts().getFirst().maxTelemetriesAllowed())
                    .isEqualTo(
                            NvcfConstants.DEFAULT_MAX_TELEMETRIES_ALLOWED);
            assertThat(responseBody.cloudAccounts().getFirst().maxRegistryCredentialsAllowed())
                    .isEqualTo(
                            NvcfConstants.DEFAULT_MAX_REGISTRY_CREDENTIALS_ALLOWED);
        }
    }

    Stream<Arguments> getAccountsDetailsArgs() {
        var jwtCases = Stream.of(
                Arguments.of(null, TEST_NCA_ID, Set.of(TEST_CLIENT_ID), UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             "INVALID_NCA_ID",
                             null,
                             NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_AUTHORIZED_NCA_ID_5,
                             null,
                             OK)
        );
        var apiKeyCases = Stream.of(
                Arguments.of("nvapi-stg-key", TEST_NCA_ID, Set.of(TEST_CLIENT_ID), FORBIDDEN),
                // Valid scope and NCA ID -- should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             OK),
                // Mismatched NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID_2,
                             Set.of(TEST_CLIENT_ID),
                             FORBIDDEN),
                // Non-existent NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             "non-existent-account",
                             Set.of(TEST_CLIENT_ID),
                             FORBIDDEN),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             FORBIDDEN),
                // Invalid scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of("invalid-scope"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("getAccountsDetailsArgs")
    void shouldGetAccountDetails(
            Object tokenSupplier,
            String ncaId,
            Set<String> clientIds,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var builder = RequestEntity.get(
                URI.create("/v2/nvcf/accounts/" + ncaId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, AccountDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.account()).isNotNull();
        assertThat(responseBody.account().ncaId()).isEqualTo(ncaId);
        if (clientIds == null) {
            assertThat(responseBody.account().clientIds()).isNull();
        } else {
            assertThat(responseBody.account().clientIds()).hasSize(1);
            assertThat(responseBody.account().clientIds()).contains(TEST_CLIENT_ID);
        }
        assertThat(responseBody.account().name()).isNotBlank();
        assertThat(responseBody.account().maxFunctionsAllowed())
                .isEqualTo(DEFAULT_MAX_FUNCTIONS_ALLOWED);
        assertThat(responseBody.account().currentNumberFunctions())
                .isEqualTo(0);
        assertThat(responseBody.account().maxTasksAllowed())
                .isEqualTo(DEFAULT_MAX_TASKS_ALLOWED);
        assertThat(responseBody.account().maxTelemetriesAllowed())
                .isEqualTo(NvcfConstants.DEFAULT_MAX_TELEMETRIES_ALLOWED);
        assertThat(responseBody.account().maxRegistryCredentialsAllowed())
                .isEqualTo(NvcfConstants.DEFAULT_MAX_REGISTRY_CREDENTIALS_ALLOWED);
        assertThat(responseBody.account().lastUpdatedAt()).isNotNull();
        assertThat(responseBody.account().registryCredentials()).isNotEmpty().hasSize(3);
        assertThat(responseBody.account().registryCredentials())
                .allSatisfy(registryCredential -> {
                    assertThat(registryCredential.registryCredentialId()).isNotNull();
                    assertThat(registryCredential.secret()).isNotNull();
                });
    }

    Stream<Arguments> getAccountWithTelemetryArgs() {
        return Stream.of(
                // Empty Token
                Arguments.of(null,
                        TEST_NCA_ID,
                        Set.of(TEST_CLIENT_ID),
                        TEST_TELEMETRY_LOGS_ID,
                        TEST_TELEMETRY_ENDPOINT,
                        TelemetryProtocol.HTTP,
                        TelemetryProvider.PROMETHEUS,
                        Set.of(TelemetryType.LOGS),
                        TEST_TELEMETRY_LOG_SECRETS,
                        UNAUTHORIZED),
                // Invalid token
                Arguments.of("nvapi-stg-key",
                        TEST_NCA_ID,
                        Set.of(TEST_CLIENT_ID),
                        TEST_TELEMETRY_LOGS_ID,
                        TEST_TELEMETRY_ENDPOINT,
                        TelemetryProtocol.HTTP,
                        TelemetryProvider.PROMETHEUS,
                        Set.of(TelemetryType.LOGS),
                        TEST_TELEMETRY_LOG_SECRETS,
                        FORBIDDEN),
                // Empty Scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             TEST_TELEMETRY_LOGS_ID,
                             TEST_TELEMETRY_ENDPOINT,
                             TelemetryProtocol.HTTP,
                             TelemetryProvider.PROMETHEUS,
                             Set.of(TelemetryType.LOGS),
                             TEST_TELEMETRY_LOG_SECRETS,
                             FORBIDDEN),
                // Invalid Scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             TEST_TELEMETRY_LOGS_ID,
                             TEST_TELEMETRY_ENDPOINT,
                             TelemetryProtocol.HTTP,
                             TelemetryProvider.PROMETHEUS,
                             Set.of(TelemetryType.LOGS),
                             TEST_TELEMETRY_LOG_SECRETS,
                             FORBIDDEN),
                // No client ID
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_AUTHORIZED_NCA_ID_5,
                             null,
                             TEST_TELEMETRY_LOGS_ID,
                             TEST_TELEMETRY_ENDPOINT,
                             TelemetryProtocol.HTTP,
                             TelemetryProvider.PROMETHEUS,
                             Set.of(TelemetryType.LOGS),
                             TEST_TELEMETRY_LOG_SECRETS,
                             OK),
                // valid telemetry
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP,ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                             100),
                             TEST_NCA_ID,
                             Set.of(TEST_CLIENT_ID),
                             TEST_TELEMETRY_LOGS_ID,
                             TEST_TELEMETRY_ENDPOINT,
                             TelemetryProtocol.HTTP,
                             TelemetryProvider.PROMETHEUS,
                             Set.of(TelemetryType.LOGS),
                             TEST_TELEMETRY_LOG_SECRETS,
                             OK)
        );
    }

    @ParameterizedTest
    @MethodSource("getAccountWithTelemetryArgs")
    void shouldGetAccountWithTelemetry(
            String token,
            String ncaId,
            Set<String> clientIds,
            UUID telemetryId,
            String telemetryEndpoint,
            TelemetryProtocol protocol,
            TelemetryProvider provider,
            Set<TelemetryType> type,
            SecretDto secret,
            HttpStatus expectedStatus) {
        testAccountService.createAccountAndAssociateClients(ncaId, clientIds);
        testTelemetryService.createTelemetry(
                ncaId,
                telemetryId,
                telemetryEndpoint,
                protocol,
                provider,
                type,
                secret);

        var builder = RequestEntity.get(URI.create("/v2/nvcf/accounts/" + ncaId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, AccountDetailsResponse.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.account()).isNotNull();
        assertThat(responseBody.account().ncaId()).isEqualTo(ncaId);

        var telemetryResponse = responseBody.account().telemetries();
        assertThat(telemetryResponse).isNotNull();
        assertThat(telemetryResponse).hasSizeGreaterThan(0);

        var telemetry = telemetryResponse.getFirst();
        assertThat(telemetry).isNotNull();

        var actualEndpoint = telemetry.endpoint();
        var actualProtocol = telemetry.protocol();
        var actualProvider = telemetry.provider();
        var actualTypes = telemetry.types();

        var expectedProtocol = telemetryMapperService.toTelemetryProtocolEnum(protocol);
        var expectedProvider = telemetryMapperService.toTelemetryProviderEnum(provider);
        var expectedTypes = telemetryMapperService.toTelemetryTypeEnums(type);

        assertThat(actualEndpoint).isEqualTo(telemetryEndpoint);
        assertThat(actualProtocol).isEqualTo(expectedProtocol);
        assertThat(actualProvider).isEqualTo(expectedProvider);
        assertThat(actualTypes).isEqualTo(expectedTypes);
    }

    Stream<Arguments> associateMultipleJwtClientsArgs() {
        var jwtCases = Stream.of(
                Arguments.of(null, TEST_CLIENT_3, UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             TEST_CLIENT_3, FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             TEST_CLIENT_3, FORBIDDEN),
                // Associate Client JWT that is already associated with the account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_CLIENT_ID, CONFLICT),
                // Associate Client JWT that is associated with another account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_CLIENT_ID_2, CONFLICT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_CLIENT_3, OK));
        var apiKeyCases = Stream.of(
                Arguments.of("nvapi-stg-key", TEST_NCA_ID, FORBIDDEN),
                // Valid scope and NCA ID -- should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_3,
                             OK),
                // Associate TEST_CLIENT_ID that is already associated with TEST_NCA_ID.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_ID,
                             CONFLICT),
                // Associate TEST_CLIENT_ID_2 that is already associated with TEST_NCA_ID_2.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_ID_2,
                             CONFLICT),
                // Mismatched NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID_2, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_3,
                             FORBIDDEN),
                // Non-existent NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-account", TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_3,
                             FORBIDDEN),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_3,
                             FORBIDDEN),
                // Invalid scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of("invalid-scope"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_3,
                             FORBIDDEN)

        );
        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("associateMultipleJwtClientsArgs")
    void shouldAssociateMultipleOAuth2ClientsWithCloudAccount(
            Object tokenSupplier,
            String clientId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        testAccountService.createDefaultAccountsClientsAndRegistries();

        // Account TEST_ACCOUNT_NAME for NVIDIA Cloud Account TEST_NCA_ID is already
        // associated with OAuth2 Client TEST_CLIENT_ID.
        var requestBody = PatchAccountRequest.builder().adminClientId(clientId).build();
        var endpoint = URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/clients/associate");
        var builder = RequestEntity.patch(endpoint).contentType(MediaType.APPLICATION_JSON);
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
        assertThat(responseBody.account().name()).isEqualTo(TEST_ACCOUNT_NAME);
        assertThat(responseBody.account().adminClientIds()).hasSize(2);
        assertThat(responseBody.account().adminClientIds())
                .containsExactlyInAnyOrder(TEST_CLIENT_ID, TEST_CLIENT_3);
        assertThat(responseBody.account().ncaId()).isEqualTo(TEST_NCA_ID);

        var client = clientsRepository.findById(clientId);
        assertThat(client).isNotNull();
        assertThat(client).isPresent();
        assertThat(client.get().getClientId()).isEqualTo(clientId);
        assertThat(client.get().getNcaId()).isEqualTo(TEST_NCA_ID);
        assertThat(client.get().getName()).isEqualTo(TEST_ACCOUNT_NAME);
    }

    Stream<Arguments> disAssociateOAuth2ClientArgs() {
        var jwtCases = Stream.of(
                Arguments.of(null, TEST_CLIENT_ID, UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             TEST_CLIENT_ID, FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             TEST_CLIENT_ID, FORBIDDEN),
                // Disassociate OAuth2 Client that is not registered with the account.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_CLIENT_ID_2, BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             TEST_CLIENT_ID, OK));
        var apiKeyCases = Stream.of(
                Arguments.of("nvapi-stg-key", TEST_NCA_ID, FORBIDDEN),
                // Valid scope and NCA ID -- should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_ID,
                             OK),
                // Disassociate TEST_CLIENT_ID_2 that is not associated with TEST_NCA_ID.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_ID_2,
                             BAD_REQUEST),
                // Mismatched NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID_2, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_ID,
                             FORBIDDEN),
                // Non-existent NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-account", TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_ACCOUNT_SETUP));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_ID,
                             FORBIDDEN),
                // Missing scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_ID,
                             FORBIDDEN),
                // Invalid scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of("invalid-scope"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_CLIENT_ID,
                             FORBIDDEN)
        );
        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("disAssociateOAuth2ClientArgs")
    void shouldDisassociateOAuth2ClientFromCloudAccount(
            Object tokenSupplier,
            String clientId,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        testAccountService.createDefaultAccountsClientsAndRegistries();

        // Account TEST_ACCOUNT_NAME for NVIDIA Cloud Account TEST_NCA_ID is already
        // associated with OAuth2 Client TEST_CLIENT_ID.
        var requestBody = PatchAccountRequest.builder().adminClientId(clientId).build();
        var endpoint = URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/clients/disassociate");
        var builder = RequestEntity.patch(endpoint).contentType(MediaType.APPLICATION_JSON);
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
        assertThat(responseBody.account().name()).isEqualTo(TEST_ACCOUNT_NAME);
        assertThat(responseBody.account().adminClientIds()).isEmpty();
        assertThat(responseBody.account().ncaId()).isEqualTo(TEST_NCA_ID);

        var client = clientsRepository.findById(clientId);
        assertThat(client).isEmpty();
    }

    Stream<Arguments> deleteAccountsArgs() {
        var ncaId = "cloud-account-1";
        var clientIds = Set.of("client-id-1", "client-id-2", "client-id-3");
        return Stream.of(
                Arguments.of(null, ncaId, clientIds, UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             ncaId,
                             clientIds,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             ncaId,
                             clientIds,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             "unknown-nca-id",
                             clientIds,
                             NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             ncaId,
                             clientIds,
                             NO_CONTENT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             ncaId,
                             Set.of(),
                             NO_CONTENT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 600),
                             ncaId,
                             null,
                             NO_CONTENT)
        );
    }

    @ParameterizedTest
    @MethodSource("deleteAccountsArgs")
    void shouldDeleteAccount(
            Object tokenSupplier,
            String ncaId,
            Set<String> clientIds,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        // Create an account and associate clients.
        testAccountService.createAccountAndAssociateClients(ncaId, clientIds);
        var accountId = ncaId;
        if (expectedStatus == NOT_FOUND) {
            accountId = "non-existent-cloud-account-1";
        }

        // Delete account.
        var builder = RequestEntity.delete(URI.create("/v2/nvcf/accounts/" + accountId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            // Check if the DB contains the rows for account/clients.
            if (clientIds != null) {
                clientIds.forEach(clientId -> {
                    var client = clientService.lookupClient(clientId);
                    assertThat(client).isNotNull();
                    assertThat(client).isPresent();
                    assertThat(client.get().getClientId()).isNotBlank();
                    assertThat(client.get().getClientId()).isEqualTo(clientId);
                });
            }

            return;
        }
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNull();

        // Check if the rows are deleted from the tables.
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> accountService.assertAccountExistsOrThrow(ncaId));
        if (clientIds != null) {
            // Clear out the Caffeine cache manually after deletion so the test can pass by checking
            // real db. Caffeine cache will invalidate itself after 60m
            clientService.clearClientCache();
            clientIds.forEach(clientId ->
                    assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                            () -> clientService.lookupClientOrThrow(clientId)));
        }
    }

    @Test
    void shouldNotDeleteAccountWithFunctions() {
        // Create an account and associate clients.
        var ncaId = "cloud-account-1";
        var clientIds = Set.of("client-id-1", "client-id-2", "client-id-3");
        testAccountService.createAccountAndAssociateClients(ncaId, clientIds);

        // Create functions in the account.
        TestUtil.insertFunctions(functionsRepository, ncaId, FunctionStatus.INACTIVE);

        // Try deleting account.
        assertThatExceptionOfType(BadRequestException.class)
                .isThrownBy(() -> accountService.deleteAccount(
                        ncaId, testCommonService.getAuditEventPayloadBuilder()));

        // Check if the account and client rows still exist.
        accountService.assertAccountExistsOrThrow(ncaId);
        clientIds.forEach(clientId -> assertThat(clientService.lookupClientOrThrow(clientId))
                                                    .isNotNull());
    }


    @Test
    void shouldDeleteAccountWithTelemetrySecret() {
        // Create an account and associate clients.
        var ncaId = "cloud-account-1";
        var clientIds = Set.of("client-id-1", "client-id-2", "client-id-3");
        testAccountService.createAccountAndAssociateClients(ncaId, clientIds);

        // Create telemetry Entries and account secrets
        testTelemetryService.createTelemetry(
                ncaId,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS);

        accountService.deleteAccount(
                ncaId, testCommonService.getAuditEventPayloadBuilder());

        var secret = essService.getTelemetrySecret(ncaId, TEST_TELEMETRY_LOGS_ID);
        assertThat(secret).isNotNull().isEmpty();
    }

    @Test
    void shouldNotDeleteAccountAuthorizedToInvokeListFunctions() {
        // Create an account and associate clients.
        var ncaId = "cloud-account-1";
        var clientIds = Set.of("client-id-1", "client-id-2", "client-id-3");
        testAccountService.createAccountAndAssociateClients("cloud-account-1", clientIds);

        // Create a function TEST_FUNCTION_ID in account TEST_NCA_ID.
        testAuthPartiesService.createTestFunction(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                TEST_NCA_ID, TEST_FUNCTION_NAME);

        // Associate "cloud-account-1" as an authorized party for the function.
        var authorizedParties = Set.of(
                AuthorizedPartyDto.builder().ncaId(ncaId).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    Optional.empty(),
                                                    authorizedParties);

        // Try deleting "cloud-account-1".
        assertThatExceptionOfType(BadRequestException.class)
                .isThrownBy(() -> accountService.deleteAccount(
                        ncaId, testCommonService.getAuditEventPayloadBuilder()));

        // Check if the account and client rows still exist.
        accountService.assertAccountExistsOrThrow(ncaId);
        clientIds.forEach(clientId -> assertThat(clientService.lookupClientOrThrow(clientId))
                .isNotNull());
    }
}
