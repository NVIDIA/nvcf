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
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.icms.configuration.nats.NatsConfiguration;
import io.micrometer.tracing.Tracer;
import io.nats.client.Connection;
import io.nats.client.api.RetentionPolicy;
import io.nats.client.api.StorageType;
import java.time.Duration;
import java.util.Optional;
import lombok.SneakyThrows;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;
import org.springframework.context.ApplicationContext;

class NatsStreamManagerIntegrationTest extends IntegrationTest {

    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;

    private FixedNatsPool fixedNatsPool;
    private NatsStreamManager natsStreamManager;

    @SneakyThrows
    @BeforeEach
    void setUp() {
        MockitoAnnotations.openMocks(this);
        when(natsConfigurationProperties.getNatsUrl()).thenReturn(NATS_URL);
        when(natsConfigurationProperties.getConnectionTimeout()).thenReturn(Duration.ofSeconds(5));
        when(natsConfigurationProperties.getPingInterval()).thenReturn(Duration.ofSeconds(10));
        when(natsConfigurationProperties.getReconnectWait()).thenReturn(Duration.ofSeconds(1));
        when(natsConfigurationProperties.getReconnectJitter()).thenReturn(Duration.ZERO);
        when(natsConfigurationProperties.getNkeySeed()).thenReturn(Optional.empty());
        when(natsConfigurationProperties.getMessageTtl()).thenReturn(Duration.ofHours(24));
        when(natsConfigurationProperties.isNatsEnabled()).thenReturn(true);
        when(natsConfigurationProperties.getMaxPoolSize()).thenReturn(1);

        try {
            var applicationContext = mock(ApplicationContext.class);
            when(applicationContext.getBean(Connection.class)).thenAnswer(ignored ->
                    new NatsConfiguration().natsConnection(
                            natsConfigurationProperties, mock(Tracer.class)));
            fixedNatsPool = new FixedNatsPool(applicationContext, natsConfigurationProperties);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
        natsStreamManager = new NatsStreamManager(
                new NatsResourceService(fixedNatsPool),
                natsConfigurationProperties);
    }

    @AfterEach
    void tearDown() throws Exception {
        if (fixedNatsPool != null) {
            fixedNatsPool.close();
        }
    }

    @Test
    void validateNatsStreams_createsNvcaStreamsWithExistingConfiguration() throws Exception {
        natsStreamManager.validateNatsStreams();

        var management = fixedNatsPool.borrowJetStreamManagement();
        var createStream = management.getStreamInfo(NatsStreamManager.CREATE_NVCA_STREAM_NAME);
        var terminateStream = management.getStreamInfo(NatsStreamManager.TERMINATE_NVCA_STREAM_NAME);

        assertStream(createStream, "Create.NVCA.>");
        assertStream(terminateStream, "Terminate.NVCA.>");
    }

    private static void assertStream(io.nats.client.api.StreamInfo streamInfo, String subject) {
        assertNotNull(streamInfo);
        var configuration = streamInfo.getConfiguration();
        assertEquals(java.util.List.of(subject), configuration.getSubjects());
        assertEquals(StorageType.Memory, configuration.getStorageType());
        assertEquals(RetentionPolicy.WorkQueue, configuration.getRetentionPolicy());
        assertEquals(1_000_000, configuration.getMaxMsgs());
    }
}
