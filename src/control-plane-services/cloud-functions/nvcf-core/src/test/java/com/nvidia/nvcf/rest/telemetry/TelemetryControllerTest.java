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
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_MANAGE_REGISTRY_CREDENTIALS;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_MANAGE_TELEMETRIES;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOG_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;
import static org.assertj.core.api.AssertionsForClassTypes.assertThatThrownBy;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
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
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult;
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
import java.util.function.Supplier;
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
class TelemetryControllerTest {
    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Autowired
    private TelemetryLookupService telemetryLookupService;

    @Autowired
    private TelemetryService telemetryService;

    @Autowired
    private EssService essService;

    @Autowired
    private TestCommonService testCommonService;

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
        testTelemetryService.deleteAllTelemetries();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testTelemetryService.deleteAllTelemetries();
        resetToDefault();
    }

    @Test
    void shouldFailWhenEndpointIsMissing() {
        assertThatThrownBy(() -> TelemetryRequest.builder()
                // No .endpoint(...) call
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.PROMETHEUS)
                .types(Set.of(TelemetryTypeEnum.LOGS))
                .secret(SecretDto.builder().name("telemetry-secret").build())
                .build()
        ).isInstanceOf(NullPointerException.class);
    }

    @Test
    void shouldFailWhenProtocolIsMissing() {
        assertThatThrownBy(() -> TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                // No .protocol(...) call
                .provider(TelemetryProviderEnum.PROMETHEUS)
                .types(Set.of(TelemetryTypeEnum.LOGS))
                .secret(SecretDto.builder().name("telemetry-secret").build())
                .build()
        ).isInstanceOf(NullPointerException.class);
    }

    @Test
    void shouldFailWhenProviderIsMissing()  {
        assertThatThrownBy(() -> TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                // No .provider(...) call
                .types(Set.of(TelemetryTypeEnum.LOGS))
                .secret(SecretDto.builder().name("telemetry-secret").build())
                .build()
        ).isInstanceOf(NullPointerException.class);
    }

    @Test
    void shouldFailWhenTypesAreMissing() {
        assertThatThrownBy(() -> TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.PROMETHEUS)
                // No .types(...) call
                .secret(SecretDto.builder().name("telemetry-secret").build())
                .build()
        ).isInstanceOf(NullPointerException.class);
    }

    Stream<Arguments> shouldFailWhenTypesAreInvalidArgs() {
        return Stream.of(
            // Empty types field
            Arguments.of(
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
            // Missing types field
            Arguments.of(
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
            // Null types field
            Arguments.of(
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
    void shouldFailWhenTypesAreInvalid(String rawJsonInput, HttpStatus expectedStatus) {
        String token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                       List.of(SCOPE_MANAGE_TELEMETRIES), 100);

        var url = URI.create("/v2/nvcf/telemetries");
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
        var jwtCases = Stream.of(
                // Invalud JWT Scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of("read_only"), 100),
                        // Wrong scope
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder().name("telemetry-secret").value(secretJsonNodeValue).build())
                                .build(),
                        HttpStatus.FORBIDDEN
                ),
                // Missing JWT Token
                Arguments.of(
                        null, // No JWT token
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder().name("telemetry-secret").value(secretJsonNodeValue).build())
                                .build(),
                        HttpStatus.UNAUTHORIZED
                ),
                // Missing Authorization header
                Arguments.of(
                        null, // No token
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
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(), 100),
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
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
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
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
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
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
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
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Success endpoint
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
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
                ),
                // Secret as String
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES), 100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.GRAFANA_CLOUD)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("plaintext-secret")
                                        .value(jsonMapper.createObjectNode().put("secret", "plaintext_value"))
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                // Secrets with username / password
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES), 100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.GRAFANA_CLOUD)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("grafana-secret")
                                        .value(jsonMapper.createObjectNode()
                                                .put("username", "grafana-user")
                                                .put("password", "grafana-password"))
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                // Secrets with Certificates
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        telemetryRequest,
                        HttpStatus.OK
                )
        );

        var apiKeyCases = Stream.of(
                // Authorized to create telemetry.
                Arguments.of(
                        (Supplier<String>) () -> {
                            setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                        List.of(new ApiKeyValidationResult.Resource(
                                                "account-functions", "*")),
                                        List.of(SCOPE_MANAGE_TELEMETRIES));
                            return "nvapi-stg-some-key";
                        },
                        telemetryRequest, HttpStatus.OK),
                // Authorized to create telemetry with function resource
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(
                                                     new ApiKeyValidationResult.Resource(
                                                             "function",
                                                             TEST_FUNCTION_ID + "/" +
                                                                     TEST_VERSION_ID_1)),
                                             List.of(SCOPE_MANAGE_TELEMETRIES));
                                 return "nvapi-stg-some-key";
                             },
                             telemetryRequest,
                             HttpStatus.OK),
                // Authorized to create telemetry having resources for single function
                // TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new ApiKeyValidationResult.Resource(
                                                     "function", TEST_FUNCTION_ID + "/" + "*")),
                                             List.of(SCOPE_MANAGE_TELEMETRIES));
                                 return "nvapi-stg-some-key";
                             },
                             telemetryRequest,
                             HttpStatus.OK),
                // Authorized to create telemetry without resource policy
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_TELEMETRIES));
                                 return "nvapi-stg-some-key";
                             },
                             telemetryRequest,
                             HttpStatus.OK),
                // Attempt to create telemetry without scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             telemetryRequest,
                             HttpStatus.FORBIDDEN),
                // Attempt to create telemetry with wrong scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             telemetryRequest,
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("createTelemetryArgs")
    void shouldCreateTelemetry(
            Object tokenSupplier,
            TelemetryRequest telemetryRequest,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        var url = URI.create("/v2/nvcf/telemetries");
        var updateRequestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(telemetryRequest);

        var responseEntity = testRestTemplate.exchange(updateRequestEntity, TelemetryResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var telemetryId = responseBody.telemetry().telemetryId();
        assertThat(telemetryId).isNotNull();

        var getTelemetryUrl = URI.create("/v2/nvcf/telemetries/" + telemetryId);
        var getTelemetryRequestEntity = RequestEntity.get(getTelemetryUrl)
                .header("Authorization", "Bearer " + token)
                .build();

        var getTelemetryResponse = testRestTemplate.exchange(
                getTelemetryRequestEntity,
                TelemetryResponse.class);
        assertThat(getTelemetryResponse.getStatusCode()).isEqualTo(HttpStatus.OK);

        var getResponseBody = getTelemetryResponse.getBody();
        assertThat(getResponseBody).isNotNull();
        var retrievedTelemetry = getResponseBody.telemetry();

        assertThat(retrievedTelemetry.endpoint()).isEqualTo(telemetryRequest.endpoint());
        assertThat(retrievedTelemetry.protocol()).isEqualTo(telemetryRequest.protocol());
        assertThat(retrievedTelemetry.provider()).isEqualTo(telemetryRequest.provider());
        assertThat(retrievedTelemetry.types()).isEqualTo(telemetryRequest.types());
        assertThat(retrievedTelemetry.name()).isEqualTo(telemetryRequest.secret().name());

        var telemetrySecret = essService.getTelemetrySecret(TEST_NCA_ID, telemetryId);

        assertThat(telemetrySecret).isPresent();
        assertThat(telemetrySecret).isNotNull();
        telemetrySecret.ifPresent(secretDto -> {
            assertThat(secretDto.name()).isEqualTo(telemetryRequest.secret().name());
            assertThat(secretDto.value()).isEqualTo(telemetryRequest.secret().value());
        });
    }

    Stream<Arguments> shouldNotAllowDuplicateTelemetryArgs() {
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("GRAFANA", "GRAFANA_PASSWORD");
        return Stream.of(
                // Duplicate secret case insensitive
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.PROMETHEUS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("Telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Duplicate secret case sensitive
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
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
                        HttpStatus.BAD_REQUEST
                ));
    }

    @ParameterizedTest
    @MethodSource("shouldNotAllowDuplicateTelemetryArgs")
    void shouldNotAllowDuplicateTelemetry(
            Object tokenSupplier,
            TelemetryRequest telemetryRequest,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        var url = URI.create("/v2/nvcf/telemetries");
        var requestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(telemetryRequest);

        var firstResponse = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);
        assertThat(firstResponse.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = firstResponse.getBody();
        assertThat(responseBody).isNotNull();
        var telemetryId = responseBody.telemetry().telemetryId();
        assertThat(telemetryId).isNotNull();

        // Second request - should be rejected if duplicate is not allowed
        var secondResponse = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);

        assertThat(secondResponse.getStatusCode()).isEqualTo(expectedStatus);

        var errorResponse = secondResponse.getBody();
        assertThat(errorResponse).isNotNull();

    }


    Stream<Arguments> createValidTelemetryArgs()  {
        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("GRAFANA", "GRAFANA_PASSWORD");
        return Stream.of(
                //Telemetry Provider Datadog
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
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
                ),
                //Telemetry Provider GRAFANA_CLOUD
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.GRAFANA_CLOUD)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry Provider SPLUNK
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.SPLUNK)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry provider SERVICENOW
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.SERVICENOW)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry provider KRATOS
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.KRATOS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry provider KRATOS_THANOS
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.KRATOS_THANOS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry provider TIMESTREAM
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.TIMESTREAM)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry provider VICTORIAMETRICS
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.VICTORIAMETRICS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry provider AZURE_MONITOR
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.AZURE_MONITOR)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry provider OTEL_COLLECTOR
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.OTEL_COLLECTOR)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry Protocol HTTPS
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.VICTORIAMETRICS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry Protocol GRPC
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.GRPC)
                                .provider(TelemetryProviderEnum.VICTORIAMETRICS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry Protocol TCP
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.VICTORIAMETRICS)
                                .types(Set.of(TelemetryTypeEnum.LOGS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry Type METRICS
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.VICTORIAMETRICS)
                                .types(Set.of(TelemetryTypeEnum.METRICS))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                //Telemetry Type
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.VICTORIAMETRICS)
                                .types(Set.of(TelemetryTypeEnum.TRACES))
                                .secret(SecretDto.builder()
                                        .name("telemetry-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                ),
                // Telemetry Type - Multiple values (LOGS, METRICS, TRACES)
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TelemetryRequest.builder()
                                .endpoint(TEST_TELEMETRY_ENDPOINT)
                                .protocol(TelemetryProtocolEnum.HTTP)
                                .provider(TelemetryProviderEnum.SPLUNK)
                                .types(Set.of(TelemetryTypeEnum.LOGS,
                                        TelemetryTypeEnum.METRICS,
                                        TelemetryTypeEnum.TRACES))
                                .secret(SecretDto.builder()
                                        .name("multi-type-secret")
                                        .value(secretJsonNodeValue)
                                        .build())
                                .build(),
                        HttpStatus.OK
                )
        );
    }

    @ParameterizedTest
    @MethodSource("createValidTelemetryArgs")
    void shouldCreateTelemetryWithDifferentProvider(
            Object tokenSupplier,
            TelemetryRequest telemetryRequest,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        var url = URI.create("/v2/nvcf/telemetries");
        var requestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(telemetryRequest);

        var response = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);

        if (!expectedStatus.isError()) {
            assertThat(response.getBody()).isNotNull();
        }
    }

    Stream<Arguments> getTelemetryDetailsArgs() {
        var jwtCases = Stream.of(
                // Missing authorization token
                Arguments.of(
                        null, // No token
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.UNAUTHORIZED
                ),
                // Expired token
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        -100), // Expired token
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.UNAUTHORIZED
                ),
                // Token without required scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(), // No scopes
                                                        100),
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.FORBIDDEN
                ),
                // Valid ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_TELEMETRY_LOGS_ID, // Prepopulated valid ID
                        HttpStatus.OK
                ),
                // Non-existent ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        UUID.randomUUID(),
                        HttpStatus.NOT_FOUND
                ),
                // Randomly generated UUID (should return NOT FOUND)
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        UUID.randomUUID().toString(),
                        HttpStatus.NOT_FOUND
                )
        );

        var apiKeyCases = Stream.of(
                // Authorized to get telemetry.
                Arguments.of(
                        (Supplier<String>) () -> {
                            setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                        List.of(new ApiKeyValidationResult.Resource(
                                                "account-functions", "*")),
                                        List.of(SCOPE_MANAGE_TELEMETRIES));
                            return "nvapi-stg-some-key";
                        }, TEST_TELEMETRY_LOGS_ID, HttpStatus.OK),
                // Authorized to get telemetry with function resource
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(
                                        new ApiKeyValidationResult.Resource(
                                                "function",
                                                TEST_FUNCTION_ID + "/" +
                                                        TEST_VERSION_ID_1)),
                                List.of(SCOPE_MANAGE_TELEMETRIES));
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.OK),
                // Authorized to get telemetry having resources for single function
                // TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new ApiKeyValidationResult.Resource("function",
                                                                     TEST_FUNCTION_ID + "/" + "*")),
                                List.of(SCOPE_MANAGE_TELEMETRIES));
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.OK),
                // Authorized to get telemetry without resource policy
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(),
                                List.of(SCOPE_MANAGE_TELEMETRIES));
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.OK),
                // Attempt to get telemetry without scope
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(),
                                List.of());
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.FORBIDDEN),
                // Attempt to get telemetry with wrong scope
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(),
                                List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("getTelemetryDetailsArgs")
    void shouldGetTelemetryDetails(
            Object tokenSupplier,
            UUID telemetryId,
            HttpStatus expectedStatus) {
        testTelemetryService.createTelemetry(TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID, TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS), TEST_TELEMETRY_LOG_SECRETS);

        var url = URI.create("/v2/nvcf/telemetries/" + telemetryId.toString());
        var token = getToken(tokenSupplier);
        var requestEntity = RequestEntity.get(url)
                .header("Authorization", "Bearer " + token)
                .build();
        var response = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = response.getBody();
        assertThat(responseBody).isNotNull();
        var telemetry = responseBody.telemetry();
        assertThat(telemetry.endpoint()).isNotEmpty();
        assertThat(telemetry.protocol()).isNotNull();
        assertThat(telemetry.provider()).isNotNull();
    }

    @Test
    void shouldFailGetTelemetryDetailsAcrossAccount() {
        var telemetryId = UUID.randomUUID();
        testTelemetryService.createTelemetry(
                TEST_NCA_ID_3,
                telemetryId,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS);

        var url = URI.create("/v2/nvcf/telemetries/" + telemetryId);
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_TELEMETRIES),
                                                    100);
        var requestEntity = RequestEntity.get(url)
                .header("Authorization", "Bearer " + token)
                .build();
        var response = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }



    Stream<Arguments> listTelemetriesArgs() {
        var jwtCases = Stream.of(
                // No entries created, expect empty list
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        0,
                        HttpStatus.OK,
                        0
                ),
                // 5 valid telemetry entries
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        5,
                        HttpStatus.OK,
                        5
                ),
                // Large number of entries (e.g., 50)
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        50,
                        HttpStatus.OK,
                        50
                ),
                // Invalid token case
                Arguments.of(
                        "invalid-token",
                        5,
                        HttpStatus.UNAUTHORIZED,
                        0
                ),
                // No authorization scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(),
                                                        100),
                        5,
                        HttpStatus.FORBIDDEN,
                        0
                ),
                // Expired token
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        -100), // Expired token
                        5,
                        HttpStatus.UNAUTHORIZED,
                        0
                )
        );

        var apiKeyCases = Stream.of(
                // Authorized to list telemetry.
                Arguments.of(
                        (Supplier<String>) () -> {
                            setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                        List.of(new ApiKeyValidationResult.Resource(
                                                "account-functions", "*")),
                                        List.of(SCOPE_MANAGE_TELEMETRIES));
                            return "nvapi-stg-some-key";
                        }, 5, HttpStatus.OK, 5),
                // Authorized to list telemetry with function resource
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(
                                                     new ApiKeyValidationResult.Resource(
                                                             "function",
                                                             TEST_FUNCTION_ID + "/" +
                                                                     TEST_VERSION_ID_1)),
                                             List.of(SCOPE_MANAGE_TELEMETRIES));
                                 return "nvapi-stg-some-key";
                             }, 5, HttpStatus.OK, 5),
                // Authorized to list telemetry having resources for single function
                // TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new ApiKeyValidationResult.Resource(
                                                     "function", TEST_FUNCTION_ID + "/" + "*")),
                                             List.of(SCOPE_MANAGE_TELEMETRIES));
                                 return "nvapi-stg-some-key";
                             }, 5, HttpStatus.OK, 5),
                // Authorized to list telemetry without resource policy
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_TELEMETRIES));
                                 return "nvapi-stg-some-key";
                             }, 5, HttpStatus.OK, 5),
                // Attempt to list telemetry without scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             }, 5, HttpStatus.FORBIDDEN, 5),
                // Attempt to create telemetry with wrong scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             }, 5, HttpStatus.FORBIDDEN, 5)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("listTelemetriesArgs")
    void shouldListTelemetries(
            Object tokenSupplier,
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
        var getAllUrl = URI.create("/v2/nvcf/telemetries");
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

    @Test
    void shouldFailListTelemetryDetailsAcrossAccount() {
        // Create telemetry in TEST_NCA_ID account.
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID, TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS), TEST_TELEMETRY_LOG_SECRETS
        );

        // Create a JWT using TEST_CLIENT_SUBJECT_2 which is tied with TEST_NCA_ID_2 account
        // to get details of Telemetry in TEST_NCA_ID account.
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT_2,
                                                    List.of(SCOPE_MANAGE_TELEMETRIES),
                                                    100);
        var url = URI.create("/v2/nvcf/telemetries/" + TEST_TELEMETRY_LOGS_ID);
        var requestEntity = RequestEntity.get(url)
                .header("Authorization", "Bearer " + token)
                .build();
        var response = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    Stream<Arguments> deleteTelemetryArgs() {
        var jwtCases = Stream.of(
                // Valid ID - Successful deletion
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.NO_CONTENT
                ),
                // Non-existent ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        UUID.randomUUID(),
                        HttpStatus.NOT_FOUND
                ),
                // Already deleted telemetry (deleting twice should return NOT FOUND)
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        100),
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.NO_CONTENT
                ),
                // Unauthorized deletion (invalid token)
                Arguments.of(
                        "invalid-token",
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.UNAUTHORIZED
                ),
                // Missing required authorization scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(), // No scopes
                                                        100),
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.FORBIDDEN
                ),
                // Expired token case
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_MANAGE_TELEMETRIES),
                                                        -100), // Expired token
                        TEST_TELEMETRY_LOGS_ID,
                        HttpStatus.UNAUTHORIZED
                )
        );

        var apiKeyCases = Stream.of(
                // Authorized to delete telemetry.
                Arguments.of(
                        (Supplier<String>) () -> {
                            setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                        List.of(new ApiKeyValidationResult.Resource(
                                                "account-functions", "*")),
                                        List.of(SCOPE_MANAGE_TELEMETRIES));
                            return "nvapi-stg-some-key";
                        }, TEST_TELEMETRY_LOGS_ID, HttpStatus.NO_CONTENT),
                // Authorized to delete telemetry with function resource
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(
                                        new ApiKeyValidationResult.Resource(
                                                "function",
                                                TEST_FUNCTION_ID + "/" +
                                                        TEST_VERSION_ID_1)),
                                List.of(SCOPE_MANAGE_TELEMETRIES));
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.NO_CONTENT),
                // Authorized to delete telemetry having resources for single function
                // TEST_NCA_ID account.
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(new ApiKeyValidationResult.Resource("function",
                                                                     TEST_FUNCTION_ID + "/" + "*")),
                                List.of(SCOPE_MANAGE_TELEMETRIES));
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.NO_CONTENT),
                // Authorized to delete telemetry without resource policy
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(),
                                List.of(SCOPE_MANAGE_TELEMETRIES));
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.NO_CONTENT),
                // Attempt to delete telemetry without scope
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(),
                                List.of());
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.FORBIDDEN),
                // Attempt to delete telemetry with wrong scope
                Arguments.of((Supplier<String>) () -> {
                    setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                List.of(),
                                List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                    return "nvapi-stg-some-key";
                }, TEST_TELEMETRY_LOGS_ID, HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("deleteTelemetryArgs")
    void shouldDeleteTelemetry(
            Object tokenSupplier,
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
        var deleteUrl = URI.create("/v2/nvcf/telemetries/" + telemetryId);
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
        var telemetryTraceSecrets = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_TRACES_ID);
        assertThat(telemetryTraceSecrets).isEmpty();
        var telemetryMetricsSecrets = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_METRICS_ID);
        assertThat(telemetryMetricsSecrets).isEmpty();
    }

    @Test
    void deleteTelemetryWithFunction() {
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

        testTelemetryService.createTestFunctionEntityForTelemetry(
                TEST_FUNCTION_ID,
                TEST_VERSION_ID_1,
                TEST_NCA_ID,
                TEST_FUNCTION_NAME,
                FunctionStatus.INACTIVE,
                null,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_METRICS_ID,
                TEST_TELEMETRY_TRACES_ID
        );

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_TELEMETRIES),
                                                    100);
        var deleteUrl = URI.create("/v2/nvcf/telemetries/" + TEST_TELEMETRY_LOGS_ID);
        var deleteRequestEntity = RequestEntity.delete(deleteUrl)
                .header("Authorization", "Bearer " + token)
                .build();
        var response = testRestTemplate.exchange(deleteRequestEntity, Void.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldFailDeleteTelemetryAcrossAccount() {
        var telemetryId = UUID.randomUUID();
        testTelemetryService.createTelemetry(
                TEST_NCA_ID_3,
                telemetryId,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS);

        var url = URI.create("/v2/nvcf/telemetries/" + telemetryId);
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_TELEMETRIES),
                                                    100);
        var requestEntity = RequestEntity.delete(url)
                .header("Authorization", "Bearer " + token)
                .build();
        var response = testRestTemplate.exchange(requestEntity, TelemetryResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    @Test
    void shouldEnforceTelemetryLimitForAccount() {
        // Set account's max telemetries allowed to 2
        testAccountService.updateAccountMaxTelemetries(TEST_NCA_ID, 2);

        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("GRAFANA", "GRAFANA_PASSWORD");
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_TELEMETRIES),
                                                    100);

        // Create first telemetry - should succeed
        var firstTelemetryRequest = TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.PROMETHEUS)
                .types(Set.of(TelemetryTypeEnum.LOGS))
                .secret(SecretDto.builder()
                                .name("first-telemetry-secret")
                                .value(secretJsonNodeValue)
                                .build())
                .build();

        var url = URI.create("/v2/nvcf/telemetries");
        var firstRequestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(firstTelemetryRequest);

        var firstResponse = testRestTemplate.exchange(firstRequestEntity, TelemetryResponse.class);
        assertThat(firstResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(firstResponse.getBody()).isNotNull();

        // Create second telemetry - should succeed
        var secondTelemetryRequest = TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.GRAFANA_CLOUD)
                .types(Set.of(TelemetryTypeEnum.METRICS))
                .secret(SecretDto.builder()
                                .name("second-telemetry-secret")
                                .value(secretJsonNodeValue)
                                .build())
                .build();

        var secondRequestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(secondTelemetryRequest);

        var secondResponse = testRestTemplate.exchange(secondRequestEntity,
                                                       TelemetryResponse.class);
        assertThat(secondResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(secondResponse.getBody()).isNotNull();

        // Try to create third telemetry - should fail due to limit
        var thirdTelemetryRequest = TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.SPLUNK)
                .types(Set.of(TelemetryTypeEnum.TRACES))
                .secret(SecretDto.builder()
                                .name("third-telemetry-secret")
                                .value(secretJsonNodeValue)
                                .build())
                .build();

        var thirdRequestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(thirdTelemetryRequest);

        var thirdResponse = testRestTemplate.exchange(thirdRequestEntity, TelemetryResponse.class);
        assertThat(thirdResponse.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldAllowTelemetryCreationAfterDeletion() {
        // Set account's max telemetries allowed to 2
        testAccountService.updateAccountMaxTelemetries(TEST_NCA_ID, 2);

        var secretJsonNodeValue = jsonMapper.createObjectNode()
                .put("PROMETHEUS", "PROMETHEUS_PASSWORD");
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_TELEMETRIES),
                                                    100);

        var url = URI.create("/v2/nvcf/telemetries");

        testTelemetryService.createTelemetry(TEST_NCA_ID,
                                             TEST_TELEMETRY_LOGS_ID,
                                             TEST_TELEMETRY_ENDPOINT,
                                             TelemetryProtocol.HTTP,
                                             TelemetryProvider.DATADOG,
                                             Set.of(TelemetryType.LOGS),
                                             SecretDto.builder()
                                                     .name("first-telemetry-secret")
                                                     .value(secretJsonNodeValue)
                                                     .build());

        testTelemetryService.createTelemetry(TEST_NCA_ID,
                                             TEST_TELEMETRY_METRICS_ID,
                                             TEST_TELEMETRY_ENDPOINT,
                                             TelemetryProtocol.HTTP,
                                             TelemetryProvider.GRAFANA_CLOUD,
                                             Set.of(TelemetryType.METRICS),
                                             SecretDto.builder()
                                                     .name("second-telemetry-secret")
                                                     .value(secretJsonNodeValue)
                                                     .build());

        // Try to create third telemetry - should fail due to limit
        var thirdTelemetryRequest = TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.SPLUNK)
                .types(Set.of(TelemetryTypeEnum.LOGS))
                .secret(SecretDto.builder()
                                .name("third-telemetry-secret")
                                .value(secretJsonNodeValue)
                                .build())
                .build();

        var thirdRequestEntity = RequestEntity.post(url)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(thirdTelemetryRequest);

        var thirdResponse = testRestTemplate.exchange(thirdRequestEntity, TelemetryResponse.class);
        assertThat(thirdResponse.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        // Delete first telemetry
        var deleteUrl = URI.create("/v2/nvcf/telemetries/" + TEST_TELEMETRY_LOGS_ID);
        var deleteRequestEntity = RequestEntity.delete(deleteUrl)
                .header("Authorization", "Bearer " + token)
                .build();

        var deleteResponse = testRestTemplate.exchange(deleteRequestEntity, Void.class);
        assertThat(deleteResponse.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        // Now try to create third telemetry again - should succeed after deletion
        var thirdRetryResponse = testRestTemplate.exchange(thirdRequestEntity,
                                                           TelemetryResponse.class);
        assertThat(thirdRetryResponse.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(thirdRetryResponse.getBody()).isNotNull();
    }
}
