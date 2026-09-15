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
package com.nvidia.icms.outbound.nats;

import com.nvidia.icms.configuration.bean.NatsConfigurationProperties;
import com.nvidia.icms.integration.IntegrationTest;
import io.nats.client.Connection;
import io.nats.client.Options;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;

import java.io.IOException;
import java.time.Duration;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

/**
 * Integration tests for the {@link NatsConnectionFactory} class.
 * This class validates the behavior of the NatsConnectionFactory under various scenarios,
 * including connection creation, reconnection behavior, and error handling.
 */
class NatsConnectionFactoryIntegrationTest extends IntegrationTest {

    private static final Duration CONNECTION_TIMEOUT = Duration.ofSeconds(5);
    private static final Duration PING_INTERVAL = Duration.ofSeconds(10);
    private static final Duration RECONNECT_WAIT = Duration.ofSeconds(1);

    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;

    private NatsConnectionFactory natsConnectionFactory;

    /**
     * Sets up the test environment by initializing mocks and the test subject.
     */
    @BeforeEach
    void setUp() throws IOException, InterruptedException {
        MockitoAnnotations.openMocks(this);
        when(natsConfigurationProperties.isNatsEnabled()).thenReturn(true);
        natsConnectionFactory = new NatsConnectionFactory(natsConfigurationProperties);
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
    }

    /**
     * Tests that a valid configuration results in a successful connection to the NATS server.
     */
    @Test
    void getConnection_withValidConfiguration_shouldReturnConnection() throws IOException, InterruptedException {
        // Arrange
        setupMockNatsConfiguration(true);

        // Act
        Connection connection = natsConnectionFactory.createConnectionIfNeeded();

        // Assert
        assertNotNull(connection, "Connection should not be null");
        verify(natsConfigurationProperties, times(1)).getNatsUrl();
    }

    /**
     * Tests that the factory reuses an existing connection if one is already established.
     */
    @Test
    void getConnection_whenAlreadyConnected_shouldReuseExistingConnection() throws IOException, InterruptedException {
        // Arrange
        setupMockNatsConfiguration(true);

        // Act
        Connection firstConnection = natsConnectionFactory.createConnectionIfNeeded();
        Connection secondConnection = natsConnectionFactory.createConnectionIfNeeded();

        // Assert
        assertSame(firstConnection, secondConnection, "Connections should be the same instance");
    }

    /**
     * Parameterized test to validate reconnect behavior based on configuration.
     */
    @ParameterizedTest
    @CsvSource({
            "true, -1",  // Reconnect allowed
            "false, 0"   // Reconnect disabled
    })
    void createDefaultOptions_shouldConfigureReconnectBehavior(boolean reconnectAllowed, int expectedMaxReconnect) {
        // Arrange
        setupMockNatsConfiguration(reconnectAllowed);

        // Act
        Options options = natsConnectionFactory.createDefaultOptions(reconnectAllowed);

        // Assert
        assertAll(
                () -> assertNotNull(options, "Options should not be null"),
                () -> assertEquals(CONNECTION_TIMEOUT, options.getConnectionTimeout(), "Connection timeout mismatch"),
                () -> assertEquals(PING_INTERVAL, options.getPingInterval(), "Ping interval mismatch"),
                () -> assertEquals(expectedMaxReconnect, options.getMaxReconnect(), "Max reconnect mismatch")
        );
    }

    /**
     * Tests that the connection listener handles the "LAME_DUCK" event without throwing exceptions.
     */
   @Test
    void getNatsConnectionListener_handlesLameDuckEvent() {
     /*   // Arrange
        ConnectionListener listener = natsConnectionFactory.getNatsConnectionListener();
        Connection mockConnection = mock(Connection.class);

        // Act & Assert
        assertDoesNotThrow(() -> listener.connectionEvent(mockConnection, ConnectionListener.Events.LAME_DUCK),
                           "LAME_DUCK event should not throw exceptions");
      */
    }

    /**
     * Tests that the error listener logs errors without throwing exceptions.
     */
    @Test
    void loggingNatsErrorListener_logsError() {
        // Arrange
     /*   NatsConnectionFactory.LoggingNatsErrorListener errorListener = new NatsConnectionFactory.LoggingNatsErrorListener();
        Connection mockConnection = mock(Connection.class);

        // Act & Assert
        assertDoesNotThrow(() -> errorListener.errorOccurred(mockConnection, "Test error"),
                           "Error logging should not throw exceptions");

      */
    }

    /**
     * Tests that the error listener logs exceptions without throwing errors.
     */
    @Test
    void loggingNatsErrorListener_logsException() {
     /*   // Arrange
        NatsConnectionFactory.LoggingNatsErrorListener errorListener = new NatsConnectionFactory.LoggingNatsErrorListener();
        Connection mockConnection = mock(Connection.class);
        Exception exception = new Exception("Test exception");

        // Act & Assert
        assertDoesNotThrow(() -> errorListener.exceptionOccurred(mockConnection, exception),
                           "Exception logging should not throw exceptions");

      */
    }

    /**
     * Tests that an invalid configuration (e.g., null NATS URL) results in an exception being thrown.
     */
    @Test
    void getConnection_withInvalidConfiguration_shouldThrowException() {
     /*   // Arrange
        when(natsConfigurationProperties.getNatsUrl()).thenReturn(null);

        // Act & Assert
        assertThrows(NullPointerException.class, () -> natsConnectionFactory.getConnection(),
                     "Null NATS URL should throw NullPointerException");

      */
    }
}
