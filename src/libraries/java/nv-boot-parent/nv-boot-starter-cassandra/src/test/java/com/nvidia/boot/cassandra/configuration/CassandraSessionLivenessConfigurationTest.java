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

import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.METADATA_SCHEMA_ENABLED;
import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.REQUEST_TIMEOUT;
import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.RETRY_POLICY_CLASS;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import com.datastax.oss.driver.api.core.config.DriverConfig;
import com.datastax.oss.driver.api.core.config.DriverConfigLoader;
import com.datastax.oss.driver.api.core.config.DriverExecutionProfile;
import com.datastax.oss.driver.api.core.config.OptionsMap;
import com.datastax.oss.driver.api.core.config.TypedDriverOption;
import com.datastax.oss.driver.api.core.context.DriverContext;
import com.datastax.oss.driver.internal.core.context.InternalDriverContext;
import com.nvidia.boot.cassandra.retry.NextHostRetryPolicy;
import io.micrometer.core.instrument.Metrics;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.time.Duration;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.boot.health.contributor.Status;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;

class CassandraSessionLivenessConfigurationTest {

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withUserConfiguration(CassandraSessionLivenessConfiguration.class)
            .withPropertyValues("app.cassandra.session-liveness.check-interval=1h");

    @Test
    void registersCassandraSessionContributor() {
        contextRunner.withBean(CqlSession.class, CassandraSessionLivenessConfigurationTest::session)
                .run(context -> {
                    // The bean name maps to the "cassandraSession" contributor in health groups.
                    assertThat(context).hasBean("cassandraSessionHealthIndicator");
                    var indicator = context.getBean(CassandraSessionLivenessIndicator.class);
                    assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);
                });
    }

    @Test
    void reportsUpWithoutAUniqueSession() {
        contextRunner.run(context -> {
            var indicator = context.getBean(CassandraSessionLivenessIndicator.class);
            assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);
        });
    }

    @Test
    void canBeDisabled() {
        contextRunner.withPropertyValues("app.cassandra.session-liveness.enabled=false")
                .withBean(CqlSession.class, CassandraSessionLivenessConfigurationTest::session)
                .run(context -> assertThat(context)
                        .doesNotHaveBean(CassandraSessionLivenessIndicator.class));
    }

    @Test
    @SuppressWarnings("unchecked")
    void freshSessionUsesPrivateMetricRegistry() {
        var builder = mock(CqlSessionBuilder.class);
        when(builder.withMetricRegistry(any())).thenReturn(builder);
        when(builder.build()).thenReturn(mock(CqlSession.class));
        var builders = (ObjectProvider<CqlSessionBuilder>) mock(ObjectProvider.class);
        when(builders.getIfAvailable()).thenReturn(builder);

        assertThat(CassandraSessionLivenessConfiguration.freshSessionSucceeds(
                builders, mock(ObjectProvider.class))).isTrue();

        var registry = ArgumentCaptor.forClass(Object.class);
        verify(builder).withMetricRegistry(registry.capture());
        assertThat(registry.getValue())
                .isInstanceOf(SimpleMeterRegistry.class)
                .isNotSameAs(Metrics.globalRegistry);
    }

    @Test
    @SuppressWarnings("unchecked")
    void freshSessionSkipsSchemaMetadataAndKeepsApplicationSettings() {
        var builder = mock(CqlSessionBuilder.class);
        when(builder.withMetricRegistry(any())).thenReturn(builder);
        when(builder.build()).thenReturn(mock(CqlSession.class));
        var builders = (ObjectProvider<CqlSessionBuilder>) mock(ObjectProvider.class);
        when(builders.getIfAvailable()).thenReturn(builder);
        var configLoaders = (ObjectProvider<DriverConfigLoader>) mock(ObjectProvider.class);
        when(configLoaders.getIfUnique()).thenReturn(applicationLoader());

        assertThat(CassandraSessionLivenessConfiguration.freshSessionSucceeds(
                builders, configLoaders)).isTrue();

        var loader = ArgumentCaptor.forClass(DriverConfigLoader.class);
        verify(builder).withConfigLoader(loader.capture());
        var profile = loader.getValue().getInitialConfig().getDefaultProfile();
        assertThat(profile.getBoolean(METADATA_SCHEMA_ENABLED)).isFalse();
        assertThat(profile.getDuration(REQUEST_TIMEOUT)).isEqualTo(Duration.ofSeconds(7));
    }

    @Test
    void probeLoaderLeavesApplicationLoaderAlone() {
        var applicationLoader = mock(DriverConfigLoader.class);
        when(applicationLoader.getInitialConfig())
                .thenReturn(applicationLoader().getInitialConfig());

        var probeLoader = CassandraSessionLivenessConfiguration.probeConfigLoader(applicationLoader);
        probeLoader.getInitialConfig();
        probeLoader.onDriverInit(mock(InternalDriverContext.class));
        probeLoader.close();

        verify(applicationLoader, never()).onDriverInit(any());
        verify(applicationLoader, never()).close();
    }

    private static DriverConfigLoader applicationLoader() {
        var options = OptionsMap.driverDefaults();
        options.put(TypedDriverOption.REQUEST_TIMEOUT, Duration.ofSeconds(7));
        return DriverConfigLoader.fromMap(options);
    }

    private static CqlSession session() {
        var session = mock(CqlSession.class);
        var context = mock(DriverContext.class);
        var config = mock(DriverConfig.class);
        var profile = mock(DriverExecutionProfile.class);
        when(session.getContext()).thenReturn(context);
        when(context.getConfig()).thenReturn(config);
        when(config.getDefaultProfile()).thenReturn(profile);
        when(profile.withClass(eq(RETRY_POLICY_CLASS), eq(NextHostRetryPolicy.class)))
                .thenReturn(profile);
        return session;
    }
}
