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
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import io.nats.client.api.RetentionPolicy;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import java.time.Duration;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class NatsStreamManagerTest {

    @Mock
    private NatsResourceService natsResourceService;
    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;

    private NatsStreamManager natsStreamManager;

    @BeforeEach
    void setUp() {
        natsStreamManager = new NatsStreamManager(
                natsResourceService, natsConfigurationProperties);
    }

    @Test
    void validateNatsStreams_createsNvcaStreamsWithExistingConfiguration() throws Exception {
        when(natsConfigurationProperties.getMessageTtl()).thenReturn(Duration.ofHours(24));

        natsStreamManager.validateNatsStreams();

        var captor = ArgumentCaptor.forClass(StreamConfiguration.class);
        verify(natsResourceService, org.mockito.Mockito.times(2)).createStream(captor.capture());
        var streams = captor.getAllValues();

        assertStream(streams.get(0), NatsStreamManager.CREATE_NVCA_STREAM_NAME,
                     "Create.NVCA.>");
        assertStream(streams.get(1), NatsStreamManager.TERMINATE_NVCA_STREAM_NAME,
                     "Terminate.NVCA.>");
    }

    @Test
    void init_doesNothingWhenNatsIsDisabled() throws Exception {
        when(natsConfigurationProperties.isNatsEnabled()).thenReturn(false);

        natsStreamManager.init();

        verify(natsResourceService, never()).createStream(any());
    }

    @Test
    void init_doesNothingWhenStreamCreationIsDisabled() throws Exception {
        when(natsConfigurationProperties.isNatsEnabled()).thenReturn(true);
        when(natsConfigurationProperties.isCreateNatsStreams()).thenReturn(false);

        natsStreamManager.init();

        verify(natsResourceService, never()).createStream(any());
    }

    @Test
    void init_createsBothStreams() throws Exception {
        when(natsConfigurationProperties.isNatsEnabled()).thenReturn(true);
        when(natsConfigurationProperties.isCreateNatsStreams()).thenReturn(true);
        when(natsConfigurationProperties.getMessageTtl()).thenReturn(Duration.ofHours(24));

        natsStreamManager.init();

        verify(natsResourceService, org.mockito.Mockito.times(2)).createStream(any());
    }

    private static void assertStream(
            StreamConfiguration stream, String expectedName, String expectedSubject) {
        assertEquals(expectedName, stream.getName());
        assertEquals(java.util.List.of(expectedSubject), stream.getSubjects());
        assertEquals(StorageType.Memory, stream.getStorageType());
        assertEquals(RetentionPolicy.WorkQueue, stream.getRetentionPolicy());
        assertEquals(1_000_000, stream.getMaxMsgs());
        assertEquals(Duration.ofHours(24), stream.getMaxAge());
    }
}
