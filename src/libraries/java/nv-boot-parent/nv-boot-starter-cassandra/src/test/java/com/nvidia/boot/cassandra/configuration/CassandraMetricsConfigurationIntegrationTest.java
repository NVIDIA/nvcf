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

import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.METRICS_FACTORY_CLASS;
import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.METRICS_ID_GENERATOR_CLASS;
import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.METRICS_NODE_ENABLED;
import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.METRICS_SESSION_ENABLED;
import static org.assertj.core.api.Assertions.assertThat;

import com.datastax.oss.driver.api.core.config.DriverConfigLoader;
import java.util.Collections;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.boot.cassandra.autoconfigure.DriverConfigLoaderBuilderCustomizer;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.core.env.MapPropertySource;

class CassandraMetricsConfigurationIntegrationTest {

    private final ApplicationContextRunner runner = new ApplicationContextRunner()
            .withUserConfiguration(CassandraMetricsConfiguration.class);

    @Test
    void doesNotRegisterCustomizerWhenNoMetricsConfigured() {
        runner.run(context -> assertThat(context.getBeansOfType(DriverConfigLoaderBuilderCustomizer.class))
                .isEmpty());
    }

    @Test
    void doesNotRegisterCustomizerWhenSessionMetricsExplicitlyEmpty() {
        runner.withInitializer(applicationContext -> {
            var env = applicationContext.getEnvironment();
            env.getPropertySources()
                    .addFirst(new MapPropertySource(
                            "empty-session-metrics",
                            Map.of("spring.cassandra.session-metrics", Collections.<String>emptyList())));
        })
        .run(context -> assertThat(context.getBeansOfType(DriverConfigLoaderBuilderCustomizer.class))
                        .isEmpty());
    }

    @Test
    void registersCustomizerAndAppliesDriverOptionsWhenSessionMetricsSet() {
        runner.withPropertyValues("spring.cassandra.session-metrics[0]=bytes-sent")
                .run(context -> {
                    assertThat(context.getBeansOfType(DriverConfigLoaderBuilderCustomizer.class))
                            .hasSize(1);
                    var customizer = context.getBean(DriverConfigLoaderBuilderCustomizer.class);
                    var builder = DriverConfigLoader.programmaticBuilder();
                    customizer.customize(builder);
                    var profile = builder.build().getInitialConfig().getDefaultProfile();
                    assertThat(profile.getString(METRICS_FACTORY_CLASS))
                            .isEqualTo("com.datastax.oss.driver.internal.metrics.micrometer.MicrometerMetricsFactory");
                    assertThat(profile.getString(METRICS_ID_GENERATOR_CLASS))
                            .isEqualTo("com.datastax.oss.driver.internal.core.metrics.TaggingMetricIdGenerator");
                    assertThat(profile.getStringList(METRICS_SESSION_ENABLED))
                            .containsExactly("bytes-sent");
                });
    }

    @Test
    void registersCustomizerWhenOnlyNodeMetricsSet() {
        runner.withPropertyValues("spring.cassandra.node-metrics[0]=pool.in-flight")
                .run(context -> {
                    assertThat(context.getBeansOfType(DriverConfigLoaderBuilderCustomizer.class))
                            .hasSize(1);
                    var customizer = context.getBean(DriverConfigLoaderBuilderCustomizer.class);
                    var builder = DriverConfigLoader.programmaticBuilder();
                    customizer.customize(builder);
                    var profile = builder.build().getInitialConfig().getDefaultProfile();
                    assertThat(profile.getStringList(METRICS_NODE_ENABLED))
                            .containsExactly("pool.in-flight");
                    assertThat(profile.getStringList(METRICS_SESSION_ENABLED)).isEmpty();
                });
    }

    @Test
    void registersCustomizerWithBothSessionAndNodeLists() {
        runner.withPropertyValues(
                        "spring.cassandra.session-metrics[0]=connected-nodes",
                        "spring.cassandra.session-metrics[1]=cql-requests",
                        "spring.cassandra.node-metrics[0]=pool.open-connections")
                .run(context -> {
                    assertThat(context.getBeansOfType(DriverConfigLoaderBuilderCustomizer.class))
                            .hasSize(1);
                    var customizer =
                            context.getBean(DriverConfigLoaderBuilderCustomizer.class);
                    var builder = DriverConfigLoader.programmaticBuilder();
                    customizer.customize(builder);
                    var profile = builder.build().getInitialConfig().getDefaultProfile();
                    assertThat(profile.getStringList(METRICS_SESSION_ENABLED))
                            .containsExactly("connected-nodes", "cql-requests");
                    assertThat(profile.getStringList(METRICS_NODE_ENABLED))
                            .containsExactly("pool.open-connections");
                });
    }
}
