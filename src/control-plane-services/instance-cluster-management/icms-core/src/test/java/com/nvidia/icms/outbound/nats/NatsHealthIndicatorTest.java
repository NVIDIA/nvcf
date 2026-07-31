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
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import io.nats.client.Connection;
import org.junit.jupiter.api.Test;
import org.springframework.boot.health.contributor.Status;

class NatsHealthIndicatorTest {

    private final NatsConnectionFactory factory = mock(NatsConnectionFactory.class);
    private final NatsHealthIndicator healthIndicator = new NatsHealthIndicator(factory);

    @Test
    void health_isDownBeforeConnectionInitialization() {
        var health = healthIndicator.health();

        assertEquals(Status.DOWN, health.getStatus());
        assertEquals("NOT_INITIALIZED", health.getDetails().get("status"));
    }

    @Test
    void health_isUpForConnectedConnection() {
        Connection connection = mock(Connection.class);
        when(factory.getCachedConnection()).thenReturn(connection);
        when(connection.getStatus()).thenReturn(Connection.Status.CONNECTED);
        when(connection.getConnectedUrl()).thenReturn("nats://nats:4222");

        var health = healthIndicator.health();

        assertEquals(Status.UP, health.getStatus());
        assertEquals("CONNECTED", health.getDetails().get("status"));
        assertEquals("nats://nats:4222", health.getDetails().get("server"));
        assertNull(health.getDetails().get("lastError"));
    }

    @Test
    void health_isDownAndIncludesLastErrorWhileReconnecting() {
        Connection connection = mock(Connection.class);
        when(factory.getCachedConnection()).thenReturn(connection);
        when(connection.getStatus()).thenReturn(Connection.Status.RECONNECTING);
        when(connection.getLastError()).thenReturn("User Authentication Expired");

        var health = healthIndicator.health();

        assertEquals(Status.DOWN, health.getStatus());
        assertEquals("RECONNECTING", health.getDetails().get("status"));
        assertEquals("User Authentication Expired", health.getDetails().get("lastError"));
    }
}
