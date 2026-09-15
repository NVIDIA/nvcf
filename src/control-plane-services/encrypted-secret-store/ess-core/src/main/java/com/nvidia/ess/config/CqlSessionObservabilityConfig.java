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
package com.nvidia.ess.config;

import com.datastax.oss.driver.api.core.CqlSession;
import com.nvidia.boot.cassandra.configuration.RefreshingCqlSessionObservabilityProperties;
import com.nvidia.ess.metrics.CustomCassandraObservationConvention;
import io.micrometer.core.instrument.config.MeterFilter;
import io.micrometer.observation.ObservationRegistry;
import java.util.Set;
import org.springframework.boot.cassandra.autoconfigure.CqlSessionBuilderCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.data.cassandra.ReactiveSession;
import org.springframework.data.cassandra.observability.ObservableReactiveSessionFactoryBean;
import org.springframework.data.cassandra.observability.ObservationRequestTracker;

/**
 * Enables Cassandra CQL observation via {@link ObservableReactiveSessionFactoryBean},
 * replacing the default {@code DefaultBridgedReactiveSession} with an observation-aware one.
 *
 * @see <a href="https://docs.spring.io/spring-data/cassandra/reference/observability.html">
 *     Spring Data Cassandra Observability</a>
 */
@Configuration
public class CqlSessionObservabilityConfig {


    /** Required for {@link ObservableReactiveSessionFactoryBean} **/
    @Bean
    CqlSessionBuilderCustomizer observationRequestTrackerCustomizer() {
        return builder -> builder.addRequestTracker(ObservationRequestTracker.INSTANCE);
    }

    /**
     * Observation-aware {@link ReactiveSession};
     * <p>Needed to get Observations on the Cql queries
     */
    @Bean
    ObservableReactiveSessionFactoryBean observableReactiveSession(
            CqlSession cqlSession, ObservationRegistry observationRegistry) {
        var factory = new ObservableReactiveSessionFactoryBean(cqlSession, observationRegistry);
        factory.setConvention(
                new CustomCassandraObservationConvention(cqlSession.getContext().getConfig()));
        return factory;
    }

    /**
     * {@link ObservableReactiveSessionFactoryBean} Observations creates metrics {@code execute_*}/{@code prepare_*}.
     * Not useful (only query INSERT/SELECT + prepare/execute tags) beyond Spring data metrics
     */
    @Bean
    MeterFilter suppressCassandraObservationMetrics() {
        Set<String> suppressed = Set.of(
                "execute", "execute.active", "execute.cassandra.node.success",
                "prepare", "prepare.active");
        return MeterFilter.deny(id -> suppressed.contains(id.getName()));
    }

    @Bean
    RefreshingCqlSessionObservabilityProperties refreshingCqlSessionObservabilityProperties() {
        var properties = new RefreshingCqlSessionObservabilityProperties();
        properties.setEnabled(false);
        return properties;
    }
}
