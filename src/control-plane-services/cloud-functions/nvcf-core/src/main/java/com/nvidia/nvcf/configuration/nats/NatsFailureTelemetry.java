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

import io.micrometer.core.instrument.MeterRegistry;
import io.nats.client.Connection;
import io.nats.client.ConnectionListener.Events;
import io.nats.client.ErrorListener;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.stereotype.Component;

/**
 * Records classified NATS failures as {@code nats.failures{reason,source}} and remembers the last
 * failure per connection so pool health can report why a connection is down.
 */
@Slf4j
@Component
public class NatsFailureTelemetry {

    static final String FAILURES_METRIC = "nats.failures";
    static final String SOURCE_CONNECT = "connect";
    static final String SOURCE_EVENT = "event";

    private final ObjectProvider<MeterRegistry> meterRegistry;
    private final Map<Connection, NatsFailureReason> lastFailures = new ConcurrentHashMap<>();

    public NatsFailureTelemetry(ObjectProvider<MeterRegistry> meterRegistry) {
        this.meterRegistry = meterRegistry;
    }

    public NatsFailureReason recordConnectFailure(Exception error) {
        NatsFailureReason reason = NatsFailureClassifier.classify(error);
        count(reason, SOURCE_CONNECT);
        return reason;
    }

    public ErrorListener errorListener() {
        return new ErrorListener() {
            @Override
            public void errorOccurred(Connection conn, String error) {
                NatsFailureReason reason = NatsFailureClassifier.classifyMessage(error);
                recordEventFailure(conn, reason);
                log.error("nats error occurred {} reason={} {}", conn.getServerInfo(),
                          reason.tag(), error);
            }

            @Override
            public void exceptionOccurred(Connection conn, Exception exp) {
                NatsFailureReason reason = NatsFailureClassifier.classify(exp);
                recordEventFailure(conn, reason);
                log.error("nats exception occurred {} reason={}", conn.getServerInfo(),
                          reason.tag(), exp);
            }
        };
    }

    public void onConnectionEvent(Connection conn, Events type) {
        if (type == Events.CONNECTED || type == Events.RECONNECTED || type == Events.CLOSED) {
            lastFailures.remove(conn);
        }
    }

    public Optional<NatsFailureReason> lastFailure(Connection conn) {
        return Optional.ofNullable(lastFailures.get(conn));
    }

    private void recordEventFailure(Connection conn, NatsFailureReason reason) {
        lastFailures.put(conn, reason);
        count(reason, SOURCE_EVENT);
    }

    private void count(NatsFailureReason reason, String source) {
        MeterRegistry registry = meterRegistry.getIfAvailable();
        if (registry != null) {
            registry.counter(FAILURES_METRIC, "reason", reason.tag(), "source", source)
                    .increment();
        }
    }
}
