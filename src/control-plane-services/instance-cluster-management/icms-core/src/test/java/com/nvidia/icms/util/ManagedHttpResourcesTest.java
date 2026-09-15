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
package com.nvidia.icms.util;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.util.OAuth2ClientUtils.ManagedHttpResources;
import java.time.Duration;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.http.client.reactive.ClientHttpConnector;
import reactor.core.publisher.Mono;
import reactor.netty.resources.ConnectionProvider;
import reactor.netty.resources.LoopResources;

/**
 * Tests for {@link ManagedHttpResources}. These verify disposal semantics without
 * actually allocating real Netty pools, so they run in milliseconds.
 */
@ExtendWith(MockitoExtension.class)
class ManagedHttpResourcesTest {

    @Mock
    private ClientHttpConnector connector;

    @Mock
    private ConnectionProvider connectionProvider;

    @Mock
    private LoopResources loopResources;

    @Test
    void close_invokesDisposeLaterOnBothResources() {
        // ConnectionProvider.disposeLater() has only a no-arg overload in
        // reactor-netty-core; LoopResources supports graceful two-arg disposal.
        when(connectionProvider.disposeLater()).thenReturn(Mono.empty());
        when(loopResources.disposeLater(
                eq(ManagedHttpResources.QUIET_PERIOD),
                eq(ManagedHttpResources.DISPOSE_TIMEOUT)))
                .thenReturn(Mono.empty());

        var resources = new ManagedHttpResources(connector, connectionProvider,
                loopResources, "ngc");
        resources.close();

        verify(connectionProvider).disposeLater();
        verify(loopResources).disposeLater(
                ManagedHttpResources.QUIET_PERIOD, ManagedHttpResources.DISPOSE_TIMEOUT);
    }

    @Test
    void close_swallowsException_whenDisposeFails() {
        // Simulate Reactor Netty failing during disposal (e.g. shutdown interruption).
        // close() must log a warning and continue on to dispose LoopResources
        // rather than propagating the exception to the caller.
        when(connectionProvider.disposeLater())
                .thenReturn(Mono.error(new RuntimeException("simulated dispose failure")));
        when(loopResources.disposeLater(
                eq(ManagedHttpResources.QUIET_PERIOD),
                eq(ManagedHttpResources.DISPOSE_TIMEOUT)))
                .thenReturn(Mono.empty());

        var resources = new ManagedHttpResources(connector, connectionProvider,
                loopResources, "ngc");

        assertDoesNotThrow(resources::close);

        // Even though ConnectionProvider.disposeLater failed, LoopResources must
        // still be disposed — close() must not short-circuit on the first failure.
        verify(loopResources).disposeLater(
                ManagedHttpResources.QUIET_PERIOD, ManagedHttpResources.DISPOSE_TIMEOUT);
    }

    @Test
    void close_isNoOp_whenBothResourcesNull() {
        // The test-only constructors of @RefreshScope clients leave these null;
        // close() must not NPE.
        var resources = new ManagedHttpResources(null, null, null, "test");
        assertDoesNotThrow(resources::close);
    }

    @Test
    void connector_returnsInjectedInstance() {
        var resources = new ManagedHttpResources(connector, null, null, "test");
        assertSame(connector, resources.connector());
    }

    @Test
    void timeoutConstants_areInternallyConsistent() {
        // BLOCK_TIMEOUT must exceed DISPOSE_TIMEOUT so block() doesn't trip
        // before the underlying Mono has a chance to complete on its own.
        assertTrue(ManagedHttpResources.BLOCK_TIMEOUT.compareTo(
                ManagedHttpResources.DISPOSE_TIMEOUT) > 0,
                "BLOCK_TIMEOUT must be strictly greater than DISPOSE_TIMEOUT");

        // DISPOSE_TIMEOUT must stay below Spring's default 30s lifecycle-per-phase.
        assertTrue(ManagedHttpResources.DISPOSE_TIMEOUT.compareTo(Duration.ofSeconds(30)) < 0,
                "DISPOSE_TIMEOUT must stay below spring.lifecycle.timeout-per-shutdown-phase (30s)");
    }
}
