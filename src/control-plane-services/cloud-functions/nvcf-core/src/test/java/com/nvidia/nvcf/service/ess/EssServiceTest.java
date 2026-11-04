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
package com.nvidia.nvcf.service.ess;

import static com.github.tomakehurst.wiremock.client.WireMock.putRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_TELEMETRY_LOGS_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.util.MockEssServer;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.assertj.core.api.Assertions;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.node.StringNode;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@ExtendWith(MockitoExtension.class)
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class EssServiceTest {

    private static final String HTTP_CLIENT_REQUESTS_METRIC = "http.client.requests";
    private static final String ESS_FUNCTION_VERSION_URI_PREFIX = "/v1/functions/";

    @Autowired
    private EssService essService;

    @Autowired
    private MeterRegistry meterRegistry;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockEssServer.start(essBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockEssServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        MockEssServer.clearSecrets();
    }

    @Test
    void testSavingFunctionVersionSecrets() {
        // Save secrets for a function.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());
        var version = essService.saveFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1, secrets);
        assertThat(version).isNotNull();

        // Verify saved secrets for the function.
        essService.getFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(1);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isEqualTo("AWS_SECRET_ACCESS_KEY");
                        assertThat(dto.value()).isEqualTo(new StringNode("shhh!"));
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secrets"));
    }

    @Test
    void testUpdatingFunctionVersionSecrets() {
        // Save secrets for a function.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());
        var version = essService.saveFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1, secrets);
        assertThat(version).isNotNull();

        // Verify saved secrets for the function.
        essService.getFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(1);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isEqualTo("AWS_SECRET_ACCESS_KEY");
                        assertThat(dto.value()).isEqualTo(new StringNode("shhh!"));
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secrets"));

        // Update secrets for the function.
        var updatedSecrets = Set.of(SecretDto.builder()
                                            .name("AWS_SECRET_ACCESS_KEY")
                                            .value(new StringNode("confidential!"))
                                            .build(),
                                    SecretDto.builder()
                                            .name("NGC_API_KEY")
                                            .value(new StringNode("shhh!shhh!"))
                                            .build());
        var newVersion = essService.saveFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1, updatedSecrets);
        assertThat(newVersion).isNotNull();

        // Verify updated secrets for the function.
        var expectedSecretValues = Set.of(new StringNode("confidential!"),
                                          new StringNode("shhh!shhh!"));
        essService.getFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(2);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isIn(Set.of("AWS_SECRET_ACCESS_KEY", "NGC_API_KEY"));
                        assertThat(dto.value()).isIn(expectedSecretValues);
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to update secrets"));
    }

    @Test
    void testFetchingFunctionVersionSecretNames() {
        // Save secrets for a function.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build(),
                             SecretDto.builder()
                                     .name("NGC_API_KEY")
                                     .value(new StringNode("shhh!shhh!"))
                                     .build());
        var version = essService.saveFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1, secrets);
        assertThat(version).isNotNull();

        // Get secret names for the function.
        var result = essService.getFunctionVersionSecretNames(TEST_FUNCTION_ID, TEST_VERSION_ID_1)
                .orElse(null);
        assertThat(result).isNotNull().hasSize(2)
                .containsExactlyInAnyOrder("AWS_SECRET_ACCESS_KEY", "NGC_API_KEY");
    }

    @Test
    void testFetchingNonExistingFunctionVersionSecretNames() {
        // Fetch secret names without saving secrets for a function.
        var result = essService.getFunctionVersionSecretNames(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        assertThat(result).isNotNull().isEmpty();
    }

    @Test
    void testFetchingNonExistingFunctionSecrets() {
        // Fetch secrets without saving secrets for a function.
        var secretDtos = essService.getFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        assertThat(secretDtos).isNotNull().isEmpty();
    }

    @Test
    void testDeletingFunctionVersionSecrets() {
        // Save secrets for a function.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());
        var version = essService.saveFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1, secrets);
        assertThat(version).isNotNull();

        // Verify saved secrets for the function.
        essService.getFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(1);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isEqualTo("AWS_SECRET_ACCESS_KEY");
                        assertThat(dto.value()).isEqualTo(new StringNode("shhh!"));
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secrets"));

        // Delete secrets saved for the function version.
        essService.deleteFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1);

        // Fetch secrets after deleting them.
        var secretDtos = essService.getFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        assertThat(secretDtos).isNotNull().isEmpty();
    }

    @Test
    void testDeletingFunctionSecrets() {
        // Save secrets for a function version.
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());
        var version = essService.saveFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1, secrets);
        assertThat(version).isNotNull();

        // Verify saved secrets for the function version.
        essService.getFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1)
                .map(dtos -> {
                    assertThat(dtos).isNotNull().hasSize(1);
                    dtos.forEach(dto -> {
                        assertThat(dto.name()).isEqualTo("AWS_SECRET_ACCESS_KEY");
                        assertThat(dto.value()).isEqualTo(new StringNode("shhh!"));
                    });
                    return dtos;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secret"));

        // Delete secrets for all the function versions.
        essService.deleteFunctionSecrets(TEST_FUNCTION_ID);

        // Fetch secrets after deleting the secrets path entirely for the function.
        var secretDtos = essService.getFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        assertThat(secretDtos).isNotNull().isEmpty();
    }

    @Test
    void testSavingTelemetrySecret() {
        var secret = SecretDto.builder()
                .name("GRAFANA_CREDENTIALS")
                .value(new StringNode("nca-grafana-shhh!"))
                .build();
        var version = essService.saveTelemetrySecret(TEST_NCA_ID,TEST_TELEMETRY_LOGS_ID, secret);
        assertThat(version).isNotNull();

        essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID)
                .map(secretDto -> {
                    assertThat(secretDto).isNotNull();
                    assertThat(secretDto.name()).isEqualTo("GRAFANA_CREDENTIALS");
                    assertThat(secretDto.value()).isEqualTo(new StringNode("nca-grafana-shhh!"));
                    return secretDto;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secret"));
    }

    @Test
    void testUpdatingTelemetrySecret() {
        // Save initial secret
        var secret = SecretDto.builder()
                .name("GRAFANA_CREDENTIALS")
                .value(new StringNode("initial-nca-secret"))
                .build();
        var version = essService.saveTelemetrySecret(TEST_NCA_ID,TEST_TELEMETRY_LOGS_ID, secret);
        assertThat(version).isNotNull();

        essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID)
                .map(secretDto -> {
                    assertThat(secretDto).isNotNull();
                    assertThat(secretDto.name()).isEqualTo("GRAFANA_CREDENTIALS");
                    assertThat(secretDto.value()).isEqualTo(new StringNode("initial-nca-secret"));
                    return secretDto;
                })
                .orElseGet(() -> Assertions.fail("Failed to save secret"));

        // Update secret
        var updatedSecret =
                SecretDto.builder()
                        .name("GRAFANA_CREDENTIALS")
                        .value(new StringNode("updated-nca-secret"))
                        .build();

        essService.saveTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID, updatedSecret);

        // Verify updated secret
        essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID)
                .map(secretDto -> {
                    assertThat(secretDto).isNotNull();
                    assertThat(secretDto.name()).isEqualTo("GRAFANA_CREDENTIALS");
                    assertThat(secretDto.value()).isEqualTo(new StringNode("updated-nca-secret"));
                    return secretDto;
                })
                .orElseGet(() -> Assertions.fail("Failed to update secret"));
    }

    @Test
    void testDeletingTelemetrySecret() {
        var secret = SecretDto.builder()
                .name("GRAFANA_CREDENTIALS")
                .value(new StringNode("nca-grafana-shhh!"))
                .build();
        essService.saveTelemetrySecret(TEST_NCA_ID,TEST_TELEMETRY_LOGS_ID, secret);

        essService.deleteTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID);

        var secretDtos = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID);
        assertThat(secretDtos).isNotNull().isEmpty();
    }

    @Test
    void testFetchingNonExistingTelemetrySecret() {
        var secrets = essService.getTelemetrySecret(TEST_NCA_ID, TEST_TELEMETRY_LOGS_ID);
        assertThat(secrets).isNotNull().isEmpty();
    }

    @Test
    void shouldRecordMetricsForEssResourceServerCall() {
        MockEssServer.getMockEssServer().resetRequests();

        var resourceServerRequestCountBefore = essFunctionVersionRequestCount();
        var secrets = Set.of(SecretDto.builder()
                                     .name("AWS_SECRET_ACCESS_KEY")
                                     .value(new StringNode("shhh!"))
                                     .build());

        var version = essService.saveFunctionVersionSecrets(TEST_FUNCTION_ID, TEST_VERSION_ID_1, secrets);

        assertThat(version).isNotNull();
        assertThat(essFunctionVersionRequestCount()).isGreaterThan(resourceServerRequestCountBefore);
        MockEssServer.getMockEssServer().verify(1, putRequestedFor(urlPathMatching(
                "/v1/functions/" + TEST_FUNCTION_ID + "/versions/" + TEST_VERSION_ID_1)));
    }

    private long essFunctionVersionRequestCount() {
        return meterRegistry.find(HTTP_CLIENT_REQUESTS_METRIC)
                .timers()
                .stream()
                .filter(this::isEssFunctionVersionRequestTimer)
                .mapToLong(Timer::count)
                .sum();
    }

    private boolean isEssFunctionVersionRequestTimer(Timer timer) {
        return timer.getId().getTags()
                .stream()
                .anyMatch(tag -> tag.getValue().contains(ESS_FUNCTION_VERSION_URI_PREFIX));
    }
}
