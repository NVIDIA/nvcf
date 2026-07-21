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

package com.nvidia.boot.observability;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.observability.tracing.actuator.ServletManagementTracingConfiguration;
import com.nvidia.boot.observability.tracing.client.OtelBlockingClientRequestObservationConvention;
import com.nvidia.boot.observability.tracing.server.OtelServerRequestObservationConvention;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.web.server.LocalManagementPort;
import org.springframework.context.ApplicationContext;
import org.springframework.http.HttpStatus;
import org.springframework.http.client.observation.ClientRequestObservationConvention;
import org.springframework.http.server.observation.ServerRequestObservationConvention;

/**
 * Integration tests verifying that actuator endpoints on the management port
 * are reachable when using a separate management server (ServletManagementTracingConfiguration).
 *
 * <p>Full trace verification (span capture) should be covered by the app's
 * integration test which uses a richer application context.
 */
@SpringBootTest(
        classes = {TestApplication.class, ActuatorTracingTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "spring.main.web-application-type=servlet",
                "management.server.port=0",
                "management.tracing.enabled=true",
                "management.endpoints.web.exposure.include=*",
                "spring.autoconfigure.exclude=org.springframework.boot.cassandra.autoconfigure.CassandraAutoConfiguration,org.springframework.boot.data.cassandra.autoconfigure.DataCassandraAutoConfiguration,org.springframework.boot.cassandra.autoconfigure.health.CassandraHealthContributorAutoConfiguration"
        })
@AutoConfigureTestRestTemplate
class ServletActuatorTracingIntegrationTest {

    @LocalManagementPort
    private int managementPort;

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private ApplicationContext context;

    private String actuatorUrl(String path) {
        return "http://localhost:" + managementPort + path;
    }

    @Test
    @DisplayName("Servlet observation convention is loaded")
    void servletObservationConventionIsLoaded() {
        assertThat(context.getBeansOfType(ServerRequestObservationConvention.class).values())
                .hasAtLeastOneElementOfType(OtelServerRequestObservationConvention.class);
        assertThat(context.getBeansOfType(ClientRequestObservationConvention.class).values())
            .hasAtLeastOneElementOfType(OtelBlockingClientRequestObservationConvention.class);
    }

    @Test
    @DisplayName("Reactive observation convention is not loaded")
    void reactiveObservationConventionIsNotLoaded() {
        assertThat(context.getBeansOfType(
            org.springframework.http.server.reactive.observation.ServerRequestObservationConvention.class)).isEmpty();
        // not checking client requests because any webserver might use Blocking or Reactive web clients for outgoing traffic
        // in the tests both will be present due to the test dependencies
    }

    @Test
    @DisplayName("Servlet management configuration is loaded")
    void servletManagementConfigurationIsNotLoaded() {
        assertThat(context.getBeansOfType(ServletManagementTracingConfiguration.class)).isNotEmpty();
    }

    @Test
    @DisplayName("Actuator health endpoint is available on management port")
    void actuatorHealthEndpointIsAvailable() {
        var response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/health"),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    @DisplayName("Actuator metrics endpoint is available on management port")
    void actuatorMetricsEndpointIsAvailable() {
        var response = testRestTemplate.getForEntity(
                actuatorUrl("/actuator/metrics"),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
    }
}
