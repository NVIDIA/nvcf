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

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nats.NatsConfiguration;
import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocTerminatePodMessageModel;
import io.micrometer.tracing.Tracer;
import io.nats.client.Connection;
import java.time.Duration;
import java.util.Optional;
import lombok.SneakyThrows;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;
import org.springframework.context.ApplicationContext;

/**
 * Integration tests for the {@link NatsMessageSenderClient}.
 * This class verifies the behavior of the NATS message sender client when interacting with a NATS server.
 * It tests various message types and scenarios, including valid and invalid inputs.
 */
class NatsMessageSenderClientIntegrationTest extends IntegrationTest {

    private NatsMessageSenderClient natsMessageSenderClient;
    private FixedNatsPool fixedNatsPool;

    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;

    /**
     * Sets up the test environment before each test case.
     * Initializes the NATS client and mocks the configuration properties.
     */
    @SneakyThrows
    @BeforeEach
    void setUp() {
        MockitoAnnotations.openMocks(this);

        // Mock NATS configuration properties
        when(natsConfigurationProperties.getNatsUrl()).thenReturn(NATS_URL);
        when(natsConfigurationProperties.getConnectionTimeout()).thenReturn(Duration.ofSeconds(5));
        when(natsConfigurationProperties.getPingInterval()).thenReturn(Duration.ofSeconds(10));
        when(natsConfigurationProperties.getReconnectWait()).thenReturn(Duration.ofSeconds(1));
        when(natsConfigurationProperties.getReconnectJitter()).thenReturn(Duration.ZERO);
        when(natsConfigurationProperties.getNkeySeed()).thenReturn(Optional.empty());
        when(natsConfigurationProperties.getDelayBetweenMessages()).thenReturn(Duration.ZERO);
        when(natsConfigurationProperties.isCreateNatsStreams()).thenReturn(true);
        when(natsConfigurationProperties.isNatsEnabled()).thenReturn(true);
        when(natsConfigurationProperties.getMaxPoolSize()).thenReturn(1);

        // Initialize NATS client
        try {
            var applicationContext = mock(ApplicationContext.class);
            when(applicationContext.getBean(Connection.class)).thenAnswer(ignored ->
                    new NatsConfiguration().natsConnection(
                            natsConfigurationProperties, mock(Tracer.class)));
            fixedNatsPool = new FixedNatsPool(applicationContext, natsConfigurationProperties);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }

        // Spring lifecycle callbacks do not run for manually constructed test objects.
        NatsStreamManager natsStreamManager = new NatsStreamManager(
                new NatsResourceService(fixedNatsPool),
                natsConfigurationProperties);
        natsStreamManager.init();

        natsMessageSenderClient = new NatsMessageSenderClient(fixedNatsPool,
                                                              natsConfigurationProperties);
    }

    @AfterEach
    void tearDown() throws Exception {
        if (fixedNatsPool != null) {
            fixedNatsPool.close();
        }
    }

    /**
     * Tests sending a function message to a valid cluster.
     * Verifies that the message is sent successfully.
     */
    @Test
    void sendFunctionMessage_Success() {
        testMessageSending(
                NatsBaseTest.getByocSqsMessageModel(),
                "cluster-1",
                NatsMessageSenderClient.SendNatsMessageResult.SUCCESS,
                MessageType.FUNCTION
        );
    }

    /**
     * Tests sending a task message to a valid cluster.
     * Verifies that the message is sent successfully.
     */
    @Test
    void sendTaskMessage_Success() {
        testMessageSending(
                NatsBaseTest.getByocTaskSqsMessageModel(),
                "cluster-1",
                NatsMessageSenderClient.SendNatsMessageResult.SUCCESS,
                MessageType.TASK
        );
    }

    /**
     * Tests sending a terminate instance message to a valid cluster.
     * Verifies that the message is sent successfully.
     */
    @Test
    void sendTerminateInstanceMessage_Success() {
        testMessageSending(
                NatsBaseTest.getByocTerminatePodMessageModel(),
                "cluster-1",
                NatsMessageSenderClient.SendNatsMessageResult.SUCCESS,
                MessageType.TERMINATE_INSTANCE
        );
    }

    /**
     * Tests sending a null message.
     * Verifies that an {@link IllegalArgumentException} is thrown with the appropriate error message.
     */
    @Test
    void sendMessage_withNullMessage_ThrowsException() {
        // Act & Assert

        assertThrows(NullPointerException.class, () ->
                natsMessageSenderClient.sendFunctionMessage(null, "cluster-1"));
    }

    /**
     * Helper method to test message sending for different message types.
     *
     * @param message        The message object to send.
     * @param clusterId      The cluster ID to which the message is sent.
     * @param expectedResult The expected result of the message sending operation.
     * @param messageType    The type of the message being sent.
     */
    private void testMessageSending(
            Object message, String clusterId,
            NatsMessageSenderClient.SendNatsMessageResult expectedResult, MessageType messageType) {
        // Act
        NatsMessageSenderClient.SendNatsMessageResult result;
        switch (messageType) {
            case FUNCTION:
                result = natsMessageSenderClient.sendFunctionMessage(
                        (ByocSqsMessageModel) message, clusterId);
                break;
            case TASK:
                result = natsMessageSenderClient.sendTaskMessage(
                        (ByocSqsMessageModel) message, clusterId);
                break;
            case TERMINATE_INSTANCE:
                result = natsMessageSenderClient.sendTerminateInstanceMessage(
                        (ByocTerminatePodMessageModel) message, clusterId);
                break;
            default:
                throw new IllegalArgumentException("Unsupported message type");
        }

        // Assert
        assertEquals(expectedResult, result);
    }

    /**
     * Enum representing the types of messages that can be sent.
     */
    private enum MessageType {
        FUNCTION,
        TASK,
        TERMINATE_INSTANCE
    }
}
