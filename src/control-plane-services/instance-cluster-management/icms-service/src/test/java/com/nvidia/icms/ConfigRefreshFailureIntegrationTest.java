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
package com.nvidia.icms;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.awaitility.Awaitility.await;

import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.util.CassandraTestConfiguration;
import java.net.URI;
import java.time.Duration;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.web.server.LocalManagementPort;
import org.springframework.cloud.kubernetes.commons.config.reload.ConfigurationUpdateStrategy;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Import;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.core.env.MapPropertySource;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ActiveProfiles;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.web.client.HttpStatusCodeException;
import org.springframework.web.client.RestTemplate;

/**
 * Replays the production incident in which a live ConfigMap update carried a misspelled enum
 * value ({@code icms.telemetry.oauth2.auth-method}). The update is soaked into the running
 * context through the Spring Cloud Kubernetes {@link ConfigurationUpdateStrategy} used by the
 * event reload watcher.
 *
 * <p>Before the custom update strategy, this refresh returned normally while leaving the
 * context poisoned: refresh-scoped beans such as {@code telemetryProperties} could no longer
 * be re-created, so requests failed downstream with confusing status codes (HTTP 400 in
 * production) and Kubernetes probes stayed green. The update strategy must instead report
 * readiness DOWN until a fixed configuration is refreshed.
 */
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "management.server.port=0",
                "spring.profiles.active=test",
                // Production runs with telemetry enabled. The validator materializes its
                // refresh-scoped properties after a config refresh.
                "icms.telemetry.enabled=true"})
@ContextConfiguration(initializers = IntegrationTest.Initializer.class)
@ActiveProfiles("test")
@Import(CassandraTestConfiguration.class)
class ConfigRefreshFailureIntegrationTest {

    private static final String TEST_SOURCE = "test-remote-config";

    private static final String ENUM_PROPERTY = "icms.telemetry.oauth2.auth-method";

    /** Enum value with a typo; valid values are CLIENT_SECRET_POST and CLIENT_SECRET_BASIC. */
    private static final String MISSPELLED_ENUM = "CLIENT_SECRET_BASICS";

    @Autowired
    private ConfigurableApplicationContext context;

    @Autowired
    private ConfigurationUpdateStrategy configurationUpdateStrategy;

    @LocalManagementPort
    private int managementPort;

    @Test
    void invalidEnumInLiveConfigUpdate_flipsReadinessUntilFixedConfigRefreshes() {
        // LegacyContextRefresher rebuilds the bootstrap environment retaining only standard
        // sources; in production the profile comes from the SPRING_PROFILES_ACTIVE env var,
        // here the test profile is exposed through the (retained) system properties source.
        var previousProfiles = System.getProperty("spring.profiles.active");
        System.setProperty("spring.profiles.active", "test");
        try {
            // Baseline: readiness and liveness answer normally.
            assertHealth("/actuator/health/readiness", HttpStatus.OK, "UP");
            assertHealth("/actuator/health/liveness", HttpStatus.OK, "UP");

            // The ConfigMap update with the misspelled enum arrives; the reload watcher
            // runs the configured update strategy.
            overrideRemoteConfigValue(MISSPELLED_ENUM);
            configurationUpdateStrategy.reloadProcedure().run();

            // The poisoned bean can no longer be created from the broken environment.
            assertThatThrownBy(() -> context.getBean("scopedTarget.telemetryProperties"))
                    .hasMessageContaining("TelemetryProperties");

            // Readiness reports DOWN so Kubernetes takes the pod out of service; liveness
            // stays UP so the pod is not restarted and can self-heal.
            await().atMost(Duration.ofSeconds(5)).untilAsserted(() -> assertHealth(
                    "/actuator/health/readiness", HttpStatus.SERVICE_UNAVAILABLE, "DOWN"));
            assertHealth("/actuator/health/liveness", HttpStatus.OK, "UP");

            // The ConfigMap is fixed and another refresh is triggered: the pod recovers
            // without a restart.
            overrideRemoteConfigValue("CLIENT_SECRET_BASIC");
            configurationUpdateStrategy.reloadProcedure().run();

            await().atMost(Duration.ofSeconds(5)).untilAsserted(() ->
                    assertHealth("/actuator/health/readiness", HttpStatus.OK, "UP"));
        } finally {
            // Restore a pristine environment for any other test sharing this context.
            var sources = ((ConfigurableEnvironment) context.getEnvironment())
                    .getPropertySources();
            if (sources.contains(TEST_SOURCE)) {
                sources.remove(TEST_SOURCE);
            }
            configurationUpdateStrategy.reloadProcedure().run();
            if (previousProfiles == null) {
                System.clearProperty("spring.profiles.active");
            } else {
                System.setProperty("spring.profiles.active", previousProfiles);
            }
        }
    }

    private void overrideRemoteConfigValue(String authMethod) {
        var sources = ((ConfigurableEnvironment) context.getEnvironment()).getPropertySources();
        var source = new MapPropertySource(TEST_SOURCE, Map.of(ENUM_PROPERTY, authMethod));
        if (sources.contains(TEST_SOURCE)) {
            sources.replace(TEST_SOURCE, source);
        } else {
            sources.addFirst(source);
        }
    }

    private void assertHealth(String path, HttpStatus status, String healthStatus) {
        var response = getManagementHealth(path);
        assertThat(response.status()).isEqualTo(status);
        assertThat(response.body()).contains("\"status\":\"" + healthStatus + "\"");
    }

    private HealthResponse getManagementHealth(String path) {
        var endpoint = URI.create("http://localhost:" + managementPort + path);
        try {
            var response = new RestTemplate().exchange(
                    RequestEntity.get(endpoint).build(), String.class);
            return new HealthResponse(
                    HttpStatus.valueOf(response.getStatusCode().value()), response.getBody());
        } catch (HttpStatusCodeException e) {
            return new HealthResponse(
                    HttpStatus.valueOf(e.getStatusCode().value()), e.getResponseBodyAsString());
        }
    }

    private record HealthResponse(HttpStatus status, String body) {}
}
