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
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import io.nats.client.Connection;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.api.RetentionPolicy;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import io.nats.client.support.Status;
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
    private Connection connection;
    @Mock
    private JetStreamManagement management;
    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;

    private NatsStreamManager natsStreamManager;

    @BeforeEach
    void setUp() throws Exception {
        when(connection.jetStreamManagement()).thenReturn(management);
        natsStreamManager = new NatsStreamManager(connection, natsConfigurationProperties);
    }

    @Test
    void validateNatsStreams_createsNvcaStreamsWithExistingConfiguration() throws Exception {
        when(natsConfigurationProperties.getMessageTtl()).thenReturn(Duration.ofHours(24));
        when(management.getStreamInfo(any()))
                .thenThrow(apiException(Status.NOT_FOUND_CODE));

        natsStreamManager.validateNatsStreams();

        var captor = ArgumentCaptor.forClass(StreamConfiguration.class);
        verify(management, times(2)).addStream(captor.capture());
        var streams = captor.getAllValues();

        assertStream(streams.get(0), NatsStreamManager.CREATE_NVCA_STREAM_NAME,
                     "Create.NVCA.>");
        assertStream(streams.get(1), NatsStreamManager.TERMINATE_NVCA_STREAM_NAME,
                     "Terminate.NVCA.>");
    }

    @Test
    void init_doesNothingWhenNatsIsDisabled() throws Exception {
        when(natsConfigurationProperties.isEnabled()).thenReturn(false);

        natsStreamManager.init();

        verifyNoInteractions(management);
    }

    @Test
    void init_doesNothingWhenStreamCreationIsDisabled() throws Exception {
        when(natsConfigurationProperties.isEnabled()).thenReturn(true);
        when(natsConfigurationProperties.isCreateNatsStreams()).thenReturn(false);

        natsStreamManager.init();

        verifyNoInteractions(management);
    }

    @Test
    void init_createsBothStreams() throws Exception {
        when(natsConfigurationProperties.isEnabled()).thenReturn(true);
        when(natsConfigurationProperties.isCreateNatsStreams()).thenReturn(true);
        when(natsConfigurationProperties.getMessageTtl()).thenReturn(Duration.ofHours(24));
        when(management.getStreamInfo(any()))
                .thenThrow(apiException(Status.NOT_FOUND_CODE));

        natsStreamManager.init();

        verify(management, times(2)).addStream(any());
    }

    @Test
    void createStream_doesNotModifyExistingStream() throws Exception {
        var configuration = streamConfiguration();
        var streamInfo = org.mockito.Mockito.mock(StreamInfo.class);
        when(streamInfo.getConfiguration()).thenReturn(configuration);
        when(management.getStreamInfo(configuration.getName())).thenReturn(streamInfo);

        natsStreamManager.createStream(configuration);

        verify(management, never()).addStream(configuration);
    }

    @Test
    void createStream_rejectsIncompatibleExistingStream() throws Exception {
        var configuration = streamConfiguration();
        var existing = StreamConfiguration.builder()
                .name(configuration.getName())
                .subjects("other.>")
                .build();
        var streamInfo = org.mockito.Mockito.mock(StreamInfo.class);
        when(streamInfo.getConfiguration()).thenReturn(existing);
        when(management.getStreamInfo(configuration.getName())).thenReturn(streamInfo);

        assertThrows(IllegalStateException.class,
                     () -> natsStreamManager.createStream(configuration));
        verify(management, never()).addStream(configuration);
    }

    @Test
    void createStream_addsMissingStream() throws Exception {
        var configuration = streamConfiguration();
        when(management.getStreamInfo(configuration.getName()))
                .thenThrow(apiException(Status.NOT_FOUND_CODE));

        natsStreamManager.createStream(configuration);

        verify(management).addStream(configuration);
    }

    @Test
    void createStream_propagatesLookupFailure() throws Exception {
        var configuration = streamConfiguration();
        when(management.getStreamInfo(configuration.getName()))
                .thenThrow(apiException(500));

        assertThrows(JetStreamApiException.class,
                     () -> natsStreamManager.createStream(configuration));
        verify(management, never()).addStream(configuration);
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

    private static StreamConfiguration streamConfiguration() {
        return StreamConfiguration.builder().name("stream").subjects("subject.>").build();
    }

    private static JetStreamApiException apiException(int statusCode) {
        var status = new Status(statusCode, "test error");
        var error = io.nats.client.api.Error.convert(status);
        return new JetStreamApiException(error);
    }
}
