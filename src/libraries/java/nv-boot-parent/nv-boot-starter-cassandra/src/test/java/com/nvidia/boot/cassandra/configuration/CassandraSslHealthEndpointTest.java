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

package com.nvidia.boot.cassandra.configuration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.datastax.oss.driver.api.core.context.DriverContext;
import com.datastax.oss.driver.api.core.cql.Row;
import com.datastax.oss.driver.api.core.cql.Statement;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.Set;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestMethodOrder;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.EnableAutoConfiguration;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.cloud.context.environment.EnvironmentChangeEvent;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Import;
import org.springframework.core.env.MapPropertySource;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.servlet.MockMvc;

/**
 * Exercises {@code GET /actuator/health/ssl} against the real Spring Boot wiring: the
 * {@code SslBundleRegistry} created by {@code SslAutoConfiguration}, the {@code SslHealthIndicator}
 * from {@code SslHealthContributorAutoConfiguration}, and the production
 * {@link CassandraSslBundleConfiguration}. Only the Cassandra driver is mocked.
 * <p>
 * The startup bundle holds the "stale" certificate. Rotation is simulated the way Spring Cloud does
 * it: the PEM properties change, configuration properties are rebound, and
 * {@link RefreshScopeRefreshedEvent} is published. Certificates are generated in memory and passed as
 * inline PEM: the stale one expired yesterday and the fresh one is valid for ten years.
 * <p>
 * Registering the rotated bundle makes the registry invoke the update handler that
 * {@code RefreshingCqlSession} registers, which rebuilds the session a second time. Each build
 * therefore returns a distinct session, and the test asserts that the session installed last is
 * never closed.
 * <p>
 * The two tests share one application context ({@code PER_CLASS} lifecycle) and run in order: the
 * second applies the rotation to the context the first observed in its pre-rotation state.
 */
@SpringBootTest(
        classes = CassandraSslHealthEndpointTest.TestApplication.class,
        properties = {
                "spring.cassandra.ssl.bundle=" + CassandraSslHealthEndpointTest.BUNDLE_NAME,
                "spring.cassandra.username=u",
                "spring.cassandra.password=p",
                "management.endpoints.web.exposure.include=health",
                "management.endpoint.health.show-details=always"
        })
@AutoConfigureMockMvc
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class CassandraSslHealthEndpointTest {

    static final String BUNDLE_NAME = "cassandra-ssl";
    static final String CERT_PROPERTY =
            "spring.ssl.bundle.pem." + BUNDLE_NAME + ".keystore.certificate";
    static final String KEY_PROPERTY =
            "spring.ssl.bundle.pem." + BUNDLE_NAME + ".keystore.private-key";

    private static final String SSL_HEALTH_URL = "/actuator/health/ssl";

    private static final Instant NOW = Instant.now();
    private static final TestCertificates.PemMaterial STALE = TestCertificates.selfSigned(
            "stale-cassandra-client", NOW.minus(Duration.ofDays(730)), NOW.minus(Duration.ofDays(1)));
    private static final TestCertificates.PemMaterial FRESH = TestCertificates.selfSigned(
            "rotated-cassandra-client", NOW.minus(Duration.ofDays(1)), NOW.plus(Duration.ofDays(3650)));

    @DynamicPropertySource
    static void startupCertificate(DynamicPropertyRegistry registry) {
        registry.add(CERT_PROPERTY, STALE::certificate);
        registry.add(KEY_PROPERTY, STALE::privateKey);
    }

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ConfigurableApplicationContext applicationContext;

    @Autowired
    private Sessions sessions;

    @Test
    @Order(1)
    @DisplayName("Before rotation, /actuator/health/ssl returns 503 and reports the expired startup certificate")
    void sslHealthIsDownBeforeRotation() throws Exception {
        var body = mockMvc.perform(get(SSL_HEALTH_URL))
                .andExpect(status().isServiceUnavailable())
                .andExpect(jsonPath("$.status").value("OUT_OF_SERVICE"))
                .andReturn().getResponse().getContentAsString();

        assertThat(body).contains("stale-cassandra-client");
    }

    @Test
    @Order(2)
    @DisplayName("After PEM rotation and a session refresh, /actuator/health/ssl returns 200 and reports the rotated certificate")
    void sslHealthIsUpAndReportsRotatedCertificateAfterRotation() throws Exception {
        rotateCertificateAndRefresh();

        var body = mockMvc.perform(get(SSL_HEALTH_URL))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("UP"))
                .andReturn().getResponse().getContentAsString();

        assertThat(body).contains("rotated-cassandra-client").doesNotContain("stale-cassandra-client");
        verify(sessions.initial()).close();
        verify(sessions.rebuilt()).close();
        verify(sessions.afterBundleUpdate(), never()).close();
    }

    private void rotateCertificateAndRefresh() {
        applicationContext.getEnvironment().getPropertySources().addFirst(new MapPropertySource(
                "rotated-ssl",
                Map.of(CERT_PROPERTY, FRESH.certificate(),
                       KEY_PROPERTY, FRESH.privateKey())));
        applicationContext.publishEvent(
                new EnvironmentChangeEvent(applicationContext, Set.of(CERT_PROPERTY, KEY_PROPERTY)));
        applicationContext.publishEvent(new RefreshScopeRefreshedEvent());
    }

    record Sessions(CqlSession initial, CqlSession rebuilt, CqlSession afterBundleUpdate) {
    }

    @Configuration(proxyBeanMethods = false)
    @EnableAutoConfiguration(excludeName = {
            "com.nvidia.boot.cassandra.CassandraAutoConfiguration",
            "org.springframework.boot.cassandra.autoconfigure.CassandraAutoConfiguration",
            "org.springframework.boot.cassandra.autoconfigure.health.CassandraHealthContributorAutoConfiguration",
            "org.springframework.boot.cassandra.autoconfigure.health.CassandraReactiveHealthContributorAutoConfiguration",
            "org.springframework.boot.data.cassandra.autoconfigure.DataCassandraAutoConfiguration",
            "org.springframework.boot.data.cassandra.autoconfigure.DataCassandraReactiveAutoConfiguration",
            "org.springframework.boot.data.cassandra.autoconfigure.DataCassandraRepositoriesAutoConfiguration",
            "org.springframework.boot.data.cassandra.autoconfigure.DataCassandraReactiveRepositoriesAutoConfiguration"
    })
    @Import(CassandraSslBundleConfiguration.class)
    static class TestApplication {

        @Bean
        Sessions sessions() {
            return new Sessions(mock(CqlSession.class), validSession(), validSession());
        }

        @Bean
        CqlSessionBuilder cqlSessionBuilder(Sessions sessions) {
            var builder = mock(CqlSessionBuilder.class);
            when(builder.withSslContext(any())).thenReturn(builder);
            when(builder.build()).thenReturn(
                    sessions.initial(), sessions.rebuilt(), sessions.afterBundleUpdate());
            return builder;
        }

        private static CqlSession validSession() {
            var session = mock(CqlSession.class);
            var resultSet = mock(ResultSet.class);
            when(resultSet.one()).thenReturn(mock(Row.class));
            when(session.execute(anyString())).thenReturn(resultSet);
            when(session.execute(any(Statement.class))).thenReturn(resultSet);
            var driverContext = mock(DriverContext.class);
            when(driverContext.getSessionName()).thenReturn("test-session");
            when(session.getContext()).thenReturn(driverContext);
            return session;
        }
    }
}
