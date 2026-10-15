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

package com.nvidia.boot.observability.tracing.cassandra;

import com.datastax.oss.driver.api.core.CqlSession;
import io.micrometer.observation.ObservationRegistry;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.boot.autoconfigure.AutoConfigureBefore;
import org.springframework.boot.cassandra.autoconfigure.CassandraAutoConfiguration;
import org.springframework.boot.cassandra.autoconfigure.CqlSessionBuilderCustomizer;
import org.springframework.boot.autoconfigure.condition.ConditionalOnBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.data.cassandra.observability.ObservableCqlSessionFactory;
import org.springframework.data.cassandra.observability.ObservationRequestTracker;

/**
 * Cassandra observability for the <strong>Spring Boot default {@link CqlSession}</strong> path:
 * wraps the auto-configured session with {@link ObservableCqlSessionFactory} so queries can emit
 * observations (including db.* attributes such as consistency, coordinator DC, keyspace, etc.)
 * when an {@link ObservationRegistry} is present.
 *
 * <p>When {@code nv-boot-starter-cassandra}'s {@code CassandraSslBundleConfiguration} is active,
 * it registers {@link ObservationRequestTracker} on the
 * {@link com.datastax.oss.driver.api.core.CqlSessionBuilder} (bean
 * {@code cassandraObservationRequestTrackerCustomizer}) and performs wrapping in
 * {@code com.nvidia.boot.cassandra.configuration.CassandraSslBundleConfiguration}; this configuration
 * then omits the duplicate customizer via
 * {@code @ConditionalOnMissingBean(name = "cassandraObservationRequestTrackerCustomizer")} and skips
 * {@link #observableCqlSession} via {@code @ConditionalOnMissingBean(name = "cassandraSession")}.
 *
 * <p>Runs before {@link CassandraAutoConfiguration} so this {@code @Primary} replacement is ordered
 * ahead of default Cassandra session creation. The {@link #observableCqlSession} bean post-processes
 * the existing {@code CqlSession} bean (Spring resolves it after the session exists).
 *
 * <p>Registered in {@code META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports}
 * (not pulled in only by {@link com.nvidia.boot.observability.ObservabilityAutoConfiguration}) because
 * Spring Data Cassandra is an optional dependency.
 */
@Configuration
@AutoConfigureBefore(CassandraAutoConfiguration.class)
public class CassandraTracingAutoConfiguration {

    /**
     * Registers {@link ObservationRequestTracker} on Spring Boot's shared
     * {@link com.datastax.oss.driver.api.core.CqlSessionBuilder} for the default {@link CqlSession}
     * path. Omitted when {@code nv-boot-starter-cassandra} already declared the same bean.
     */
    @Bean
    @ConditionalOnClass(name = "org.springframework.data.cassandra.observability.ObservationRequestTracker")
    @ConditionalOnMissingBean(name = "cassandraObservationRequestTrackerCustomizer")
    public CqlSessionBuilderCustomizer cassandraObservationRequestTrackerCustomizer() {
        return builder -> builder.addRequestTracker(ObservationRequestTracker.INSTANCE);
    }

    /**
     * Returns a {@code @Primary} {@link CqlSession} that wraps Boot's default session with
     * {@link ObservableCqlSessionFactory} when no bean named {@code cassandraSession} exists.
     * <p>
     * For the {@code cassandraSession} / SSL-bundle path, {@code nv-boot-starter-cassandra} owns
     * both {@link ObservationRequestTracker} registration and wrapping; this method does not run
     * in that case.
     * <p>
     * Apps that want to instrument observability manually (e.g. {@code ObservableReactiveSessionFactoryBean}
     * is mutually exclusive) can create a {@link CqlSessionObservabilityProperties} bean with
     * {@code enabled=false} to opt out and avoid duplicate or orphan spans.
     */
    @Bean
    @Primary
    @ConditionalOnBean({CqlSession.class, ObservationRegistry.class})
    @ConditionalOnMissingBean(name = "cassandraSession")
    public CqlSession observableCqlSession(
            ObjectProvider<CqlSession> sessionProvider,
            ObservationRegistry observationRegistry,
            ObjectProvider<CqlSessionObservabilityProperties> observationPropertiesProvider) {
        var session = sessionProvider.getObject();
        var observationProperties = observationPropertiesProvider.getIfAvailable();
        var shouldWrap = observationProperties == null || observationProperties.isEnabled();

        return shouldWrap ? ObservableCqlSessionFactory.wrap(session, observationRegistry) : session;
    }
}
