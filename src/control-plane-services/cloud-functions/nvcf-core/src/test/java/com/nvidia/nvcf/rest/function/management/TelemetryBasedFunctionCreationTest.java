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

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.function.management.dto.ApiBodyFormatEnum.PREDICT_V2;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_DTO;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_DTOS;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOG_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_SECRETS;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.telemetry.TestTelemetryService;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetriesDto;
import com.nvidia.nvcf.service.common.TestCommonService;
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

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class TelemetryBasedFunctionCreationTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    protected JsonMapper jsonMapper;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

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
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_LOGS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS
        );
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_METRICS_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.METRICS),
                TEST_TELEMETRY_METRICS_SECRETS
        );
        testTelemetryService.createTelemetry(
                TEST_NCA_ID,
                TEST_TELEMETRY_TRACES_ID,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.TRACES),
                TEST_TELEMETRY_TRACES_SECRETS
        );
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();

        testTelemetryService.deleteAllTelemetries();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        resetToDefault();
    }

    Stream<Arguments> createFunctionWithTelemetryArgs() {
        return Stream.of(
                // Valid function creation with full telemetry set
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           TEST_TELEMETRY_METRICS_ID,
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.OK
                ),
                // Function creation with only logs telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           null,
                                           null),
                        HttpStatus.OK
                ),
                // Function creation with only metrics telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(null,
                                           TEST_TELEMETRY_METRICS_ID,
                                           null),
                        HttpStatus.OK
                ),
                // Function creation with only traces telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(null,
                                           null,
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.OK
                ),
                // Function creation with logs and metrics telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           TEST_TELEMETRY_METRICS_ID,
                                           null),
                        HttpStatus.OK
                ),
                // Function creation with logs and traces telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           null,
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.OK
                ),
                // Function creation with metrics and traces telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(null,
                                           TEST_TELEMETRY_METRICS_ID,
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.OK
                ),
                // Metrics and Logs swapped
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        Set.of("misaligned-logs-metrics-test"),
                        new TelemetriesDto(TEST_TELEMETRY_METRICS_ID,
                                           TEST_TELEMETRY_LOGS_ID,
                                           null), // Swapped IDs
                        HttpStatus.BAD_REQUEST
                ),
                // Logs and Metrics swapped
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        Set.of("misaligned-logs-metrics-test"),
                        new TelemetriesDto(TEST_TELEMETRY_METRICS_ID,
                                           TEST_TELEMETRY_LOGS_ID,
                                           null),
                        HttpStatus.BAD_REQUEST
                ),
                // Logs and Traces swapped
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        Set.of("misaligned-logs-traces-test"),
                        new TelemetriesDto(TEST_TELEMETRY_TRACES_ID,
                                           null,
                                           TEST_TELEMETRY_LOGS_ID),
                        HttpStatus.BAD_REQUEST
                ),
                // Metrics and Traces swapped
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        Set.of("misaligned-metrics-traces-test"),
                        new TelemetriesDto(null,
                                           TEST_TELEMETRY_TRACES_ID,
                                           TEST_TELEMETRY_METRICS_ID),
                        HttpStatus.BAD_REQUEST
                ),
                // Function creation without telemetry
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        null,
                        HttpStatus.OK
                ),
                // Function creation with an invalid logs telemetry ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(UUID.randomUUID(),
                                           TEST_TELEMETRY_METRICS_ID,
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.NOT_FOUND
                ),
                // Function creation with an invalid metrics telemetry ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           UUID.randomUUID(),
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.NOT_FOUND
                ),
                // Function creation with an invalid traces telemetry ID
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                        100),
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           TEST_TELEMETRY_METRICS_ID,
                                           UUID.randomUUID()),
                        HttpStatus.NOT_FOUND
                ),
                // Function creation with invalid authorization token
                Arguments.of(
                        "invalid-token",
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           TEST_TELEMETRY_METRICS_ID,
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.UNAUTHORIZED
                ),
                // Function creation with expired token
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION,
                                                     SCOPE_LIST_FUNCTIONS),
                                                        -100), // Expired token
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           TEST_TELEMETRY_METRICS_ID,
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.UNAUTHORIZED
                ),
                // Function creation with missing required scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                        List.of(),
                                                        100), // No required scope
                        TEST_TAGS,
                        new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                           TEST_TELEMETRY_METRICS_ID,
                                           TEST_TELEMETRY_TRACES_ID),
                        HttpStatus.FORBIDDEN
                )
        );
    }

    @ParameterizedTest
    @MethodSource("createFunctionWithTelemetryArgs")
    void createFunctionWithTelemetry(
            String token,
            Set<String> tags,
            TelemetriesDto telemetryDto,
            HttpStatus expectedStatus) {
        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .tags(tags)
                .description(TEST_DESCRIPTION)
                .health(TEST_HEALTH_DTO)
                .telemetries(telemetryDto)
                .build();

        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().telemetries()).isEqualTo(telemetryDto);
    }

    @Test
    void createFunctionWithTelemetryFromDifferentAccount() {

        var telemetryDto = new TelemetriesDto(TEST_TELEMETRY_LOGS_ID,
                                              TEST_TELEMETRY_METRICS_ID, TEST_TELEMETRY_TRACES_ID);

        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(TEST_HEALTH_DTO)
                .telemetries(telemetryDto)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT_2,
                                                    List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                    100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    @Test
    void createFunctionWithNonExistantTelemetry() {

        var telemetryId = UUID.randomUUID();
        testTelemetryService.createTelemetry(
                TEST_NCA_ID_3,
                telemetryId,
                TEST_TELEMETRY_ENDPOINT,
                TelemetryProtocol.HTTP,
                TelemetryProvider.PROMETHEUS,
                Set.of(TelemetryType.LOGS),
                TEST_TELEMETRY_LOG_SECRETS
        );
        var telemetryDto = new TelemetriesDto(telemetryId, null, null);

        var requestBody = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(TEST_MODEL_DTOS)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(TEST_HEALTH_DTO)
                .telemetries(telemetryDto)
                .build();

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_REGISTER_FUNCTION, SCOPE_LIST_FUNCTIONS),
                                                    100);
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);

    }

}
