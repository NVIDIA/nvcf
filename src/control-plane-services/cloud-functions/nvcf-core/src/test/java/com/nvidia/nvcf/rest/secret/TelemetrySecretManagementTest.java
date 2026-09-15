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
package com.nvidia.nvcf.rest.secret;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_SECRET_NAME_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_SECRET_VALUE_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_UPDATE_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOG_SECRETS;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.secret.dto.UpdateTelemetrySecretRequest;
import com.nvidia.nvcf.rest.telemetry.TestTelemetryService;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Set;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
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
import tools.jackson.databind.node.StringNode;

@Slf4j
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class TelemetrySecretManagementTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private EssService essService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Autowired
    private TestCommonService testCommonService;

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
        testAccountService.createDefaultAccountsClientsAndRegistries();

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS
        );
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockEssServer.stop();
        MockApiKeysServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        testTelemetryService.deleteAllTelemetries();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockEssServer.clearSecrets();
        resetToDefault();
    }

    Stream<Arguments> updateTelemetrySecretArgs() {
        var jwtCases = Stream.of(
                // Valid secret update with correct scope and token
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode("grafana-secret"))
                                     .build(),
                             HttpStatus.NO_CONTENT),
                // Null secret should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             null,
                             HttpStatus.BAD_REQUEST),
                // Empty secret set should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             null,
                             HttpStatus.BAD_REQUEST),
                // Empty secret name should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             SecretDto.builder()
                                     .name("")
                                     .value(new StringNode("empty-name-secret")).build(),
                             HttpStatus.BAD_REQUEST),
                // Secret name exceeding max allowed length should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             SecretDto.builder()
                                     .name(StringUtils.repeat("x", MAX_SECRET_NAME_LENGTH + 1))
                                     .value(new StringNode("valid-secret")).build(),
                             HttpStatus.BAD_REQUEST),
                // Secret name containing invalid characters should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             SecretDto.builder()
                                     .name("invalid*secret#name")
                                     .value(new StringNode("invalid-secret")).build(),
                             HttpStatus.BAD_REQUEST),
                // Maximum allowed secret name length should be valid
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             SecretDto.builder()
                                     .name(StringUtils.repeat("x", MAX_SECRET_NAME_LENGTH))
                                     .value(new StringNode("valid-secret"))
                                     .build(),
                             HttpStatus.NO_CONTENT),
                // Empty secret value should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode(""))
                                     .build(),
                             HttpStatus.BAD_REQUEST),
                // Secret value exceeding max length should return BAD_REQUEST
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), 100),
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode(
                                             StringUtils.repeat("value1", MAX_SECRET_VALUE_LENGTH)))
                                     .build(),
                             HttpStatus.BAD_REQUEST),
                // Token missing `update_secrets` scope should return FORBIDDEN
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(), 100),
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode("valid-secret"))
                                     .build(),
                             HttpStatus.FORBIDDEN),
                // Expired token should return UNAUTHORIZED
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_UPDATE_SECRETS), -100),
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode("valid-secret"))
                                     .build(),
                             HttpStatus.UNAUTHORIZED),
                // Missing authentication token should return UNAUTHORIZED
                Arguments.of(null,
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode("grafana-secret"))
                                     .build(),
                             HttpStatus.UNAUTHORIZED)
        );

        var apiKeyCases = Stream.of(
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_UPDATE_SECRETS));
                                 return "nvapi-stg-some-key";
                             },
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode("api-key-secret"))
                                     .build(),
                             HttpStatus.NO_CONTENT),
                // apikey with missing scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode("api-key-secret"))
                                     .build(),
                             HttpStatus.FORBIDDEN),
                // apikey with incorrect scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of("list_functions"));
                                 return "nvapi-stg-some-key";
                             },
                             SecretDto.builder()
                                     .name("grafana.credentials")
                                     .value(new StringNode("api-key-secret"))
                                     .build(),
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("updateTelemetrySecretArgs")
    void shouldUpdateTelemetrySecret(
            Object tokenSupplier,
            SecretDto secret,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        var requestBody = UpdateTelemetrySecretRequest.builder().secret(secret).build();
        var requestEntity = RequestEntity.put(URI.create("/v2/nvcf/secrets/telemetries/" + TEST_TELEMETRY_LOGS_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var response = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);
    }

    @Test
    void shouldUpdateTelemetrySecretAndValidate() {
        var secretValue = "new-latest-updated-secret";
        var secretKey = "telemetry-log-secret-name";
        var secret = SecretDto.builder().name(secretKey).value(new StringNode(secretValue)).build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_UPDATE_SECRETS), 100);
        var requestBody = UpdateTelemetrySecretRequest.builder().secret(secret).build();
        var requestEntity = RequestEntity.put(URI.create("/v2/nvcf/secrets/telemetries/" + TEST_TELEMETRY_LOGS_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var response = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);
        var telemetrySecret = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID);

        assertThat(telemetrySecret).isPresent();
        telemetrySecret.ifPresent(secretDto -> {
            assertThat(secretDto.name()).isEqualTo(secretKey);
            assertThat(secretDto.value().asString()).isEqualTo(secretValue);
        });
    }
}
