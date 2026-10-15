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
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_UPDATE_SECRETS;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_SECRET_NAME_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_SECRET_VALUE_LENGTH;
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
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Set;
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
import tools.jackson.databind.node.StringNode;

@Slf4j
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class XAccountTelemetrySecretManagementTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

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
        return Stream.of(
                // Valid secret
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana.credentials")
                                        .value(new StringNode("grafana-secret"))
                                        .build(),
                        HttpStatus.NO_CONTENT
                ),
                // Secret with leading or trailing spaces
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name(" grafana.credentials ")
                                        .value(new StringNode(" grafana-secret "))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Case-sensitive secret name
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("Grafana.Credentials")
                                        .value(new StringNode("secret-case"))
                                        .build(),
                        HttpStatus.NO_CONTENT
                ),
                // Invalid secret name with unsupported special characters
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("invalid*secret#name")
                                        .value(new StringNode("invalid-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Empty secret name
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("")
                                        .value(new StringNode("empty-name-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secrets with empty values
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana.credentials")
                                        .value(new StringNode(""))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secrets with malformed JSON value
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("malformed.secret")
                                        .value(new StringNode("{invalid-json}"))
                                        .build(),
                        HttpStatus.NO_CONTENT
                ),
                // Secret value exceeding MAX_SECRET_VALUE_LENGTH characters
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("long.secret")
                                        .value(new StringNode("x".repeat(MAX_SECRET_VALUE_LENGTH + 1)))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Null secret payload
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        null,
                        HttpStatus.BAD_REQUEST
                ),
                // Empty secret set
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        null,
                        HttpStatus.BAD_REQUEST
                ),
                // Invalid JWT token
                Arguments.of(
                        "invalid-token",
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana.credentials")
                                        .value(new StringNode("valid-secret"))
                                        .build(),
                        HttpStatus.UNAUTHORIZED
                ),
                // Missing authorization header
                Arguments.of(
                        null,
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana.credentials")
                                        .value(new StringNode("valid-secret"))
                                        .build(),
                        HttpStatus.UNAUTHORIZED
                ),
                // Token missing `update_secrets` scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana.credentials")
                                        .value(new StringNode("valid-secret"))
                                        .build(),
                        HttpStatus.FORBIDDEN
                ),
                // Expired token
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        -100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana.credentials")
                                        .value(new StringNode("valid-secret"))
                                        .build(),
                        HttpStatus.UNAUTHORIZED
                ),
                // Token with correct scope but for a non-existent NCA ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        "non-existent-nca-id",
                        SecretDto.builder()
                                        .name("grafana.credentials")
                                        .value(new StringNode("valid-secret"))
                                        .build(),
                        HttpStatus.NOT_FOUND
                ),
                // Secret with only whitespace as a name
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("   ") // Only spaces
                                        .value(new StringNode("whitespace-name-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secret with non-printable characters in the name
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("\u0000\u0008\t") // Null, backspace, tab
                                        .value(new StringNode("non-printable-name-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secret with newline characters in the name
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana\ncredentials") // Newline in name
                                        .value(new StringNode("newline-name-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secret with tab characters in the name
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana\tcredentials") // Tab in name
                                        .value(new StringNode("tab-name-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secret name starts with a special character
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name(".grafana.credentials") // Starts with "."
                                        .value(new StringNode("special-start-name-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secret name with emoji characters
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana🔑credentials")
                                        .value(new StringNode("emoji-name-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secret name with SQL injection attempt
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("grafana'; DROP TABLE secret; --")
                                        .value(new StringNode("sql-injection-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                ),
                // Secret name exceeding max length but value is valid
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                        100),
                        TEST_NCA_ID,
                        SecretDto.builder()
                                        .name("x".repeat(MAX_SECRET_NAME_LENGTH + 1))
                                        .value(new StringNode("valid-secret"))
                                        .build(),
                        HttpStatus.BAD_REQUEST
                )
        );
    }

    @ParameterizedTest
    @MethodSource("updateTelemetrySecretArgs")
    void shouldUpdateTelemetrySecret(
            Object tokenSupplier,
            String ncaId,
            SecretDto secret,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);

        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS
        );
        var requestBody = UpdateTelemetrySecretRequest.builder().secret(secret).build();
        var requestEntity = RequestEntity.put(URI.create("/v2/nvcf/accounts/" + ncaId + "/secrets/telemetries/" + TEST_TELEMETRY_LOGS_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var response = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(response.getStatusCode()).isEqualTo(expectedStatus);
    }

   @Test
    void shouldUpdateTelemetrySecretAndValidate() {
        var secretValue = "new-latest-updated-secret";
        var secretKey =  "telemetry-log-secret-name";
        var secret  =
               SecretDto.builder()
                       .name(secretKey)
                       .value(new StringNode(secretValue))
                       .build();

       var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                   List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                   100);
        var requestBody = UpdateTelemetrySecretRequest.builder().secret(secret).build();
        var requestEntity = RequestEntity.put(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/secrets/telemetries/" + TEST_TELEMETRY_LOGS_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var response = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);
        var telemetrySecret = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID);

       assertThat(telemetrySecret).isPresent().hasValueSatisfying(secretDto -> {
           assertThat(secretDto.name()).isEqualTo(secretKey);
           assertThat(secretDto.value().asString()).isEqualTo(secretValue);
       });
    }

    @Test
    void shouldUpdateTelemetrySecretOnlyOneSecret() {
        var secretValue = "new-latest-updated-secret";
        var secretKey = "telemetry-log-secret-name";
        var secret = SecretDto.builder()
                .name(secretKey)
                .value(new StringNode(secretValue))
                .build();

        var actualSecretOptional = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID);
        Map<String, String> actualSecrets = actualSecretOptional
                .map(secretDto -> Map.of(secretDto.name(), secretDto.value().asString()))
                .orElse(Collections.emptyMap());

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_UPDATE_SECRETS),
                                                    100);
        var requestBody = UpdateTelemetrySecretRequest.builder().secret(secret).build();
        var requestEntity = RequestEntity.put(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/secrets/telemetries/" + TEST_TELEMETRY_LOGS_ID))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var response = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        var updatedSecretOptional = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID);
        assertThat(updatedSecretOptional).isPresent();

        var updatedSecrets = updatedSecretOptional
                .map(secretDto -> Map.of(secretDto.name(), secretDto.value().asString()))
                .orElse(Collections.emptyMap());

        assertThat(updatedSecrets)
                .containsEntry(secretKey, secretValue);

        actualSecrets.forEach((key, value) -> {
            if (!key.equals(secretKey)) {
                assertThat(updatedSecrets)
                        .containsEntry(key, value);
            }
        });
    }
}

