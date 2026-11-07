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
package com.nvidia.nvcf.configuration.nats;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nvidia.nvcf.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.configuration.nats.NatsHealthConfiguration.NatsHealthIndicator;
import io.nats.client.Connection;
import io.nats.client.Connection.Status;
import io.nats.client.ConnectionListener.Events;
import io.nats.client.ErrorListener;
import java.io.IOException;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.context.ApplicationContext;

class NatsPoolHealthTest {

    private NatsFailureTelemetry telemetry;
    private Connection connection;
    private FixedNatsPool pool;

    @SuppressWarnings("unchecked")
    @BeforeEach
    void setUp() throws IOException, InterruptedException {
        telemetry = new NatsFailureTelemetry(mock(ObjectProvider.class));
        connection = mock(Connection.class);
        when(connection.getStatus()).thenReturn(Status.CONNECTED);
        var context = mock(ApplicationContext.class);
        when(context.getBean(Connection.class)).thenReturn(connection);
        var properties = new NatsProperties();
        properties.setMaxPoolSize(1);
        pool = new FixedNatsPool(context, properties, telemetry);
    }

    private String healthStatus() {
        return new NatsHealthIndicator(pool).health().getStatus().getCode();
    }

    @Test
    void connectedPoolIsUpWithoutReason() {
        assertThat(pool.healthy()).isTrue();
        assertThat(pool.lastFailureReason()).isEmpty();
        assertThat(new NatsHealthIndicator(pool).health().getStatus().getCode())
                .isEqualTo("UP");
    }

    @Test
    void disconnectedPoolIsDownAndReportsLastFailureReason() {
        ErrorListener listener = telemetry.errorListener();
        listener.errorOccurred(connection, "Authorization Violation");
        when(connection.getStatus()).thenReturn(Status.DISCONNECTED);

        var health = new NatsHealthIndicator(pool).health();

        assertThat(pool.healthy()).isFalse();
        assertThat(health.getStatus().getCode()).isEqualTo("DOWN");
        assertThat(health.getDetails()).containsEntry("reason", "authentication");
    }

    @Test
    void disconnectedPoolWithoutKnownReasonIsDownWithoutDetail() {
        when(connection.getStatus()).thenReturn(Status.DISCONNECTED);

        var health = new NatsHealthIndicator(pool).health();

        assertThat(health.getStatus().getCode()).isEqualTo("DOWN");
        assertThat(health.getDetails()).doesNotContainKey("reason");
    }

    @Test
    void poolRecoversAfterReconnect() {
        telemetry.errorListener().errorOccurred(connection, "Authorization Violation");
        when(connection.getStatus()).thenReturn(Status.DISCONNECTED);
        assertThat(pool.healthy()).isFalse();

        when(connection.getStatus()).thenReturn(Status.CONNECTED);
        telemetry.onConnectionEvent(connection, Events.RECONNECTED);

        assertThat(pool.healthy()).isTrue();
        assertThat(pool.lastFailureReason()).isEmpty();
        assertThat(healthStatus()).isEqualTo("UP");
        assertThat(telemetry.lastFailure(connection)).isEmpty();
    }
}
