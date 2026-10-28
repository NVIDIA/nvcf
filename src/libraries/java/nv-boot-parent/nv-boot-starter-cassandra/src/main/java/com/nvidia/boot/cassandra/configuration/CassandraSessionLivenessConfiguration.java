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

import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.RETRY_POLICY_CLASS;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import com.datastax.oss.driver.api.core.config.DriverConfig;
import com.datastax.oss.driver.api.core.config.DriverConfigLoader;
import com.datastax.oss.driver.api.core.config.OptionsMap;
import com.datastax.oss.driver.api.core.config.TypedDriverOption;
import com.datastax.oss.driver.api.core.context.DriverContext;
import com.datastax.oss.driver.api.core.cql.SimpleStatement;
import com.nvidia.boot.cassandra.retry.NextHostRetryPolicy;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.time.Clock;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.convert.DurationStyle;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.env.Environment;

/**
 * Registers the {@code cassandraSession} health contributor. Applications opt in by adding it to
 * the liveness group, for example
 * {@code management.endpoint.health.group.liveness.include: [livenessState, cassandraSession]}.
 */
@Slf4j
@Configuration
@ConditionalOnProperty(name = "app.cassandra.session-liveness.enabled", havingValue = "true",
                       matchIfMissing = true)
public class CassandraSessionLivenessConfiguration {

    private static final String HEALTH_QUERY = "SELECT release_version FROM system.local";

    @Bean
    CassandraSessionLivenessIndicator cassandraSessionHealthIndicator(
            ObjectProvider<CqlSession> sessions,
            ObjectProvider<CqlSessionBuilder> sessionBuilders,
            ObjectProvider<DriverConfigLoader> configLoaders,
            Environment environment) {
        var gracePeriod = DurationStyle.detectAndParse(
                environment.getProperty("app.cassandra.session-liveness.grace-period", "3m"));
        var checkInterval = DurationStyle.detectAndParse(
                environment.getProperty("app.cassandra.session-liveness.check-interval", "30s"));
        var session = sessions.getIfUnique();
        if (session == null) {
            log.info("No unique CqlSession; the cassandraSession liveness check always reports UP");
            return new CassandraSessionLivenessIndicator(() -> true, () -> false, gracePeriod,
                                                         Clock.systemUTC());
        }
        var profile = session.getContext().getConfig().getDefaultProfile()
                .withClass(RETRY_POLICY_CLASS, NextHostRetryPolicy.class);
        var healthQuery = SimpleStatement.builder(HEALTH_QUERY)
                .setExecutionProfile(profile)
                .build();
        return new CassandraSessionLivenessIndicator(
                () -> succeeds(() -> session.execute(healthQuery)),
                () -> freshSessionSucceeds(sessionBuilders, configLoaders),
                gracePeriod,
                checkInterval);
    }

    static boolean freshSessionSucceeds(ObjectProvider<CqlSessionBuilder> sessionBuilders,
                                        ObjectProvider<DriverConfigLoader> configLoaders) {
        var builder = sessionBuilders.getIfAvailable();
        if (builder == null) {
            return false;
        }
        var configLoader = configLoaders.getIfUnique();
        if (configLoader != null) {
            builder.withConfigLoader(probeConfigLoader(configLoader));
        }
        // The probe shares the application session's name and meter IDs, and closing a session
        // removes its meters. A private registry keeps the application's driver metrics intact.
        try (var fresh = builder.withMetricRegistry(new SimpleMeterRegistry()).build()) {
            fresh.execute(HEALTH_QUERY);
            return true;
        } catch (RuntimeException e) {
            log.debug("New Cassandra session could not connect", e);
            return false;
        }
    }

    /**
     * The application's driver settings with schema metadata off, which keeps the probe cheap
     * when many pods check at once. The probe only reads the application's loader: a session
     * registers with its loader on start and closes it on close.
     */
    static DriverConfigLoader probeConfigLoader(DriverConfigLoader applicationLoader) {
        var overrides = new OptionsMap();
        overrides.put(TypedDriverOption.METADATA_SCHEMA_ENABLED, false);
        return DriverConfigLoader.compose(DriverConfigLoader.fromMap(overrides),
                                          new ReadOnlyConfigLoader(applicationLoader));
    }

    private record ReadOnlyConfigLoader(DriverConfigLoader delegate) implements DriverConfigLoader {

        @Override
        public DriverConfig getInitialConfig() {
            return delegate.getInitialConfig();
        }

        @Override
        public void onDriverInit(DriverContext context) {
        }

        @Override
        public CompletionStage<Boolean> reload() {
            return CompletableFuture.completedFuture(false);
        }

        @Override
        public boolean supportsReloading() {
            return false;
        }

        @Override
        public void close() {
        }
    }

    private static boolean succeeds(Runnable check) {
        try {
            check.run();
            return true;
        } catch (RuntimeException e) {
            return false;
        }
    }
}
