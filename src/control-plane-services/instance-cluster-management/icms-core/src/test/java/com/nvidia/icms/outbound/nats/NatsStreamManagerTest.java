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

import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.any;
import static org.mockito.Mockito.anyString;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.NatsConfigurationProperties;
import io.nats.client.Connection;
import io.nats.client.ConsumerContext;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.StreamContext;
import io.nats.client.api.ApiResponse;
import io.nats.client.api.ConsumerConfiguration;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import io.nats.client.support.JsonValue;
import java.io.IOException;
import java.time.Duration;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;

/**
 * Unit tests for the NatsStreamManager class.
 * This class validates the behavior of stream and consumer management in NATS.
 */
class NatsStreamManagerTest {

    @Mock
    private NatsConnectionFactory natsConnectionFactory;
    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;
    @Mock
    private JetStreamManagement jetStreamManagement;
    @Mock
    private StreamContext streamContext;
    @Mock
    private ConsumerContext consumerContext;
    @Mock
    private Connection natsConnection;
    private NatsStreamManager natsStreamManager;

    /**
     * Sets up the test environment by initializing mocks and configuring default behavior.
     */
    @BeforeEach
    void setUp()
            throws IOException, InterruptedException, JetStreamApiException {
        MockitoAnnotations.openMocks(this);

        when(natsConnectionFactory.createConnectionIfNeeded()).thenReturn(natsConnection);
        when(natsConnectionFactory.createConnectionIfNeeded().jetStreamManagement()).thenReturn(
                jetStreamManagement);
        when(natsConnectionFactory.createConnectionIfNeeded()
                     .getStreamContext(anyString())).thenReturn(streamContext);

        when(natsConfigurationProperties.isCreateNatsStreams()).thenReturn(false);
        natsStreamManager = new NatsStreamManager(natsConnectionFactory,
                                                  natsConfigurationProperties,
                                                  List.of(new CoreNatsStreamRegistrar()));
    }

    /**
     * Validates that all required streams are created if they do not exist.
     */
    @Test
    void validateNatsStreams_createsAllStreamsIfNotExist()
            throws IOException, JetStreamApiException {
        // Mock
        when(jetStreamManagement.getStreamInfo(anyString())).thenThrow(
                new JetStreamApiException(new TestResponse()));
        when(jetStreamManagement.addStream(any(StreamConfiguration.class))).thenReturn(
                mock(StreamInfo.class));

        // Act
        natsStreamManager.validateNatsStreams();

        // Assert
        verify(jetStreamManagement, times(2)).addStream(any(StreamConfiguration.class));
    }

    /**
     * Tests successful creation of a stream.
     */
    @Test
    void createStream_createsStreamSuccessfully()
            throws IOException, JetStreamApiException, InterruptedException {
        // Mock
        when(jetStreamManagement.addStream(any(StreamConfiguration.class))).thenReturn(
                mock(StreamInfo.class));
        when(natsConfigurationProperties.getMessageTtl()).thenReturn(Duration.ofHours(24));

        // Act
        StreamInfo streamInfo = natsStreamManager.createStream("TestStream", "Test.Subject");

        // Assert
        assertNotNull(streamInfo);
        verify(jetStreamManagement, times(1)).addStream(any(StreamConfiguration.class));
    }

    /**
     * Tests exception handling during stream creation.
     */
    @Test
    void createStream_throwsExceptionOnFailure()
            throws IOException, JetStreamApiException {
        // Mock
        when(jetStreamManagement.addStream(any(StreamConfiguration.class))).thenThrow(
                new IOException("Stream creation failed"));

        // Act & Assert
        assertThrows(IOException.class,
                     () -> natsStreamManager.createStream("TestStream", "Test.Subject"));
    }

    /**
     * Tests successful deletion of a stream.
     */
    @Test
    void deleteStream_deletesStreamSuccessfully()
            throws IOException, JetStreamApiException, InterruptedException {
        // Act
        natsStreamManager.deleteStream("TestStream");

        // Assert
        verify(jetStreamManagement, times(1)).deleteStream("TestStream");
    }

    /**
     * Tests exception handling during stream deletion.
     */
    @Test
    void deleteStream_throwsExceptionOnFailure()
            throws IOException, JetStreamApiException {
        // Mock
        doThrow(new IOException("Stream deletion failed")).when(jetStreamManagement)
                .deleteStream(anyString());

        // Act & Assert
        assertThrows(IOException.class, () -> natsStreamManager.deleteStream("TestStream"));
    }

