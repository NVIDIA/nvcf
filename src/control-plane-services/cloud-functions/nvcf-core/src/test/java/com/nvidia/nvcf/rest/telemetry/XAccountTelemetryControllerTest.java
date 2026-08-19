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
package com.nvidia.nvcf.rest.telemetry;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_MANAGE_TELEMETRIES;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOG_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_SECRETS;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.telemetry.dto.ListTelemetryResponse;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProtocolEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProviderEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryRequest;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryResponse;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryTypeEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.telemetry.TelemetryLookupService;
import com.nvidia.nvcf.service.telemetry.TelemetryService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
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
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.StringNode;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class XAccountTelemetryControllerTest {
    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Autowired
    private TelemetryService telemetryService;

    @Autowired
    private TelemetryLookupService telemetryLookupService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private EssService essService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        MockApiKeysServer.start(apiKeysBaseUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testCommonService.reset();
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockApiKeysServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testTelemetryService.deleteAllTelemetries();
        resetToDefault();
    }

    Stream<Arguments> shouldFailWhenTypesAreInvalidArgs() {
        return Stream.of(
            // Missing types field
            Arguments.of(
                TEST_NCA_ID,
                """
                {
                    "endpoint": "http://example-telemetry-endpoint.com",
                    "protocol": "HTTP",
                    "provider": "PROMETHEUS",
                    "secret": {
                        "name": "telemetry-secret",
                        "value": {
                            "key": "value"
                        }
                    }
                }
                """,
               HttpStatus.BAD_REQUEST),
           // Empty types field
            Arguments.of(
                TEST_NCA_ID,
                """
                {
                    "endpoint": "http://example-telemetry-endpoint.com",
                    "protocol": "HTTP",
                    "provider": "PROMETHEUS",
                    "types": [],
                    "secret": {
                        "name": "telemetry-secret",
                        "value": {
                            "key": "value"
                        }
                    }
                }
                """,
                HttpStatus.BAD_REQUEST),
            // Null types field
            Arguments.of(
                TEST_NCA_ID,
                """
                {
                    "endpoint": "http://example-telemetry-endpoint.com",
                    "protocol": "HTTP",
                    "provider": "PROMETHEUS",
                    "types": null,
                    "secret": {
                        "name": "telemetry-secret",
                        "value": {
                            "key": "value"
                        }
                    }
                }
                """,
                HttpStatus.BAD_REQUEST),
            // Invalid type value
            Arguments.of(
                TEST_NCA_ID,
                """
                {
                    "endpoint": "http://example-telemetry-endpoint.com",
                    "protocol": "HTTP",
                    "provider": "PROMETHEUS",
                    "types": ["INVALID_TYPE"],
                    "secret": {
                        "name": "telemetry-secret",
                        "value": {
                            "key": "value"
                        }
                    }
                }
                """,
                HttpStatus.BAD_REQUEST),
            // Mixed valid and invalid types
            Arguments.of(
                TEST_NCA_ID,
                """
                {
                    "endpoint": "http://example-telemetry-endpoint.com",
                    "protocol": "HTTP",
                    "provider": "PROMETHEUS",
                    "types": ["LOGS", "INVALID_TYPE"],
                    "secret": {
                        "name": "telemetry-secret",
                        "value": {
                            "key": "value"
                        }
                    }
                }
                """,
                HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("shouldFailWhenTypesAreInvalidArgs")
    void shouldFailWhenTypesAreInvalid(
            String ncaId,
            String rawJsonInput,
            HttpStatus expectedStatus) {

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                    100);

        var url = URI.create("/v2/nvcf/accounts/" + ncaId + "/telemetries");
        var requestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(rawJsonInput);

        var response = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);

        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);

        if (!expectedStatus.isError()) {
            assertThat(response.getBody()).isNotNull();
        }
    }

    Stream<Arguments> createTelemetryArgs() {
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("GRAFANA", "GRAFANA_PASSWORD");

        var telemetryRequest = TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.PROMETHEUS)
                .types(Set.of(TelemetryTypeEnum.LOGS))
                .secret(SecretDto.builder()
                                .name("prometheus-secret")
                                .value(jsonMapper.createObjectNode()
                                               .put("clientCert",
                                                    "client-cert")
                                               .put("clientKey", "cert-key")
                                               .put("caCert", "ca-cert"))
                                .build())
                .build();
        return Stream.of(
                // Missing Authorization header
                Arguments.of(
                        null,
                        TEST_NCA_ID,
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.UNAUTHORIZED
                ),
                // Invalid token
                Arguments.of(
                        "invalid-token",
                        TEST_NCA_ID,
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.UNAUTHORIZED
                ),
                // Missing scope in token
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(),
                                                        100),
                        TEST_NCA_ID,
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.FORBIDDEN
                ),
                // Missing protocol
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(null)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Missing types
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(null) // Missing types
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Missing secret
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(null) // Missing secret
                                .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // No secret
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Invalid NCA ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        "invalid-nca-id",
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.NOT_FOUND
                ),
                // Success case
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                )
        );
    }

    @ParameterizedTest
    @MethodSource("createTelemetryArgs")
    void shouldCreateTelemetry(
            Object tokenSupplier,
            String ncaId,
            TelemetryRequest telemetryRequest,
            HttpStatus expectedStatus) {

        var token = getToken(tokenSupplier);

        var url = URI.create("/v2/nvcf/accounts/" + ncaId + "/telemetries");
        var requestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(telemetryRequest);

        var responseEntity = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var telemetryId = responseBody.telemetry().telemetryId();
        assertThat(telemetryId).isNotNull();

        var getTelemetryUrl =
                URI.create("/v2/nvcf/accounts/" + ncaId + "/telemetries/" + telemetryId);
        var request = RequestEntity.get(getTelemetryUrl)
                .header("Authorization", "Bearer " + token)
                .build();

        var response = testRestTemplate.exchange(request, TelemetryResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);

        var getResponseBody = response.getBody();
        assertThat(getResponseBody).isNotNull();

        var telemetry = getResponseBody.telemetry();
        assertThat(telemetry.endpoint()).isEqualTo(telemetryRequest.endpoint());
        assertThat(telemetry.protocol()).isEqualTo(telemetryRequest.protocol());
        assertThat(telemetry.provider()).isEqualTo(telemetryRequest.provider());
        assertThat(telemetry.types()).isEqualTo(telemetryRequest.types());
        assertThat(telemetry.name()).isEqualTo(telemetryRequest.secret().name());

        var telemetrySecret = essService.getTelemetrySecret(ncaId, telemetryId);

        assertThat(telemetrySecret).isPresent();
        telemetrySecret.ifPresent(secretDto -> {
            assertThat(secretDto.name()).isEqualTo(telemetryRequest.secret().name());
            assertThat(secretDto.value()).isEqualTo(telemetryRequest.secret().value());
        });
    }

    Stream<Arguments> listTelemetriesArgs() {
        return Stream.of(
                // No entries created
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        0,
                        HttpStatus.OK,
                        0
                ),
                // Single entry created
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        1,
                        HttpStatus.OK,
                        1
                ),
                // Multiple entries created
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        5,
                        HttpStatus.OK,
                        5
                ),
                // No authorization scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(), 100),
                        TEST_NCA_ID,
                        5,
                        HttpStatus.FORBIDDEN,
                        0
                ),
                // Expired token
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        -100), // Expired token
                        TEST_NCA_ID,
                        5,
                        HttpStatus.UNAUTHORIZED,
                        0
                )
        );
    }

    @ParameterizedTest
    @MethodSource("listTelemetriesArgs")
    void shouldListTelemetries(
            Object tokenSupplier,
            String ncaId,
            int actualCount,
            HttpStatus expectedStatus,
            int expectedCount) {

        for (int i = 0; i < actualCount; i++) {
            SecretDto secret = SecretDto.builder()
                    .name("telemetry-secret-name"+ i)
                    .value(new StringNode("telemetry-secret-value"))
                    .build();

            testTelemetryService.createTelemetry(
                    TEST_NCA_ID,
                    UUID.randomUUID(),
                    TEST_TELEMETRY_ENDPOINT,
                    TelemetryProtocol.HTTP,
                    TelemetryProvider.PROMETHEUS,
                    Set.of(TelemetryType.LOGS),
                    secret
            );
        }

        var token = getToken(tokenSupplier);
        var getAllUrl = URI.create("/v2/nvcf/accounts/" + ncaId + "/telemetries");
        var getAllRequestEntity = RequestEntity.get(getAllUrl)
                .header("Authorization", "Bearer " + token)
                .build();

        var response = testRestTemplate.exchange(getAllRequestEntity, ListTelemetryResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().telemetries()).hasSize(expectedCount);
    }

    Stream<Arguments> getTelemetryDetailsArgs() {
        return Stream.of(
                // Valid ID - Should retrieve successfully
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.OK
                ),
                // Non-existent ID - Should return NOT_FOUND
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        UUID.randomUUID(),
                        HttpStatus.NOT_FOUND
                ),
                // Invalid ID format - Should return BAD_REQUEST
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        null, // Passing null to simulate an invalid UUID
                        HttpStatus.BAD_REQUEST
                ),
                // Unauthorized access - Should return UNAUTHORIZED
                Arguments.of(
                        "invalid-token",
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.UNAUTHORIZED
                ),
                // Missing required scope - Should return FORBIDDEN
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.FORBIDDEN
                )
        );
    }

    @ParameterizedTest
    @MethodSource("getTelemetryDetailsArgs")
    void shouldGetTelemetryDetails(
            Object tokenSupplier,
            String ncaId,
            UUID telemetryId,
            HttpStatus expectedStatus) {

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID, TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS), TEST_TELEMETRY_LOG_SECRETS
        );

        var token = getToken(tokenSupplier);
        var getUrl = URI.create("/v2/nvcf/accounts/" + ncaId + "/telemetries/" + telemetryId);
        var getRequestEntity = RequestEntity.get(getUrl)
                .header("Authorization", "Bearer " + token)
                .build();

        var response = testRestTemplate.exchange(getRequestEntity, TelemetryResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = response.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.telemetry().endpoint())
                .isEqualTo(TEST_TELEMETRY_ENDPOINT);
        assertThat(responseBody.telemetry().protocol())
                .isEqualTo(TelemetryProtocolEnum.HTTP);
        assertThat(responseBody.telemetry().provider())
                .isEqualTo(TelemetryProviderEnum.DATADOG);
    }

    Stream<Arguments> deleteTelemetryArgs() {
        return Stream.of(
                // Valid ID - Successful deletion
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.NO_CONTENT
                ),
                // Non-existent ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        UUID.randomUUID().toString(),
                        HttpStatus.NO_CONTENT
                ),
                // Already deleted telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.NO_CONTENT
                ),
                // Unauthorized deletion (invalid token)
                Arguments.of(
                        "invalid-token",
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.UNAUTHORIZED
                ),
                // Missing required authorization scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(),
                                                        100),
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.FORBIDDEN
                )
        );
    }

    @ParameterizedTest
    @MethodSource("deleteTelemetryArgs")
    void shouldDeleteTelemetry(
            Object tokenSupplier,
            String ncaId,
            UUID telemetryId,
            HttpStatus expectedStatus) {

        testTelemetryService.createTelemetry(ncaId,
                telemetryId, TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS), TEST_TELEMETRY_LOG_SECRETS);

        var token = getToken(tokenSupplier);
        var deleteUrl = URI.create("/v2/nvcf/accounts/" + ncaId + "/telemetries/" + telemetryId);
        var deleteRequestEntity = RequestEntity.delete(deleteUrl)
                .header("Authorization", "Bearer " + token)
                .build();

        var response = testRestTemplate.exchange(deleteRequestEntity, Void.class);
        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> telemetryLookupService
                        .lookupByAccountAndTelemetryIdOrThrow(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID));

        var telemetrySecret = essService.getTelemetrySecret(TEST_NCA_ID, telemetryId);
        assertThat(telemetrySecret).isEmpty();
    }

    Stream<Arguments> deleteTelemetryWithMultipleTelemetryArgs() {
        return Stream.of(
                // Valid ID - Successful deletion
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.NO_CONTENT
                ),
                // Non-existent ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        UUID.randomUUID().toString(),
                        HttpStatus.NOT_FOUND
                ),
                // Already deleted telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.NO_CONTENT
                ),
                // Unauthorized deletion (invalid token)
                Arguments.of(
                        "invalid-token",
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.UNAUTHORIZED
                ),
                // Missing required authorization scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(),
                                                        100),
                        TEST_NCA_ID,
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.FORBIDDEN
                )
        );
    }

    @ParameterizedTest
    @MethodSource("deleteTelemetryWithMultipleTelemetryArgs")
    void shouldDeleteSecretWithMultipleTelemetry(
            String tokenSupplier,
            String ncaId,
            UUID telemetryId,
            HttpStatus expectedStatus) {

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS
        );

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_METRICS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.METRICS),
                TEST_TELEMETRY_METRICS_SECRETS
        );

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_TRACES_ID, TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.TRACES),
                TEST_TELEMETRY_TRACES_SECRETS
        );

        var token = getToken(tokenSupplier);
        var deleteUrl = URI.create("/v2/nvcf/accounts/" + ncaId + "/telemetries/" + telemetryId);
        var deleteRequestEntity = RequestEntity.delete(deleteUrl)
                .header("Authorization", "Bearer " + token)
                .build();

        var response = testRestTemplate.exchange(deleteRequestEntity, Void.class);
        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> telemetryLookupService
                        .lookupByAccountAndTelemetryIdOrThrow(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID));

        var telemetrySecret = essService.getTelemetrySecret(TEST_NCA_ID, telemetryId);
        assertThat(telemetrySecret).isEmpty();

    }

    @Test
    void shouldDeleteAllTelemetry() {
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS
        );

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_METRICS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.METRICS),
                TEST_TELEMETRY_METRICS_SECRETS
        );

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_TRACES_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.TRACES),
                TEST_TELEMETRY_TRACES_SECRETS
        );

        telemetryService.deleteAllTelemetries(TEST_NCA_ID);

        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> telemetryLookupService
                        .lookupByAccountAndTelemetryIdOrThrow(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID));
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> telemetryLookupService
                        .lookupByAccountAndTelemetryIdOrThrow(TEST_NCA_ID, TEST_TELEMETRY_METRICS_ID));
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> telemetryLookupService
                        .lookupByAccountAndTelemetryIdOrThrow(TEST_NCA_ID, TEST_TELEMETRY_TRACES_ID));

        var telemetryLogSecrets = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID);
        assertThat(telemetryLogSecrets).isEmpty();
        var telemetryMetricsSecrets = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_METRICS_ID);
        assertThat(telemetryMetricsSecrets).isEmpty();
        var telemetryTracesSecrets = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_TRACES_ID);
        assertThat(telemetryTracesSecrets).isEmpty();

    }
}
