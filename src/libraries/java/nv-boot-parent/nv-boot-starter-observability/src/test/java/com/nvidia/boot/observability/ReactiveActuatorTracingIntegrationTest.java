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
import com.nvidia.boot.observability.tracing.client.OtelReactiveClientRequestObservationConvention;
import com.nvidia.boot.observability.tracing.server.OtelReactiveServerRequestObservationConvention;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.web.server.LocalManagementPort;
import org.springframework.context.ApplicationContext;
import org.springframework.http.server.reactive.observation.ServerRequestObservationConvention;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.web.reactive.function.client.ClientRequestObservationConvention;

@SpringBootTest(
        classes = {TestApplication.class, ActuatorTracingTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "spring.main.web-application-type=reactive",
                "management.server.port=0",
                "management.tracing.enabled=true",
                "management.endpoints.web.exposure.include=*",
                "spring.autoconfigure.exclude=org.springframework.boot.cassandra.autoconfigure.CassandraAutoConfiguration,org.springframework.boot.data.cassandra.autoconfigure.DataCassandraAutoConfiguration,org.springframework.boot.cassandra.autoconfigure.health.CassandraHealthContributorAutoConfiguration"
        })
class ReactiveActuatorTracingIntegrationTest {

    @LocalManagementPort
    private int managementPort;

    @Autowired
    private ApplicationContext context;

    @Test
    @DisplayName("Reactive observation convention is loaded")
    void reactiveObservationConventionIsLoaded() {
        assertThat(context.getBeansOfType(ServerRequestObservationConvention.class).values())
                .hasAtLeastOneElementOfType(OtelReactiveServerRequestObservationConvention.class);
        assertThat(context.getBeansOfType(ClientRequestObservationConvention.class).values())
            .hasAtLeastOneElementOfType(OtelReactiveClientRequestObservationConvention.class);
    }

    @Test
    @DisplayName("Servlet observation convention is not loaded")
    void servletObservationConventionIsNotLoaded() {
        assertThat(context.getBeansOfType(
                org.springframework.http.server.observation.ServerRequestObservationConvention.class)).isEmpty();
        // not checking client requests because any webserver might use Blocking or Reactive web clients for outgoing traffic
        // in the tests both will be present due to the test dependencies
    }

    @Test
    @DisplayName("Servlet management configuration is not loaded")
    void servletManagementConfigurationIsNotLoaded() {
        assertThat(context.getBeansOfType(ServletManagementTracingConfiguration.class)).isEmpty();
    }

    private WebTestClient managementClient() {
        return WebTestClient.bindToServer()
                .baseUrl("http://localhost:" + managementPort)
                .build();
    }

    @Test
    @DisplayName("Actuator health endpoint is available on management port")
    void actuatorHealthEndpointIsAvailable() {
        managementClient().get().uri("/actuator/health")
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.status").isEqualTo("UP");
    }

    @Test
    @DisplayName("Actuator metrics endpoint is available on management port")
    void actuatorMetricsEndpointIsAvailable() {
        managementClient().get().uri("/actuator/metrics")
                .exchange()
                .expectStatus().isOk();
    }
}