    /**
     * Tests retrieval of stream information if the stream exists.
     */
    @Test
    void getStream_returnsStreamInfoIfExists()
            throws IOException, InterruptedException, JetStreamApiException {
        // Mock
        StreamInfo mockStreamInfo = mock(StreamInfo.class);
        when(jetStreamManagement.getStreamInfo("TestStream")).thenReturn(mockStreamInfo);

        // Act
        StreamInfo streamInfo = natsStreamManager.getStream("TestStream");

        // Assert
        assertNotNull(streamInfo);
        verify(jetStreamManagement, times(1)).getStreamInfo("TestStream");
    }

    /**
     * Tests retrieval of stream information when the stream does not exist.
     */
    @Test
    void getStream_returnsNullIfStreamDoesNotExist()
            throws IOException, InterruptedException, JetStreamApiException {
        // Mock
        when(jetStreamManagement.getStreamInfo("TestStream")).thenThrow(
                new JetStreamApiException(new TestResponse()));

        // Act
        StreamInfo streamInfo = natsStreamManager.getStream("TestStream");

        // Assert
        assertNull(streamInfo);
    }

    /**
     * Tests creation of a stream if it does not exist.
     */
    @Test
    void getOrCreateStream_createsStreamIfNotExists()
            throws IOException, JetStreamApiException {
        // Mock
        when(jetStreamManagement.getStreamInfo("TestStream")).thenThrow(
                new JetStreamApiException(new TestResponse()));
        when(jetStreamManagement.addStream(any(StreamConfiguration.class))).thenReturn(
                mock(StreamInfo.class));

        // Act
        StreamInfo streamInfo = natsStreamManager.getOrCreateStream("TestStream", "Test.Subject");

        // Assert
        assertNotNull(streamInfo);
        verify(jetStreamManagement, times(1)).addStream(any(StreamConfiguration.class));
    }

    /**
     * Tests retrieval of an existing stream without creating a new one.
     */
    @Test
    void getOrCreateStream_returnsExistingStreamIfExists()
            throws IOException, JetStreamApiException {
        // Mock
        StreamInfo mockStreamInfo = mock(StreamInfo.class);
        when(jetStreamManagement.getStreamInfo("TestStream")).thenReturn(mockStreamInfo);

        // Act
        StreamInfo streamInfo = natsStreamManager.getOrCreateStream("TestStream", "Test.Subject");

        // Assert
        assertNotNull(streamInfo);
        verify(jetStreamManagement, times(0)).addStream(any(StreamConfiguration.class));
    }

    /**
     * Tests successful creation of a consumer.
     */
    @Test
    void createConsumer_createsConsumerSuccessfully()
            throws IOException, JetStreamApiException {
        // Mock
        when(streamContext.createOrUpdateConsumer(any(ConsumerConfiguration.class))).thenReturn(
                consumerContext);

        // Act
        ConsumerContext result = natsStreamManager.createConsumer("TestStream", "TestConsumer",
                                                                  "Test.Subject");

        // Assert
        assertNotNull(result);
        verify(streamContext, times(1)).createOrUpdateConsumer(any(ConsumerConfiguration.class));
    }

    /**
     * Tests error handling during consumer creation.
     */
    @Test
    void createConsumer_logsErrorOnFailure()
            throws IOException, JetStreamApiException {
        // Mock
        when(streamContext.createOrUpdateConsumer(any(ConsumerConfiguration.class))).thenThrow(
                new IOException("Consumer creation failed"));

        // Act
        ConsumerContext result = natsStreamManager.createConsumer("TestStream", "TestConsumer",
                                                                  "Test.Subject");

        // Assert
        assertNull(result);
        verify(streamContext, times(1)).createOrUpdateConsumer(any(ConsumerConfiguration.class));
    }

    /**
     * Mock implementation of an API response for testing purposes.
     */
    class TestResponse extends ApiResponse<StreamInfo> {

        public TestResponse() {
            super(getJsonMap());
        }

        private static JsonValue getJsonMap() {
            Map<String, JsonValue> m = new HashMap<>();
            m.put("error", new JsonValue("Stream not found"));
            return new JsonValue(m);
        }
    }
}
