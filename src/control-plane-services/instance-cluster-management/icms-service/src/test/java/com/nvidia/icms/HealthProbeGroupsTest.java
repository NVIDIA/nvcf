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

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.time.Duration;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.env.YamlPropertySourceLoader;
import org.springframework.boot.health.actuate.endpoint.HealthEndpoint;
import org.springframework.boot.health.actuate.endpoint.HealthEndpointGroups;
import org.springframework.boot.health.autoconfigure.actuate.endpoint.HealthEndpointAutoConfiguration;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthIndicator;
import org.springframework.boot.health.contributor.Status;
import org.springframework.boot.health.registry.DefaultHealthContributorRegistry;
import org.springframework.boot.health.registry.HealthContributorRegistry;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.context.ApplicationContext;
import org.springframework.core.io.ClassPathResource;

/**
 * Kubernetes restarts a container whose liveness probe fails. Cassandra and NATS
 * are shared by every replica, so they must only gate readiness under the NCP
 * profile the chart activates. Liveness fails only when the
 * cassandraSession contributor reports a session stuck after an outage.
 */
class HealthProbeGroupsTest {

    private static ApplicationContextRunner contextRunner(String profile) {
        return contextRunner(profile, Status.UP);
    }

    private static ApplicationContextRunner contextRunner(String profile, Status cassandraSession) {
        return new ApplicationContextRunner()
                .withConfiguration(AutoConfigurations.of(HealthEndpointAutoConfiguration.class))
                .withInitializer(context -> {
                    var sources = context.getEnvironment().getPropertySources();
                    load("application.yaml").forEach(sources::addLast);
                    load("application-" + profile + ".yaml").forEach(sources::addFirst);
                })
                .withBean(HealthContributorRegistry.class, () -> {
                    var registry = new DefaultHealthContributorRegistry();
                    registry.registerContributor("livenessState", (HealthIndicator) () -> Health.up().build());
                    registry.registerContributor("readinessState", (HealthIndicator) () -> Health.up().build());
                    registry.registerContributor("cassandra", (HealthIndicator) () -> Health.down().build());
                    registry.registerContributor("nats", (HealthIndicator) () -> Health.down().build());
                    registry.registerContributor("cassandraSession",
                                                 (HealthIndicator) () -> Health.status(cassandraSession).build());
                    return registry;
                });
    }

    @ParameterizedTest
    @ValueSource(strings = {"ncp"})
    void livenessStaysUpWhenCassandraIsDown(String profile) {
        contextRunner(profile).run(context ->
                assertEquals(Status.UP, endpoint(context).healthForPath("liveness").getStatus()));
    }

    @ParameterizedTest
    @ValueSource(strings = {"ncp"})
    void livenessGoesDownWhenCassandraSessionIsStuck(String profile) {
        contextRunner(profile, Status.DOWN).run(context ->
                assertEquals(Status.DOWN, endpoint(context).healthForPath("liveness").getStatus()));
    }

    @ParameterizedTest
    @ValueSource(strings = {"ncp"})
    void readinessGoesDownWhenCassandraIsDown(String profile) {
        contextRunner(profile).run(context ->
                assertEquals(Status.DOWN, endpoint(context).healthForPath("readiness").getStatus()));
    }

    private static java.util.List<org.springframework.core.env.PropertySource<?>> load(String resource) {
        try {
            return new YamlPropertySourceLoader().load(resource, new ClassPathResource(resource));
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
    }

    private static HealthEndpoint endpoint(ApplicationContext context) {
        return new HealthEndpoint(context.getBean(HealthContributorRegistry.class), null,
                                  context.getBean(HealthEndpointGroups.class), Duration.ZERO);
    }
}
