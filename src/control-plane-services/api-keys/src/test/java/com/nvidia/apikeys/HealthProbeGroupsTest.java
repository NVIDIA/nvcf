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
package com.nvidia.apikeys;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.time.Duration;
import org.junit.jupiter.api.Test;
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
 * Kubernetes restarts a container whose liveness probe fails. Cassandra is
 * shared by every replica, so it must only gate readiness. Liveness fails
 * only when the cassandraSession contributor reports a session stuck after an outage.
 */
class HealthProbeGroupsTest {

    private static ApplicationContextRunner contextRunner(Status cassandraSession) {
        return new ApplicationContextRunner()
                .withConfiguration(AutoConfigurations.of(HealthEndpointAutoConfiguration.class))
                .withInitializer(context -> {
                    try {
                        var sources = new YamlPropertySourceLoader()
                                .load("application", new ClassPathResource("application.yaml"));
                        var propertySources = context.getEnvironment().getPropertySources();
                        sources.forEach(propertySources::addLast);
                    } catch (IOException e) {
                        throw new UncheckedIOException(e);
                    }
                })
                .withBean(HealthContributorRegistry.class, () -> {
                    var registry = new DefaultHealthContributorRegistry();
                    register(registry, "livenessState", Status.UP);
                    register(registry, "readinessState", Status.UP);
                    register(registry, "cassandra", Status.DOWN);
                    register(registry, "cassandraSession", cassandraSession);
                    return registry;
                });
    }

    @Test
    void livenessStaysUpWhenCassandraIsDown() {
        contextRunner(Status.UP).run(context ->
                assertEquals(Status.UP, status(context, "liveness")));
    }

    @Test
    void livenessGoesDownWhenCassandraSessionIsStuck() {
        contextRunner(Status.DOWN).run(context ->
                assertEquals(Status.DOWN, status(context, "liveness")));
    }

    @Test
    void readinessGoesDownWhenCassandraIsDown() {
        contextRunner(Status.UP).run(context ->
                assertEquals(Status.DOWN, status(context, "readiness")));
    }

    private static Status status(ApplicationContext context, String group) {
        var registry = context.getBean(HealthContributorRegistry.class);
        var groups = context.getBean(HealthEndpointGroups.class);
        var endpoint = new HealthEndpoint(registry, null, groups, Duration.ZERO);
        return endpoint.healthForPath(group).getStatus();
    }

    private static void register(HealthContributorRegistry registry, String name, Status status) {
        registry.registerContributor(name, (HealthIndicator) () -> Health.status(status).build());
    }
}
