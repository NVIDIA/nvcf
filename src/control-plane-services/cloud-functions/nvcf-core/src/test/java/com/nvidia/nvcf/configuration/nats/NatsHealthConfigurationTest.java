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
import java.io.IOException;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.boot.health.contributor.HealthIndicator;
import org.springframework.context.ApplicationContext;

class NatsHealthConfigurationTest {

    // Readiness fails while NATS is unavailable; liveness fails only for a closed connection,
    // which jnats never reopens.
    @Test
    void livenessFailsOnlyWhenAConnectionIsClosed() throws IOException {
        var connection = mock(Connection.class);
        var pool = pool(connection);
        var liveness = new NatsHealthConfiguration()
                .natsLivenessHealthIndicator(Map.of("fixedNatsPool", pool));
        var readiness = new NatsHealthIndicator(pool);

        when(connection.getStatus()).thenReturn(Status.CONNECTED);
        assertThat(status(liveness)).isEqualTo("UP");
        assertThat(status(readiness)).isEqualTo("UP");

        when(connection.getStatus()).thenReturn(Status.RECONNECTING);
        assertThat(status(liveness)).isEqualTo("UP");
        assertThat(status(readiness)).isEqualTo("DOWN");

        when(connection.getStatus()).thenReturn(Status.CLOSED);
        assertThat(status(liveness)).isEqualTo("DOWN");
        assertThat(status(readiness)).isEqualTo("DOWN");
    }

    private static FixedNatsPool pool(Connection connection) throws IOException {
        var context = mock(ApplicationContext.class);
        when(context.getBean(Connection.class)).thenReturn(connection);
        var properties = new NatsProperties();
        properties.setMaxPoolSize(1);
        return new FixedNatsPool(context, properties);
    }

    private static String status(HealthIndicator indicator) {
        return indicator.health().getStatus().getCode();
    }
}
