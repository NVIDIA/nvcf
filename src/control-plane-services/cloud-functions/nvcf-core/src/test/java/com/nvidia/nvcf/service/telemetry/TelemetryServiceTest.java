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
package com.nvidia.nvcf.service.telemetry;

import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOG_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_METRICS_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_TRACES_SECRETS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;
import static org.assertj.core.api.AssertionsForClassTypes.assertThatThrownBy;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetriesUdt;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProtocol;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryProvider;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.telemetry.TestTelemetryService;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryDto;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProtocolEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryProviderEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryRequest;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryTypeEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.util.MockEssServer;
import java.util.Base64;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.assertj.core.api.Assertions;
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
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class TelemetryServiceTest {
    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TelemetryService telemetryService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestTelemetryService testTelemetryService;

    @Autowired
    private TelemetryLookupService telemetryLookupService;

    @Autowired
    private TelemetryMapperService telemetryMapperService;

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

    @BeforeAll
    void setupTelemetryData() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
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
        testTelemetryService.deleteAllTelemetries();
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockEssServer.clearSecrets();
    }


    Stream<Arguments> provideTelemetryCombinations() {
        return Stream.of(
                // Only logs telemetry is provided
                Arguments.of(TEST_TELEMETRY_LOGS_ID,
                        null,
                        null,
                        null),
                // Only metrics telemetry is provided
                Arguments.of(null,
                        TEST_TELEMETRY_METRICS_ID,
                        null,
                        null),
                // Only traces telemetry is provided
                Arguments.of(null,
                        null,
                        TEST_TELEMETRY_TRACES_ID,
                        null),
                // Logs and metrics telemetry are provided, no traces
                Arguments.of(TEST_TELEMETRY_LOGS_ID,
                        TEST_TELEMETRY_METRICS_ID,
                        null,
                        null),
                // Metrics and traces telemetry are provided, no logs
                Arguments.of(null,
                        TEST_TELEMETRY_METRICS_ID,
                        TEST_TELEMETRY_TRACES_ID,
                        null),
                // Logs and traces telemetry are provided, no metrics
                Arguments.of(TEST_TELEMETRY_LOGS_ID,
                        null,
                        TEST_TELEMETRY_TRACES_ID,
                        null),
                // Logs, metrics, and traces telemetry are provided
                Arguments.of(TEST_TELEMETRY_LOGS_ID,
                        TEST_TELEMETRY_METRICS_ID,
                        TEST_TELEMETRY_TRACES_ID,
                        null),
                // Logs telemetry is invalid
                Arguments.of(UUID.randomUUID(),
                        null,
                        null,
                        NotFoundException.class),
                // Metrics telemetry is invalid
                Arguments.of(null,
                        UUID.randomUUID(),
                        null,
                        NotFoundException.class),
                // Traces telemetry is invalid
                Arguments.of(null,
                        null,
                        UUID.randomUUID(),
                        NotFoundException.class),
                // Logs, metrics, and traces telemetry are invalid
                Arguments.of(UUID.randomUUID(),
                        UUID.randomUUID(),
                        UUID.randomUUID(),
                        NotFoundException.class)
        );
    }

    @ParameterizedTest
    @MethodSource("provideTelemetryCombinations")
    void testTelemetryEncoding(UUID logsTelemetryId,
                               UUID metricsTelemetryId,
                               UUID tracesTelemetryId,
                               Class<? extends Throwable> expectedException)
            throws JacksonException {
        var udt = TelemetriesUdt.builder()
                .logsTelemetryId(logsTelemetryId)
                .metricsTelemetryId(metricsTelemetryId)
                .tracesTelemetryId(tracesTelemetryId)
                .build();
        if (expectedException != null) {
            assertThatExceptionOfType(expectedException).isThrownBy(() ->
                    telemetryService.base64Encode(TEST_NCA_ID, udt));
            return;
        }
        String encodedTelemetry = telemetryService.base64Encode(TEST_NCA_ID, udt);
        assertThat(encodedTelemetry).isNotBlank();

        var decodedJson = new String(Base64.getDecoder().decode(encodedTelemetry));
        assertThat(decodedJson).contains("telemetries");

        var telemetriesDto = jsonMapper.readValue(
                decodedJson,
                TelemetryService.SerializeTelemetriesDto.class);

        var telemetries = telemetriesDto.telemetries();
        var logsTelemetry = telemetries.logsTelemetry();
        var metricsTelemetry = telemetries.metricsTelemetry();
        var tracesTelemetry = telemetries.tracesTelemetry();

        validateTelemetryEntry(TEST_NCA_ID, logsTelemetryId, logsTelemetry);
        validateTelemetryEntry(TEST_NCA_ID, metricsTelemetryId, metricsTelemetry);
        validateTelemetryEntry(TEST_NCA_ID, tracesTelemetryId, tracesTelemetry);
    }

    @Test
    void testSaveTelemetry() {
        var secret = SecretDto.builder()
                .name("prometheus-secret")
                .value(jsonMapper.createObjectNode()
                        .put("clientCert", "client-cert")
                        .put("clientKey", "cert-key")
                        .put("caCert", "ca-cert"))
                .build();

        var telemetryRequest = TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.PROMETHEUS)
                .types(Set.of(TelemetryTypeEnum.LOGS))
                .secret(secret)
                .build();

        var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID_2);
        var entity = telemetryService.saveTelemetry(accountEntity, telemetryRequest);
        var telemetryId = entity.getKey().getTelemetryId();

        essService.getTelemetrySecret(TEST_NCA_ID_2, telemetryId)
                .map(secretDto -> {
                    assertThat(secretDto).isNotNull();
                    assertThat(secretDto.name()).isEqualTo("prometheus-secret");
                    assertThat(secretDto.value().get("clientCert").asString()).isEqualTo("client-cert");
                    assertThat(secretDto.value().get("clientKey").asString()).isEqualTo("cert-key");
                    assertThat(secretDto.value().get("caCert").asString()).isEqualTo("ca-cert");
                    return secretDto;
                })
                .orElseGet(() -> Assertions.fail("Failed to save telemetry secret"));
    }

    @Test
    void testDeleteTelemetry(){
        var secret = SecretDto.builder()
                .name("prometheus-secret")
                .value(jsonMapper.createObjectNode()
                        .put("clientCert", "client-cert")
                        .put("clientKey", "cert-key")
                        .put("caCert", "ca-cert"))
                .build();

        var telemetryRequest = TelemetryRequest.builder()
                .endpoint(TEST_TELEMETRY_ENDPOINT)
                .protocol(TelemetryProtocolEnum.HTTP)
                .provider(TelemetryProviderEnum.PROMETHEUS)
                .types(Set.of(TelemetryTypeEnum.LOGS))
                .secret(secret)
                .build();

        var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID);
        var entity = telemetryService.saveTelemetry(accountEntity, telemetryRequest);
        var telemetryId = entity.getKey().getTelemetryId();
        telemetryService.deleteTelemetry(TEST_NCA_ID,telemetryId);

        assertThat(essService.getTelemetrySecret(TEST_NCA_ID, telemetryId).isEmpty()).isTrue();
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

        assertThatThrownBy(
                () -> telemetryService.deleteTelemetry(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining(TEST_FUNCTION_ID.toString())
                .hasMessageContaining(TEST_VERSION_ID_1.toString())
                .hasMessageContaining(TEST_TELEMETRY_LOGS_ID.toString());
    }


    private void validateTelemetryEntry(String ncaId, UUID telemetryId, TelemetryDto actualDto) {
        if (telemetryId != null) {
            var telemetryByAccountEntity = telemetryLookupService
                                    .lookupByAccountAndTelemetryIdOrThrow(ncaId, telemetryId);
            var expectedDto = telemetryMapperService.toTelemetryDto(telemetryByAccountEntity);
            assertThat(actualDto).isEqualTo(expectedDto);
        } else {
            assertThat(actualDto).isNull();
        }
    }
}
