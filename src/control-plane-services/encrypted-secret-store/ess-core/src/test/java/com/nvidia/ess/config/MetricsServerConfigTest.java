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
package com.nvidia.ess.config;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import io.micrometer.core.instrument.Counter;
import io.micrometer.prometheusmetrics.PrometheusConfig;
import io.micrometer.prometheusmetrics.PrometheusMeterRegistry;
import java.net.ServerSocket;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.parallel.Execution;
import org.junit.jupiter.api.parallel.ExecutionMode;
import org.springframework.boot.web.server.context.WebServerApplicationContext;
import reactor.netty.http.client.HttpClient;

@Execution(ExecutionMode.SAME_THREAD)
class MetricsServerConfigTest {

    private static PrometheusMeterRegistry newRegistry() {
        return new PrometheusMeterRegistry(PrometheusConfig.DEFAULT);
    }

    @Test
    void start_shouldBindServerAndExposeMetrics() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            Counter.builder("test_total").register(registry);
            config.start();

            assertThat(config.isRunning()).isTrue();
            assertThat(config.boundPort()).isPositive();

            String body = HttpClient.create()
                    .get()
                    .uri("http://localhost:" + config.boundPort() + "/metrics")
                    .responseContent()
                    .aggregate()
                    .asString()
                    .block();

