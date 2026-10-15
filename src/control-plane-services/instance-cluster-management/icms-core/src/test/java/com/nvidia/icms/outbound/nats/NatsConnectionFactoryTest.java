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

import static org.junit.jupiter.api.Assertions.assertSame;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.spy;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.NatsConfigurationProperties;
import io.nats.client.Connection;
import org.junit.jupiter.api.Test;

class NatsConnectionFactoryTest {

    @Test
    void getConnection_replacesClosedCachedConnection() throws Exception {
        NatsConnectionFactory factory = spy(new NatsConnectionFactory(
                mock(NatsConfigurationProperties.class)));
        Connection closedConnection = mock(Connection.class);
        Connection replacementConnection = mock(Connection.class);
        when(closedConnection.getStatus()).thenReturn(Connection.Status.CLOSED);
        when(replacementConnection.getStatus()).thenReturn(Connection.Status.CONNECTED);
        doReturn(closedConnection, replacementConnection).when(factory).connectToNats();

        assertSame(closedConnection, factory.createConnectionIfNeeded());
        assertSame(replacementConnection, factory.createConnectionIfNeeded());
        verify(factory, times(2)).connectToNats();
    }

    @Test
    void getConnection_reusesConnectionWhileReconnecting() throws Exception {
        NatsConnectionFactory factory = spy(new NatsConnectionFactory(
                mock(NatsConfigurationProperties.class)));
        Connection reconnectingConnection = mock(Connection.class);
        when(reconnectingConnection.getStatus()).thenReturn(Connection.Status.RECONNECTING);
        doReturn(reconnectingConnection).when(factory).connectToNats();

        assertSame(reconnectingConnection, factory.createConnectionIfNeeded());
        assertSame(reconnectingConnection, factory.createConnectionIfNeeded());
        verify(factory).connectToNats();
    }

    @Test
    void closedEvent_doesNotInvalidateNewerConnection() throws Exception {
        NatsConnectionFactory factory = spy(new NatsConnectionFactory(
                mock(NatsConfigurationProperties.class)));
        Connection closedConnection = mock(Connection.class);
        Connection replacementConnection = mock(Connection.class);
        when(closedConnection.getStatus()).thenReturn(Connection.Status.CLOSED);
        when(replacementConnection.getStatus()).thenReturn(Connection.Status.CONNECTED);
        doReturn(closedConnection, replacementConnection).when(factory).connectToNats();

        factory.createConnectionIfNeeded();
        assertSame(replacementConnection, factory.createConnectionIfNeeded());
        factory.invalidateClosedConnection(closedConnection);

        assertSame(replacementConnection, factory.createConnectionIfNeeded());
        verify(factory, times(2)).connectToNats();
    }
}
