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
import static org.awaitility.Awaitility.await;

import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.sdk.trace.data.SpanData;
import java.time.Duration;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
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
 * Integration tests verifying that actuator endpoints on the management port
 * produce traces when using a separate management server (ManagementTracingConfiguration).
 */
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class,
                            ActuatorTracingTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "management.server.port=0",
                "management.tracing.enabled=true"
        })
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class ActuatorTracingIntegrationTest {

    private static final AttributeKey<String> URL_PATH = AttributeKey.stringKey("url.path");
    private static final AttributeKey<String> HTTP_REQUEST_METHOD =
            AttributeKey.stringKey("http.request.method");

    @LocalManagementPort
    private int managementPort;

    @Autowired
    private TestRestTemplate testRestTemplate;

    @BeforeEach
    void resetSpanExporter() {
        ActuatorTracingTestConfiguration.SPAN_EXPORTER.reset();
    }

    private String actuatorUrl(String path) {
        return "http://localhost:" + managementPort + path;
    }

    private List<SpanData> awaitSpans() {
        return await()
                .atMost(Duration.ofSeconds(5))
                .until(
                        ActuatorTracingTestConfiguration.SPAN_EXPORTER::getFinishedSpanItems,
                        items -> !items.isEmpty());
    }

    @Test
    @DisplayName("Actuator health endpoint produces HTTP trace span")
    void actuatorHealthEndpointProducesTrace() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/health"),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);

        List<SpanData> spans = awaitSpans();

        assertThat(spans)
                .anyMatch(span -> hasHttpPath(span, "/actuator/health")
                                        && hasHttpMethod(span, "GET"));
    }

    @Test
    @DisplayName("Actuator prometheus endpoint produces HTTP trace span")
    void actuatorPrometheusEndpointProducesTrace() {
        ResponseEntity<String> response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/prometheus"),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);

        List<SpanData> spans = awaitSpans();

        assertThat(spans)
                .anyMatch(span -> hasHttpPath(span, "/actuator/prometheus")
                                        && hasHttpMethod(span, "GET"));
    }

    private static boolean hasHttpPath(SpanData span, String path) {
        var urlPath = span.getAttributes().get(URL_PATH);
        return urlPath != null && urlPath.contains(path);
    }

    private static boolean hasHttpMethod(SpanData span, String method) {
        var httpMethod = span.getAttributes().get(HTTP_REQUEST_METHOD);
        return method.equals(httpMethod);
    }
}
