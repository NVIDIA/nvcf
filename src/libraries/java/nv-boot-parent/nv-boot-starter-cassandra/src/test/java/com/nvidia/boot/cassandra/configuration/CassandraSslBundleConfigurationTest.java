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
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import io.micrometer.observation.ObservationRegistry;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.aop.TargetSource;
import org.springframework.boot.ssl.SslBundles;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;

class CassandraSslBundleConfigurationTest {

    private static final String BUNDLE_NAME = "cassandra-ssl";

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withUserConfiguration(CassandraSslBundleConfiguration.class)
            .withPropertyValues("spring.cassandra.ssl.bundle=" + BUNDLE_NAME)
            .withBean(SslBundles.class, () -> mock(SslBundles.class))
            .withBean(CqlSessionBuilder.class, this::createMockBuilder);

    private final ApplicationContextRunner sslDisabledWithoutBundleRunner = new ApplicationContextRunner()
            .withUserConfiguration(CassandraSslBundleConfiguration.class)
            .withPropertyValues("spring.cassandra.ssl.enabled=false")
            .withBean(SslBundles.class, () -> mock(SslBundles.class))
            .withBean(CqlSessionBuilder.class, this::createMockBuilder);

    private CqlSessionBuilder createMockBuilder() {
        var builder = mock(CqlSessionBuilder.class);
        var initialSession = mock(CqlSession.class);
        lenient().when(builder.build()).thenReturn(initialSession);
        return builder;
    }

    @Test
    @DisplayName("Loads cassandraSession when spring.cassandra.ssl.enabled=false without spring.cassandra.ssl.bundle")
    void loadsWhenSslDisabledWithoutBundleProperty() {
        sslDisabledWithoutBundleRunner.run(context -> assertThat(context).hasBean("cassandraSession"));
    }

    @Test
    @DisplayName("Wraps CqlSession with observability by default when ObservationRegistry is present and no RefreshingCqlSessionObservabilityProperties bean is declared")
    void wrapsByDefault() {
        contextRunner
                .withBean(ObservationRegistry.class, ObservationRegistry::create)
                .run(context -> {
                    assertThat(context).hasBean("cassandraSession");
                    var refreshing = context.getBean(
                            "cassandraSession",
                            CassandraSslBundleConfiguration.RefreshingCqlSession.class);
                    assertThat(refreshing.getDelegate())
                            .as("Default: observation wrap on, delegate should be the "
                                    + "ObservableCqlSessionFactory AOP proxy (a TargetSource)")
                            .isInstanceOf(TargetSource.class);
                });
    }

    @Test
    @DisplayName("Wraps CqlSession when RefreshingCqlSessionObservabilityProperties bean is declared with enabled=true")
    void wrapsWhenPropertiesBeanEnabledTrue() {
        contextRunner
                .withBean(ObservationRegistry.class, ObservationRegistry::create)
                .withBean(
                        RefreshingCqlSessionObservabilityProperties.class,
                        CassandraSslBundleConfigurationTest::enabledTrueProperties)
                .run(context -> {
                    assertThat(context).hasBean("cassandraSession");
                    var refreshing = context.getBean(
                            "cassandraSession",
                            CassandraSslBundleConfiguration.RefreshingCqlSession.class);
                    assertThat(refreshing.getDelegate())
                            .as("enabled=true: delegate should be the AOP-proxied observable wrap")
                            .isInstanceOf(TargetSource.class);
                });
    }

    @Test
    @DisplayName("Returns unwrapped CqlSession when RefreshingCqlSessionObservabilityProperties bean has enabled=false, even though ObservationRegistry is present")
    void skipsWrapWhenPropertiesBeanEnabledFalse() {
        contextRunner
                .withBean(ObservationRegistry.class, ObservationRegistry::create)
                .withBean(
                        RefreshingCqlSessionObservabilityProperties.class,
                        CassandraSslBundleConfigurationTest::enabledFalseProperties)
                .run(context -> {
                    assertThat(context).hasBean("cassandraSession");
                    var refreshing = context.getBean(
                            "cassandraSession",
                            CassandraSslBundleConfiguration.RefreshingCqlSession.class);
                    assertThat(refreshing.getDelegate())
                            .as("Opt-out: delegate should be the raw CqlSession "
                                    + "(not an AOP-proxied observable wrap)")
                            .isNotInstanceOf(TargetSource.class);
                });
    }

    @Test
    @DisplayName("Returns unwrapped CqlSession when no ObservationRegistry is present")
    void doesNotWrapWithoutObservationRegistry() {
        contextRunner.run(context -> {
            assertThat(context).hasBean("cassandraSession");
            var refreshing = context.getBean(
                    "cassandraSession",
                    CassandraSslBundleConfiguration.RefreshingCqlSession.class);
            assertThat(refreshing.getDelegate())
                    .as("No ObservationRegistry: delegate should be the raw CqlSession")
                    .isNotInstanceOf(TargetSource.class);
        });
    }

    private static RefreshingCqlSessionObservabilityProperties enabledTrueProperties() {
        var props = new RefreshingCqlSessionObservabilityProperties();
        props.setEnabled(true);
        return props;
    }

    private static RefreshingCqlSessionObservabilityProperties enabledFalseProperties() {
        var props = new RefreshingCqlSessionObservabilityProperties();
        props.setEnabled(false);
        return props;
    }
}
