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
package com.nvidia.nvct.actuator;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.MediaType.TEXT_PLAIN;

import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import io.micrometer.core.instrument.MeterRegistry;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.web.server.LocalManagementPort;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.ContextConfiguration;

/**
 * Integration tests for the public actuator surface on the management port.
 * Uses a separate management port (management.server.port=0) so actuator is tested on its own
 * server. Verifies the expected exposed endpoints remain available and non-allowed endpoints are
 * not exposed.
 */
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "management.server.port=0"
        })
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class MetricsIntegrationTest {

    @LocalManagementPort
    private int managementPort;

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private MeterRegistry meterRegistry;

    private String actuatorUrl(String path) {
        return "http://localhost:" + managementPort + path;
    }

    @Test
    @DisplayName("Actuator health endpoint is available")
    void actuatorHealthEndpointIsAvailable() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/health"),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    @DisplayName("Actuator liveness endpoint is available")
    void actuatorLivenessEndpointIsAvailable() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/health/liveness"),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    @DisplayName("Actuator readiness endpoint is available")
    void actuatorReadinessEndpointIsAvailable() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/health/readiness"),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    @DisplayName("Prometheus endpoint returns 200 and Prometheus text format")
    void prometheusEndpointReturnsMetrics() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/prometheus"),
                String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getHeaders().getContentType())
                .isNotNull()
                .satisfies(mediaType -> assertThat(mediaType.isCompatibleWith(TEXT_PLAIN)).isTrue());

        var body = response.getBody();
        assertThat(body).isNotNull();
        assertThat(body).contains("# HELP");
        assertThat(body).contains("# TYPE");
    }

    @Test
    @DisplayName("Prometheus endpoint includes JVM metrics")
    void prometheusEndpointIncludesJvmMetrics() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/prometheus"),
                String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        var body = response.getBody();
        assertThat(body).isNotNull();
        assertThat(body).contains("jvm_");
    }

    @Test
    @DisplayName("Metrics endpoint is available")
    void metricsEndpointIsAvailable() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/metrics"),
                String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    @DisplayName("Environment endpoint is not exposed via management.endpoints.web.exposure.include")
    void envEndpointIsNotAnonymouslyAvailable() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/env"),
                String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    @Test
    @DisplayName("MeterRegistry is configured and has metrics")
    void meterRegistryHasMetrics() {
        assertThat(meterRegistry).isNotNull();
        assertThat(meterRegistry.getMeters()).isNotEmpty();
    }
}
