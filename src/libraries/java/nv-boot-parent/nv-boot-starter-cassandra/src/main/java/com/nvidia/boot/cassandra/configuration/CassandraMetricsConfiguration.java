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

import java.util.ArrayList;
import java.util.List;
import lombok.Data;
import org.jspecify.annotations.NonNull;
import org.springframework.boot.cassandra.autoconfigure.DriverConfigLoaderBuilderCustomizer;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.boot.context.properties.bind.BindResult;
import org.springframework.boot.context.properties.bind.Bindable;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Condition;
import org.springframework.context.annotation.ConditionContext;
import org.springframework.context.annotation.Conditional;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.type.AnnotatedTypeMetadata;

// Nested types must be referenced with an enclosing name in annotations. This is required
// by javac forward-reference rules.
@Conditional(CassandraMetricsConfiguration.CassandraMetricsEnabledCondition.class)
@EnableConfigurationProperties(CassandraMetricsConfiguration.CassandraMetricsProperties.class)
@Configuration
public class CassandraMetricsConfiguration {

    @Bean
    DriverConfigLoaderBuilderCustomizer configLoaderBuilderCustomizer(
            CassandraMetricsProperties cassandraProperties) {
        return builder -> {
            builder.withString(
                    METRICS_FACTORY_CLASS,
                    "com.datastax.oss.driver.internal.metrics.micrometer.MicrometerMetricsFactory");
            // Use tagging id generator so host:port is a Micrometer tag (node=...) instead of part
            // of the meter name. DefaultMetricIdGenerator embeds ":" in names
            // (e.g. ...nodes.10_0_0_1:9042...), which OpenTelemetry rejects as an instrument name
            // when Micrometer is bridged to OTel.
            builder.withString(
                    METRICS_ID_GENERATOR_CLASS,
                    "com.datastax.oss.driver.internal.core.metrics.TaggingMetricIdGenerator");
            builder.withStringList(METRICS_SESSION_ENABLED,
                                   cassandraProperties.getSessionMetrics());
            builder.withStringList(METRICS_NODE_ENABLED,
                                   cassandraProperties.getNodeMetrics());
        };
    }

    /**
     * Binds optional {@code spring.cassandra.session-metrics} and
     * {@code spring.cassandra.node-metrics}. Micrometer driver metrics are configured only when
     * at least one of these lists is non-empty; if a key is present with an empty list, the
     * metrics customizer is not applied.
     */
    @ConfigurationProperties(prefix = "spring.cassandra")
    @Data
    public static class CassandraMetricsProperties {

        private List<String> sessionMetrics = new ArrayList<>();

        private List<String> nodeMetrics = new ArrayList<>();
    }

    /**
     * Enables Micrometer Cassandra driver metrics configuration only when the application has set
     * at least one metric name under {@code spring.cassandra.session-metrics} or
     * {@code spring.cassandra.node-metrics}.
     *
     * <p>If a key is omitted, the customizer is not registered. If a key is present with an empty
     * list (e.g. {@code session-metrics: []}), the customizer is also not registered—only non-empty
     * lists opt in.
     */
    public static class CassandraMetricsEnabledCondition implements Condition {

        private static final String SESSION_METRICS_KEY = "spring.cassandra.session-metrics";
        private static final String NODE_METRICS_KEY = "spring.cassandra.node-metrics";

        @Override
        public boolean matches(
                @NonNull ConditionContext context,
                @NonNull AnnotatedTypeMetadata metadata) {
            var binder = Binder.get(context.getEnvironment());
            return hasNonEmptyMetricList(binder, SESSION_METRICS_KEY)
                    || hasNonEmptyMetricList(binder, NODE_METRICS_KEY);
        }

        /**
         * @return true only when the property is bound and has at least one element (key absent or
         *     empty list yields false).
         */
        private static boolean hasNonEmptyMetricList(Binder binder, String propertyKey) {
            BindResult<List<String>> bound = binder.bind(propertyKey,
                                                         Bindable.listOf(String.class));
            return bound.isBound() && !bound.get().isEmpty();
        }
    }
}
