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

import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nvidia.icms.integration.IntegrationTest;
import io.micrometer.tracing.Tracer;
import io.nats.client.Connection;
import java.io.IOException;
import java.time.Duration;
import java.util.Optional;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

/**
 * Integration tests for {@link NatsConfiguration}.
 */
class NatsConfigurationIntegrationTest extends IntegrationTest {

    private static final Duration CONNECTION_TIMEOUT = Duration.ofSeconds(5);
    private static final Duration PING_INTERVAL = Duration.ofSeconds(10);
    private static final Duration RECONNECT_WAIT = Duration.ofSeconds(1);

    private NatsConfigurationProperties natsConfigurationProperties;

    private NatsConfiguration natsConfiguration;

    /**
     * Sets up the test environment by initializing mocks and the test subject.
     */
    @BeforeEach
    void setUp() {
        natsConfigurationProperties = mock(NatsConfigurationProperties.class);
        natsConfiguration = new NatsConfiguration();
    }

    /**
     * Helper method to set up mock NATS configuration properties.
     */
    private void setupMockNatsConfiguration(boolean reconnectAllowed) {
        when(natsConfigurationProperties.getNatsUrl()).thenReturn(NATS_URL);
        when(natsConfigurationProperties.getConnectionTimeout()).thenReturn(CONNECTION_TIMEOUT);
        when(natsConfigurationProperties.getPingInterval()).thenReturn(PING_INTERVAL);
        when(natsConfigurationProperties.getReconnectWait()).thenReturn(RECONNECT_WAIT);
        when(natsConfigurationProperties.getReconnectJitter()).thenReturn(Duration.ZERO);
        when(natsConfigurationProperties.isReconnectAllowed()).thenReturn(reconnectAllowed);
        when(natsConfigurationProperties.getNkeySeed()).thenReturn(Optional.empty());
    }

    /**
     * Tests that a valid configuration results in a successful connection to the NATS server.
     */
    @Test
    void natsConnection_withValidConfiguration_returnsConnection()
            throws IOException, InterruptedException {
        setupMockNatsConfiguration(true);

        try (Connection connection = natsConfiguration.natsConnection(
                natsConfigurationProperties, mock(Tracer.class))) {
            assertNotNull(connection);
        }
    }

}
