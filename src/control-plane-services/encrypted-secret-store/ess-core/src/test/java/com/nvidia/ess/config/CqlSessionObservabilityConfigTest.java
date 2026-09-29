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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import com.datastax.oss.driver.api.core.config.DriverConfig;
import com.datastax.oss.driver.api.core.context.DriverContext;
import io.micrometer.core.instrument.Meter.Id;
import io.micrometer.core.instrument.Meter.Type;
import io.micrometer.core.instrument.Tags;
import io.micrometer.core.instrument.config.MeterFilter;
import io.micrometer.core.instrument.config.MeterFilterReply;
import io.micrometer.observation.ObservationRegistry;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.data.cassandra.observability.ObservableReactiveSessionFactoryBean;
import org.springframework.data.cassandra.observability.ObservationRequestTracker;

@ExtendWith(MockitoExtension.class)
class CqlSessionObservabilityConfigTest {

    @Mock
    private CqlSession cqlSession;

    @Mock
    private ObservationRegistry observationRegistry;

    private final CqlSessionObservabilityConfig config = new CqlSessionObservabilityConfig();

    @Test
    void observationRequestTrackerCustomizer_shouldAddObservationRequestTracker() {
        var customizer = config.observationRequestTrackerCustomizer();
        CqlSessionBuilder builder = mock(CqlSessionBuilder.class);

        customizer.customize(builder);

        verify(builder).addRequestTracker(ObservationRequestTracker.INSTANCE);
    }

    @Test
    void observableReactiveSession_shouldReturnFactoryBean() {
        DriverContext context = mock(DriverContext.class);
        DriverConfig driverConfig = mock(DriverConfig.class);
        when(cqlSession.getContext()).thenReturn(context);
        when(context.getConfig()).thenReturn(driverConfig);

        ObservableReactiveSessionFactoryBean factory =
                config.observableReactiveSession(cqlSession, observationRegistry);

        assertThat(factory).isNotNull();
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "execute", "execute.active", "execute.cassandra.node.success",
            "prepare", "prepare.active"
    })
    void suppressCassandraObservationMetrics_shouldDenySuppressedMetrics(String metricName) {
        MeterFilter filter = config.suppressCassandraObservationMetrics();
        Id id = new Id(metricName, Tags.empty(), null, null, Type.COUNTER);

        MeterFilterReply reply = filter.accept(id);

        assertThat(reply).isEqualTo(MeterFilterReply.DENY);
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "http.server.requests", "jvm.memory.used", "cassandra.cql-requests",
            "execute.other", "prepared"
    })
    void suppressCassandraObservationMetrics_shouldAllowOtherMetrics(String metricName) {
        MeterFilter filter = config.suppressCassandraObservationMetrics();
        Id id = new Id(metricName, Tags.empty(), null, null, Type.COUNTER);

        MeterFilterReply reply = filter.accept(id);

        assertThat(reply).isEqualTo(MeterFilterReply.NEUTRAL);
    }
}
