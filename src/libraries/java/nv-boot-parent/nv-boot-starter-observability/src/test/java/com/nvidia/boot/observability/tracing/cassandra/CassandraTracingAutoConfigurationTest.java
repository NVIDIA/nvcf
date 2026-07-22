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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import io.micrometer.observation.ObservationRegistry;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.boot.cassandra.autoconfigure.CqlSessionBuilderCustomizer;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.data.cassandra.observability.ObservationRequestTracker;

class CassandraTracingAutoConfigurationTest {

    private final ApplicationContextRunner runner = new ApplicationContextRunner()
            .withUserConfiguration(CassandraTracingAutoConfiguration.class);

    @Test
    @DisplayName("Registers CqlSessionBuilderCustomizer when ObservationRequestTracker is on classpath")
    void registersCustomizerWhenCassandraOnClasspath() {
        runner.run(context -> {
            assertThat(context).hasSingleBean(CqlSessionBuilderCustomizer.class);

            var customizer = context.getBean(CqlSessionBuilderCustomizer.class);
            var builder = mock(CqlSessionBuilder.class);
            when(builder.addRequestTracker(any())).thenReturn(builder);

            customizer.customize(builder);

            verify(builder).addRequestTracker(ObservationRequestTracker.INSTANCE);
        });
    }

    @Test
    @DisplayName("Wraps CqlSession with ObservableCqlSessionFactory by default (no CqlSessionObservabilityProperties bean declared)")
    void wrapsCqlSessionWithObservabilityByDefault() {
        var rawSession = mock(CqlSession.class);
        var runnerWithSession = new ApplicationContextRunner()
                .withUserConfiguration(CassandraTracingAutoConfiguration.class)
                .withBean(CqlSession.class, () -> rawSession)
                .withBean(ObservationRegistry.class, ObservationRegistry::create);

        runnerWithSession.run(context -> {
            assertThat(context).hasBean("observableCqlSession");
            var session = context.getBean(CqlSession.class);
            assertThat(session).isNotNull();
            assertThat(session)
                    .as("Default: observableCqlSession should be a wrapper, not the raw session")
                    .isNotSameAs(rawSession);
        });
    }

    @Test
    @DisplayName("Wraps CqlSession when CqlSessionObservabilityProperties bean is declared with enabled=true")
    void wrapsWhenPropertiesBeanEnabledTrue() {
        var rawSession = mock(CqlSession.class);
        var runnerWithSession = new ApplicationContextRunner()
                .withUserConfiguration(CassandraTracingAutoConfiguration.class)
                .withBean(CqlSession.class, () -> rawSession)
                .withBean(ObservationRegistry.class, ObservationRegistry::create)
                .withBean(
                        CqlSessionObservabilityProperties.class,
                        CassandraTracingAutoConfigurationTest::enabledTrueProperties);

        runnerWithSession.run(context -> {
            assertThat(context).hasBean("observableCqlSession");
            var session = context.getBean("observableCqlSession", CqlSession.class);
            assertThat(session)
                    .as("enabled=true: observableCqlSession should still be a wrapper")
                    .isNotSameAs(rawSession);
        });
    }

    @Test
    @DisplayName("Returns raw session when CqlSessionObservabilityProperties bean has enabled=false (opt-out)")
    void skipsWrapWhenPropertiesBeanEnabledFalse() {
        var rawSession = mock(CqlSession.class);
        var runnerWithSession = new ApplicationContextRunner()
                .withUserConfiguration(CassandraTracingAutoConfiguration.class)
                .withBean(CqlSession.class, () -> rawSession)
                .withBean(ObservationRegistry.class, ObservationRegistry::create)
                .withBean(
                        CqlSessionObservabilityProperties.class,
                        CassandraTracingAutoConfigurationTest::enabledFalseProperties);

        runnerWithSession.run(context -> {
            assertThat(context).hasBean("observableCqlSession");
            var session = context.getBean("observableCqlSession", CqlSession.class);
            assertThat(session)
                    .as("Opt-out: observableCqlSession should be the raw upstream CqlSession")
                    .isSameAs(rawSession);
        });
    }

    @Test
    @DisplayName("Does not create observableCqlSession when cassandraSession (RefreshingCqlSession) exists")
    void doesNotCreateObservableCqlSessionWhenCassandraSessionExists() {
        var rawSession = mock(CqlSession.class);
        var runnerWithCassandraSession = new ApplicationContextRunner()
                .withUserConfiguration(CassandraTracingAutoConfiguration.class)
                .withBean("cassandraSession", CqlSession.class, () -> rawSession)
                .withBean(ObservationRegistry.class, ObservationRegistry::create);

        runnerWithCassandraSession.run(context -> {
            assertThat(context).doesNotHaveBean("observableCqlSession");
            assertThat(context.getBean(CqlSession.class)).isSameAs(rawSession);
        });
    }

    private static CqlSessionObservabilityProperties enabledTrueProperties() {
        var props = new CqlSessionObservabilityProperties();
        props.setEnabled(true);
        return props;
    }

    private static CqlSessionObservabilityProperties enabledFalseProperties() {
        var props = new CqlSessionObservabilityProperties();
        props.setEnabled(false);
        return props;
    }
}
