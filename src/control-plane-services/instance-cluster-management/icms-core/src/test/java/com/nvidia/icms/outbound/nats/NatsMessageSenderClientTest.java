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
import static org.mockito.Mockito.any;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import io.nats.client.JetStream;
import io.nats.client.JetStreamApiException;
import io.nats.client.impl.NatsMessage;
import java.io.IOException;
import java.time.Duration;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;

/**
 * Unit tests for the NatsMessageSenderClient class.
 * This class verifies the behavior of the NatsMessageSenderClient when sending messages
 * to NATS using JetStream.
 */
class NatsMessageSenderClientTest {

    private static final String CLUSTER_ID = "cluster-1";
    private static final String EMPTY_MESSAGE = "{}";
    private static final String SUBJECT = "subject";

    @Mock
    private FixedNatsPool fixedNatsPool;

    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;

    @Mock
    private JetStream jetStream;

    private NatsMessageSenderClient natsMessageSenderClient;

    /**
     * Sets up the test environment by initializing mocks and the NatsMessageSenderClient instance.
     */
    @BeforeEach
    void setUp()
            throws Exception {
        MockitoAnnotations.openMocks(this);
        when(fixedNatsPool.borrowJetStream()).thenReturn(jetStream);
        when(natsConfigurationProperties.isNatsEnabled()).thenReturn(true);
        when(natsConfigurationProperties.getDelayBetweenMessages()).thenReturn(Duration.ZERO);

        natsMessageSenderClient = new NatsMessageSenderClient(fixedNatsPool,
                                                              natsConfigurationProperties);
    }

    /**
     * Verifies that multiple function messages are sent successfully using JetStream.
     */
    @Test
    void sendFunctionMessages_withValidInputs_sendsAllMessages()
            throws JetStreamApiException, IOException {
        // Prepare
        var messages = List.of(NatsBaseTest.getByocSqsMessageModel(),
                               NatsBaseTest.getByocSqsMessageModel());

        // Act
        natsMessageSenderClient.sendFunctionMessages(messages, CLUSTER_ID);

        // Assert
        verify(jetStream, times(messages.size())).publish(any(NatsMessage.class));
    }

    /**
     * Verifies that a single function message is sent successfully.
     */
    @Test
    void sendFunctionMessage_withValidInput_returnsSuccess()
            throws JetStreamApiException, IOException {
        // Prepare
        var message = NatsBaseTest.getByocSqsMessageModel();

        // Act
        var result = natsMessageSenderClient.sendFunctionMessage(message, CLUSTER_ID);

        // Assert
        assertEquals(NatsMessageSenderClient.SendNatsMessageResult.SUCCESS, result);
        verify(jetStream).publish(any(NatsMessage.class));
    }

    /**
     * Verifies that an IOException during message sending results in a failure.
     */
    @Test
    void sendFunctionMessage_withIOException_returnsFailure()
            throws Exception {
        // Prepare
        var message = NatsBaseTest.getByocSqsMessageModel();
        doThrow(new IOException("Test exception")).when(jetStream).publish(any());

        // Act
        var result = natsMessageSenderClient.sendFunctionMessage(message, CLUSTER_ID);

        // Assert
        assertEquals(NatsMessageSenderClient.SendNatsMessageResult.FAILURE, result);
    }

    @Test
    void sendFunctionMessages_withPublishFailure_throws() throws Exception {
        var messages = List.of(NatsBaseTest.getByocSqsMessageModel());
        doThrow(new IOException("Connection is closed")).when(jetStream).publish(any());

        assertThrows(IcmsInternalServerException.class,
                () -> natsMessageSenderClient.sendFunctionMessages(messages, CLUSTER_ID));
    }

    /**
     * Verifies that multiple task messages are sent successfully using JetStream.
     */
    @Test
    void sendTaskMessages_withValidInputs_sendsAllMessages()
            throws JetStreamApiException, IOException {
        // Prepare
        var messages = List.of(NatsBaseTest.getByocTaskSqsMessageModel(),
                               NatsBaseTest.getByocTaskSqsMessageModel());

        // Act
        natsMessageSenderClient.sendTaskMessages(messages, CLUSTER_ID);

        // Assert
        verify(jetStream, times(messages.size())).publish(any(NatsMessage.class));
    }

    /**
     * Verifies that a single task message is sent successfully.
     */
    @Test
    void sendTaskMessage_withValidInput_returnsSuccess()
            throws JetStreamApiException, IOException {
        // Prepare
        var message = NatsBaseTest.getByocTaskSqsMessageModel();

        // Act
        var result = natsMessageSenderClient.sendTaskMessage(message, CLUSTER_ID);

        // Assert
        assertEquals(NatsMessageSenderClient.SendNatsMessageResult.SUCCESS, result);
        verify(jetStream).publish(any(NatsMessage.class));
    }

    /**
     * Verifies that multiple terminate instance messages are sent successfully using JetStream.
     */
    @Test
    void sendTerminateInstanceMessages_withValidInputs_sendsAllMessages()
            throws JetStreamApiException, IOException {
        // Prepare
        var messages = List.of(NatsBaseTest.getByocTerminatePodMessageModel(),
                               NatsBaseTest.getByocTerminatePodMessageModel());

        // Act
        natsMessageSenderClient.sendTerminateInstanceMessages(messages, CLUSTER_ID);

        // Assert
        verify(jetStream, times(messages.size())).publish(any(NatsMessage.class));
    }

    /**
     * Verifies that a single terminate instance message is sent successfully.
     */
    @Test
    void sendTerminateInstanceMessage_withValidInput_returnsSuccess()
            throws JetStreamApiException, IOException {
        // Prepare
        var message = NatsBaseTest.getByocTerminatePodMessageModel();

        // Act
        var result = natsMessageSenderClient.sendTerminateInstanceMessage(message, CLUSTER_ID);

        // Assert
        assertEquals(NatsMessageSenderClient.SendNatsMessageResult.SUCCESS, result);
        verify(jetStream).publish(any(NatsMessage.class));
    }

    /**
     * Verifies that publishing a message with no responders results in a NO_RESPONDERS result.
     */
    @Test
    void publishMessage_withNoResponders_returnsNoResponders()
            throws Exception {
        // Prepare
        doThrow(new IOException("503 No Responders Available For Request")).when(jetStream)
                .publish(any());

        // Act
        var result = natsMessageSenderClient.publishMessage(EMPTY_MESSAGE, SUBJECT);

        // Assert
        assertEquals(NatsMessageSenderClient.SendNatsMessageResult.NO_RESPONDERS, result);
    }

    /**
     * Verifies that an IOException during message publishing results in a FAILURE result.
     */
    @Test
    void publishMessage_withIOException_returnsFailure()
            throws Exception {
        // Prepare
        doThrow(new IOException("Test exception")).when(jetStream).publish(any());

        // Act
        var result = natsMessageSenderClient.publishMessage(EMPTY_MESSAGE, SUBJECT);

        // Assert
        assertEquals(NatsMessageSenderClient.SendNatsMessageResult.FAILURE, result);
    }

    /**
     * Verifies that the delay between messages is applied correctly based on configuration.
     */
    @Test
    void delayBetweenMessages_withValidDelay_sleepsCorrectly() {
        // Prepare
        when(natsConfigurationProperties.getDelayBetweenMessages()).thenReturn(Duration.ofMillis(100));

        // Act
        natsMessageSenderClient.delayBetweenMessages();

        // Assert
        verify(natsConfigurationProperties).getDelayBetweenMessages();
    }
}
