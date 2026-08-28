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

import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import io.nats.client.support.Status;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class NatsResourceServiceTest {

    @Mock
    private JetStreamManagement management;

    @Mock
    private FixedNatsPool fixedNatsPool;

    private NatsResourceService natsResourceService;

    @BeforeEach
    void setUp() throws Exception {
        when(fixedNatsPool.borrowJetStreamManagement()).thenReturn(management);
        natsResourceService = new NatsResourceService(fixedNatsPool);
    }

    @Test
    void createStream_doesNotModifyExistingStream() throws Exception {
        var configuration = streamConfiguration();
        var streamInfo = org.mockito.Mockito.mock(StreamInfo.class);
        when(streamInfo.getConfiguration()).thenReturn(configuration);
        when(management.getStreamInfo(configuration.getName())).thenReturn(streamInfo);

        natsResourceService.createStream(configuration);

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
                     () -> natsResourceService.createStream(configuration));
        verify(management, never()).addStream(configuration);
    }

    @Test
    void createStream_addsMissingStream() throws Exception {
        var configuration = streamConfiguration();
        when(management.getStreamInfo(configuration.getName()))
                .thenThrow(apiException(Status.NOT_FOUND_CODE));

        natsResourceService.createStream(configuration);

        verify(management).addStream(configuration);
    }

    @Test
    void createStream_propagatesLookupFailure() throws Exception {
        var configuration = streamConfiguration();
        when(management.getStreamInfo(configuration.getName()))
                .thenThrow(apiException(500));

        assertThrows(JetStreamApiException.class,
                     () -> natsResourceService.createStream(configuration));
        verify(management, never()).addStream(configuration);
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
