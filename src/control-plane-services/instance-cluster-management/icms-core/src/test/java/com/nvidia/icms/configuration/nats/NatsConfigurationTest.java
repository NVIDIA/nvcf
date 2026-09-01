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
package com.nvidia.icms.configuration.nats;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.icms.configuration.nats.NatsConfiguration.NatsMetricsConfiguration;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import io.nats.client.Connection;
import io.nats.client.JetStream;
import io.nats.client.JetStreamManagement;
import io.nats.client.Options;
import io.nats.client.Statistics;
import java.time.Duration;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.springframework.context.ApplicationContext;

class NatsConfigurationTest {

    private static final Duration CONNECTION_TIMEOUT = Duration.ofSeconds(5);
    private static final Duration PING_INTERVAL = Duration.ofSeconds(5);
    private static final Duration RECONNECT_WAIT = Duration.ofMillis(100);

    @Test
    void createDefaultOptions_matchesCloudFunctionsReconnectBehavior() {
        var properties = mock(NatsConfigurationProperties.class);
        when(properties.getNatsUrl()).thenReturn("nats://localhost:4222");
        when(properties.getConnectionTimeout()).thenReturn(CONNECTION_TIMEOUT);
        when(properties.getNkeySeed()).thenReturn(Optional.empty());

        var options = new NatsConfiguration().createDefaultOptions(properties);

        assertEquals(CONNECTION_TIMEOUT, options.getConnectionTimeout());
        assertEquals(PING_INTERVAL, options.getPingInterval());
        assertEquals(RECONNECT_WAIT, options.getReconnectWait());
        assertEquals(Options.DEFAULT_MAX_RECONNECT, options.getMaxReconnect());
    }

    @Test
    void fixedNatsPool_borrowsResourcesRoundRobin() throws Exception {
        var properties = enabledProperties(2);
        var applicationContext = mock(ApplicationContext.class);
        var first = connection();
        var second = connection();
        when(applicationContext.getBean(Connection.class)).thenReturn(first.connection(),
                                                                       second.connection());

        try (var pool = new FixedNatsPool(applicationContext, properties)) {
            assertSame(first.connection(), pool.borrowConnection());
            assertSame(second.jetStream(), pool.borrowJetStream());
            assertSame(first.management(), pool.borrowJetStreamManagement());
            assertTrue(pool.healthy());
        }

        verify(applicationContext, times(2)).getBean(Connection.class);
        verify(first.connection()).RTT();
        verify(second.connection()).RTT();
        verify(first.connection()).close();
        verify(second.connection()).close();
    }

    @Test
    void fixedNatsPool_connectsWhenConstructed() throws Exception {
        var applicationContext = mock(ApplicationContext.class);
        var resources = connection();
        when(applicationContext.getBean(Connection.class)).thenReturn(resources.connection());

        try (var pool = new FixedNatsPool(applicationContext, enabledProperties(1))) {
        }

        verify(applicationContext).getBean(Connection.class);
        verify(resources.connection()).RTT();
    }

    @Test
    void fixedNatsPool_healthCheckUsesInitializedConnection() throws Exception {
        var applicationContext = mock(ApplicationContext.class);
        var resources = connection();
        when(applicationContext.getBean(Connection.class)).thenReturn(resources.connection());

        try (var pool = new FixedNatsPool(applicationContext, enabledProperties(1))) {
            assertTrue(pool.healthy());
        }

        verify(applicationContext).getBean(Connection.class);
    }

    @Test
    void fixedNatsPool_returnsClosedConnectionWithoutReplacingIt() throws Exception {
        var properties = enabledProperties(1);
        var applicationContext = mock(ApplicationContext.class);
        var closed = connection();
        when(closed.connection().getStatus()).thenReturn(Connection.Status.CLOSED);
        when(applicationContext.getBean(Connection.class)).thenReturn(closed.connection());

        try (var pool = new FixedNatsPool(applicationContext, properties)) {
            assertSame(closed.connection(), pool.borrowConnection());
            assertSame(closed.connection(), pool.borrowConnection());
        }

        verify(applicationContext).getBean(Connection.class);
        verify(closed.connection()).close();
    }

    @Test
    void fixedNatsPool_isUnhealthyWhenAnyConnectionIsDisconnected() throws Exception {
        var properties = enabledProperties(2);
        var applicationContext = mock(ApplicationContext.class);
        var first = connection();
        var second = connection();
        when(second.connection().getStatus()).thenReturn(Connection.Status.RECONNECTING);
        when(applicationContext.getBean(Connection.class)).thenReturn(first.connection(),
                                                                       second.connection());

        try (var pool = new FixedNatsPool(applicationContext, properties)) {
            assertSame(first.connection(), pool.borrowConnection());
            assertSame(second.connection(), pool.borrowConnection());
            assertFalse(pool.healthy());
        }

        verify(applicationContext, times(2)).getBean(Connection.class);
    }

    @Test
    void natsMetricsConfiguration_registersCloudFunctionsMetrics() throws Exception {
        var properties = enabledProperties(1);
        var applicationContext = mock(ApplicationContext.class);
        var resources = connection();
        var statistics = mock(Statistics.class);
        when(resources.connection().getStatistics()).thenReturn(statistics);
        when(statistics.getPings()).thenReturn(3L);
        when(statistics.getOutstandingRequests()).thenReturn(2L);
        when(applicationContext.getBean(Connection.class)).thenReturn(resources.connection());

        try (var pool = new FixedNatsPool(applicationContext, properties)) {
            var registry = new SimpleMeterRegistry();
            new NatsMetricsConfiguration(pool, registry).afterPropertiesSet();

            assertEquals(3.0, registry.get("nats.pings").functionCounter().count());
            assertEquals(2.0, registry.get("nats.requests.outstanding").gauge().value());
            assertEquals(16, registry.getMeters().size());
        }
    }

    private static NatsConfigurationProperties enabledProperties(int poolSize) {
        var properties = mock(NatsConfigurationProperties.class);
        when(properties.isEnabled()).thenReturn(true);
        when(properties.getMaxPoolSize()).thenReturn(poolSize);
        return properties;
    }

    private static ConnectionResources connection() throws Exception {
        var connection = mock(Connection.class);
        var jetStream = mock(JetStream.class);
        var management = mock(JetStreamManagement.class);
        when(connection.jetStream()).thenReturn(jetStream);
        when(connection.jetStreamManagement()).thenReturn(management);
        when(connection.getStatus()).thenReturn(Connection.Status.CONNECTED);
        return new ConnectionResources(connection, jetStream, management);
    }

    private record ConnectionResources(
            Connection connection, JetStream jetStream, JetStreamManagement management) {
    }
}
