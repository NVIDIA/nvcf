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

import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import io.nats.client.AuthenticationException;
import io.nats.client.Connection;
import io.nats.client.ConnectionListener.Events;
import io.nats.client.ErrorListener;
import java.io.IOException;
import java.net.SocketTimeoutException;
import java.security.cert.CertificateExpiredException;
import javax.net.ssl.SSLHandshakeException;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.ObjectProvider;

class NatsFailureTelemetryTest {

    private final MeterRegistry registry = new SimpleMeterRegistry();
    private NatsFailureTelemetry telemetry;
    private ErrorListener listener;
    private Connection connA;
    private Connection connB;

    @SuppressWarnings("unchecked")
    @BeforeEach
    void setUp() {
        ObjectProvider<MeterRegistry> provider = mock(ObjectProvider.class);
        when(provider.getIfAvailable()).thenReturn(registry);
        telemetry = new NatsFailureTelemetry(provider);
        listener = telemetry.errorListener();
        connA = mock(Connection.class);
        connB = mock(Connection.class);
    }

    private double count(String reason, String source) {
        var counter = registry.find(NatsFailureTelemetry.FAILURES_METRIC)
                .tags("reason", reason, "source", source).counter();
        return counter == null ? 0 : counter.count();
    }

    @Test
    void connectFailureIsCountedWithReasonAndSource() {
        var reason = telemetry.recordConnectFailure(new AuthenticationException("denied"));

        assertThat(reason).isEqualTo(NatsFailureReason.AUTHENTICATION);
        assertThat(count("authentication", "connect")).isEqualTo(1);
    }

    @Test
    void connectTimeoutIsCounted() {
        telemetry.recordConnectFailure(new IOException(new SocketTimeoutException()));

        assertThat(count("timeout", "connect")).isEqualTo(1);
    }

    @Test
    void runtimeErrorStringIsCountedAndRemembered() {
        listener.errorOccurred(connA, "Authorization Violation");

        assertThat(count("authentication", "event")).isEqualTo(1);
        assertThat(telemetry.lastFailure(connA)).contains(NatsFailureReason.AUTHENTICATION);
    }

    @Test
    void runtimeExpiredCertificateIsCountedAndRemembered() {
        var handshake = new SSLHandshakeException("handshake failed");
        handshake.initCause(new CertificateExpiredException());

        listener.exceptionOccurred(connA, handshake);

        assertThat(count("tls_cert_expired", "event")).isEqualTo(1);
        assertThat(telemetry.lastFailure(connA)).contains(NatsFailureReason.TLS_CERT_EXPIRED);
    }

    @Test
    void disconnectKeepsReasonAndReconnectClearsIt() {
        listener.errorOccurred(connA, "Authorization Violation");

        telemetry.onConnectionEvent(connA, Events.DISCONNECTED);
        assertThat(telemetry.lastFailure(connA)).contains(NatsFailureReason.AUTHENTICATION);

        telemetry.onConnectionEvent(connA, Events.RECONNECTED);
        assertThat(telemetry.lastFailure(connA)).isEmpty();
    }

    @Test
    void closedClearsReason() {
        listener.errorOccurred(connA, "Authorization Violation");

        telemetry.onConnectionEvent(connA, Events.CLOSED);

        assertThat(telemetry.lastFailure(connA)).isEmpty();
    }

    @Test
    void reasonsAreTrackedPerConnection() {
        listener.errorOccurred(connA, "Authorization Violation");

        telemetry.onConnectionEvent(connB, Events.RECONNECTED);

        assertThat(telemetry.lastFailure(connA)).contains(NatsFailureReason.AUTHENTICATION);
    }

    @SuppressWarnings("unchecked")
    @Test
    void missingMeterRegistryDoesNotBreakClassification() {
        ObjectProvider<MeterRegistry> provider = mock(ObjectProvider.class);
        var noMetrics = new NatsFailureTelemetry(provider);

        noMetrics.errorListener().errorOccurred(connA, "Authorization Violation");

        assertThat(noMetrics.lastFailure(connA)).contains(NatsFailureReason.AUTHENTICATION);
        assertThat(noMetrics.recordConnectFailure(new IOException("x")))
                .isEqualTo(NatsFailureReason.CONNECTION);
    }
}
