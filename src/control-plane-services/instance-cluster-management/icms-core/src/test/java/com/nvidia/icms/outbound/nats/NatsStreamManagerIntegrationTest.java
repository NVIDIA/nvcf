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

import static com.nvidia.icms.outbound.nats.CoreNatsStreamRegistrar.CREATE_NVCA_STREAM_NAME;
import static com.nvidia.icms.outbound.nats.CoreNatsStreamRegistrar.TERMINATE_NVCA_STREAM_NAME;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.NatsConfigurationProperties;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.util.TestUtil;
import io.nats.client.ConsumerContext;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.StreamInfo;
import java.io.IOException;
import java.time.Duration;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;
import org.springframework.beans.factory.annotation.Autowired;

/**
 * Integration tests for the {@link NatsStreamManager} class.
 * This class validates the creation, deletion, retrieval, and management of NATS streams and consumers.
 */
class NatsStreamManagerIntegrationTest extends IntegrationTest {

    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;

    private NatsStreamManager natsStreamManager;

    /**
     * Sets up the test environment by initializing mocks and the NatsStreamManager instance.
     */
    @BeforeEach
    void setUp() {
        MockitoAnnotations.openMocks(this);

        when(natsConfigurationProperties.getNatsUrl()).thenReturn(NATS_URL);
        when(natsConfigurationProperties.getConnectionTimeout()).thenReturn(Duration.ofSeconds(5));
        when(natsConfigurationProperties.getPingInterval()).thenReturn(Duration.ofSeconds(10));
        when(natsConfigurationProperties.getReconnectWait()).thenReturn(Duration.ofSeconds(1));
        when(natsConfigurationProperties.getReconnectJitter()).thenReturn(Duration.ZERO);
        when(natsConfigurationProperties.getMessageTtl()).thenReturn(Duration.ofHours(24));
        when(natsConfigurationProperties.isCreateNatsStreams()).thenReturn(false);
        when(natsConfigurationProperties.isNatsEnabled()).thenReturn(true);

        NatsConnectionFactory natsConnectionFactory = new NatsConnectionFactory(
                natsConfigurationProperties);
        natsStreamManager = new NatsStreamManager(natsConnectionFactory,
                                                  natsConfigurationProperties,
                                                  List.of(new CoreNatsStreamRegistrar()));
    }

    /**
     * Validates that all required NATS streams are created successfully.
     */
    @Test
    void validateNatsStreams_createsAllStreams()
            throws IOException, InterruptedException {
        natsStreamManager.validateNatsStreams();

        StreamInfo createNvcaStream = natsStreamManager.getStream(CREATE_NVCA_STREAM_NAME);
        StreamInfo terminateNvcaStream = natsStreamManager.getStream(TERMINATE_NVCA_STREAM_NAME);

        assertNotNull(createNvcaStream);
        assertNotNull(terminateNvcaStream);
    }

    /**
     * Tests the successful creation of a NATS stream.
     */
    @Test
    void createStream_createsStreamSuccessfully()
            throws IOException, JetStreamApiException, InterruptedException {
        when(natsConfigurationProperties.getMessageTtl()).thenReturn(Duration.ofHours(24));

        String testStream = TestUtil.getRandomStringWithPrefix("TestStream", 5);
        String testSubject = TestUtil.getRandomStringWithPrefix("Test.Subject.", 5);

        StreamInfo streamInfo = natsStreamManager.createStream(testStream, testSubject);

        assertNotNull(streamInfo);
    }

    /**
     * Tests the successful deletion of a NATS stream.
     */
    @Test
    void deleteStream_deletesStreamSuccessfully()
            throws IOException, JetStreamApiException, InterruptedException {
        String testStream = TestUtil.getRandomStringWithPrefix("TestStream", 5);
        String testSubject = TestUtil.getRandomStringWithPrefix("Test.Subject.", 5);

        natsStreamManager.createStream(testStream, testSubject);
        natsStreamManager.deleteStream(testStream);

        StreamInfo deletedStream = natsStreamManager.getStream(testStream);

        assertNull(deletedStream);
    }

    /**
     * Tests retrieving stream information for an existing stream.
     */
    @Test
    void getStream_returnsStreamInfoIfExists()
            throws IOException, InterruptedException, JetStreamApiException {
        String testStream = TestUtil.getRandomStringWithPrefix("TestStream", 5);
        String testSubject = TestUtil.getRandomStringWithPrefix("Test.Subject.", 5);

        natsStreamManager.createStream(testStream, testSubject);
        StreamInfo streamInfo = natsStreamManager.getStream(testStream);

        assertNotNull(streamInfo);
    }

    /**
     * Tests retrieving stream information for a non-existent stream.
     */
    @Test
    void getStream_returnsNullIfStreamDoesNotExist()
            throws IOException, InterruptedException {
        String testStream = TestUtil.getRandomStringWithPrefix("TestStream", 5);

        StreamInfo streamInfo = natsStreamManager.getStream(testStream);

        assertNull(streamInfo);
    }

    /**
     * Tests creating a stream if it does not already exist.
     */
    @Test
    void getOrCreateStream_createsStreamIfNotExists() {
        String testStream = TestUtil.getRandomStringWithPrefix("TestStream", 5);
        String testSubject = TestUtil.getRandomStringWithPrefix("Test.Subject.", 5);

        StreamInfo streamInfo = natsStreamManager.getOrCreateStream(testStream, testSubject);

        assertNotNull(streamInfo);
    }

    /**
     * Tests returning an existing stream if it already exists.
     */
    @Test
    void getOrCreateStream_returnsExistingStreamIfExists()
            throws JetStreamApiException, IOException, InterruptedException {
        String testStream = TestUtil.getRandomStringWithPrefix("TestStream", 5);
        String testSubject = TestUtil.getRandomStringWithPrefix("Test.Subject.", 5);

        natsStreamManager.createStream(testStream, testSubject);
        StreamInfo streamInfo = natsStreamManager.getOrCreateStream(testStream, testSubject);

        assertNotNull(streamInfo);
    }

    /**
     * Tests the successful creation of a consumer for a specific stream and subject.
     */
    @Test
    void createConsumer_createsConsumerSuccessfully() {
        String testStream = TestUtil.getRandomStringWithPrefix("TestStream", 5);
        String testSubject = TestUtil.getRandomStringWithPrefix("Test.Subject.", 5);

        natsStreamManager.getOrCreateStream(testStream, testSubject);
        ConsumerContext result = natsStreamManager.createConsumer(testStream,
                                                                  testStream + "Consumer",
                                                                  testSubject);

        assertNotNull(result);
    }
}