            assertThat(body).contains("test_total");
        } finally {
            config.stop();
            registry.close();
        }
    }

    @Test
    void start_shouldServePrometheusContentType() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            config.start();

            String contentType = HttpClient.create()
                    .get()
                    .uri("http://localhost:" + config.boundPort() + "/metrics")
                    .response()
                    .map(res -> res.responseHeaders().get("Content-Type"))
                    .block();

            assertThat(contentType).isEqualTo("text/plain; version=0.0.4; charset=utf-8");
        } finally {
            config.stop();
            registry.close();
        }
    }

    @Test
    void start_shouldRespectCustomPath() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/custom");
        try {
            Counter.builder("custom_check_total").register(registry);
            config.start();

            Integer status = HttpClient.create()
                    .get()
                    .uri("http://localhost:" + config.boundPort() + "/custom")
                    .response()
                    .map(res -> res.status().code())
                    .block();

            assertThat(status).isEqualTo(200);
        } finally {
            config.stop();
            registry.close();
        }
    }

    @Test
    void stop_shouldDisposeServer() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            config.start();
            assertThat(config.isRunning()).isTrue();

            config.stop();

            assertThat(config.isRunning()).isFalse();
            assertThat(config.boundPort()).isEqualTo(-1);
        } finally {
            config.stop();
            registry.close();
        }
    }

    @Test
    void stop_whenNotStarted_shouldBeNoOp() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            config.stop();
            assertThat(config.isRunning()).isFalse();
        } finally {
            registry.close();
        }
    }

    @Test
    void isRunning_beforeStart_shouldReturnFalse() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            assertThat(config.isRunning()).isFalse();
        } finally {
            registry.close();
        }
    }

    @Test
    void boundPort_beforeStart_shouldReturnNegativeOne() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            assertThat(config.boundPort()).isEqualTo(-1);
        } finally {
            registry.close();
        }
    }

    @Test
    void getPhase_shouldMatchExpectedLifecyclePhase() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            int expected = WebServerApplicationContext.GRACEFUL_SHUTDOWN_PHASE - 512;
            assertThat(config.getPhase()).isEqualTo(expected);
        } finally {
            registry.close();
        }
    }

    @Test
    void start_whenAlreadyRunning_shouldBeIdempotent() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            config.start();
            int firstPort = config.boundPort();

            config.start();

            assertThat(config.boundPort()).isEqualTo(firstPort);
            assertThat(config.isRunning()).isTrue();
        } finally {
            config.stop();
            registry.close();
        }
    }

    @Test
    void start_whenPortAlreadyInUse_shouldThrowWithContext() throws Exception {
        var registry = newRegistry();
        try (var blocker = new ServerSocket(0)) {
            int occupiedPort = blocker.getLocalPort();
            var config = new MetricsServerConfig(registry, occupiedPort, "/metrics");

            assertThatThrownBy(config::start)
                    .isInstanceOf(IllegalStateException.class)
                    .hasMessageContaining("Failed to start metrics server on port " + occupiedPort);
        } finally {
            registry.close();
        }
    }

    @Test
    void constructor_shouldRejectNegativePort() {
        var registry = newRegistry();
        try {
            assertThatThrownBy(() -> new MetricsServerConfig(registry, -1, "/metrics"))
                    .isInstanceOf(IllegalArgumentException.class)
                    .hasMessageContaining("Port must be between");
        } finally {
            registry.close();
        }
    }

    @Test
    void constructor_shouldRejectPortAbove65535() {
        var registry = newRegistry();
        try {
            assertThatThrownBy(() -> new MetricsServerConfig(registry, 65536, "/metrics"))
                    .isInstanceOf(IllegalArgumentException.class)
                    .hasMessageContaining("Port must be between");
        } finally {
            registry.close();
        }
    }

    @Test
    void constructor_shouldAcceptZeroPort() {
        var registry = newRegistry();
        try {
            var config = new MetricsServerConfig(registry, 0, "/metrics");
            assertThat(config).isNotNull();
        } finally {
            registry.close();
        }
    }

    @Test
    void constructor_shouldRejectNullPath() {
        var registry = newRegistry();
        try {
            assertThatThrownBy(() -> new MetricsServerConfig(registry, 0, null))
                    .isInstanceOf(IllegalArgumentException.class)
                    .hasMessageContaining("Path must not be null or empty");
        } finally {
            registry.close();
        }
    }

    @Test
    void constructor_shouldRejectBlankPath() {
        var registry = newRegistry();
        try {
            assertThatThrownBy(() -> new MetricsServerConfig(registry, 0, "   "))
                    .isInstanceOf(IllegalArgumentException.class)
                    .hasMessageContaining("Path must not be null or empty");
        } finally {
            registry.close();
        }
    }

    @Test
    void constructor_shouldRejectPathWithoutLeadingSlash() {
        var registry = newRegistry();
        try {
            assertThatThrownBy(() -> new MetricsServerConfig(registry, 0, "metrics"))
                    .isInstanceOf(IllegalArgumentException.class)
                    .hasMessageContaining("Path must start with '/'");
        } finally {
            registry.close();
        }
    }

    @Test
    void stop_afterStop_onPreviouslyStartedServer_shouldBeIdempotent() {
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        try {
            config.start();
            assertThat(config.isRunning()).isTrue();

            config.stop();
            config.stop();

            assertThat(config.isRunning()).isFalse();
            assertThat(config.boundPort()).isEqualTo(-1);
        } finally {
            registry.close();
        }
    }

    /**
     * Races many threads calling {@code start()} simultaneously. Exactly one should bind
     * the server, and every other invocation must observe the already-running state without
     * throwing or leaking a duplicate {@code DisposableServer}.
     */
    @Test
    void start_concurrentInvocations_shouldBindExactlyOnce() throws Exception {
        int threadCount = 16;
        var registry = newRegistry();
        var config = new MetricsServerConfig(registry, 0, "/metrics");
        var startGate = new CountDownLatch(1);
        var doneGate = new CountDownLatch(threadCount);
        var exceptions = new CopyOnWriteArrayList<Throwable>();
        ExecutorService executor = Executors.newFixedThreadPool(threadCount);
        try {
            for (int i = 0; i < threadCount; i++) {
                executor.submit(() -> {
                    try {
                        startGate.await();
                        config.start();
                    } catch (Throwable t) {
                        exceptions.add(t);
                    } finally {
                        doneGate.countDown();
                    }
                });
            }
            startGate.countDown();
            assertThat(doneGate.await(10, TimeUnit.SECONDS)).isTrue();

            assertThat(exceptions).isEmpty();
            assertThat(config.isRunning()).isTrue();
            assertThat(config.boundPort()).isPositive();

            int port = config.boundPort();
            config.start();
            assertThat(config.boundPort()).isEqualTo(port);
        } finally {
            executor.shutdownNow();
            config.stop();
            registry.close();
        }
    }
}
