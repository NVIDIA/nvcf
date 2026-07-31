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

import io.nats.client.Connection;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthIndicator;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.stereotype.Component;

/** Reports whether the NATS connection used for NVCA messaging is currently usable. */
@Component
@ConditionalOnProperty(prefix = "icms.nats", name = "nats-enabled", havingValue = "true")
public class NatsHealthIndicator implements HealthIndicator {

    private final NatsConnectionFactory natsConnectionFactory;

    public NatsHealthIndicator(NatsConnectionFactory natsConnectionFactory) {
        this.natsConnectionFactory = natsConnectionFactory;
    }

    @Override
    public Health health() {
        Connection connection = natsConnectionFactory.getCachedConnection();
        if (connection == null) {
            return Health.down().withDetail("status", "NOT_INITIALIZED").build();
        }

        Connection.Status status = connection.getStatus();
        Health.Builder health = status == Connection.Status.CONNECTED ? Health.up() : Health.down();
        health.withDetail("status", status == null ? "UNKNOWN" : status.name());

        String connectedUrl = connection.getConnectedUrl();
        if (StringUtils.isNotBlank(connectedUrl)) {
            health.withDetail("server", connectedUrl);
        }
        String lastError = connection.getLastError();
        if (StringUtils.isNotBlank(lastError)) {
            health.withDetail("lastError", lastError);
        }
        return health.build();
    }
}
