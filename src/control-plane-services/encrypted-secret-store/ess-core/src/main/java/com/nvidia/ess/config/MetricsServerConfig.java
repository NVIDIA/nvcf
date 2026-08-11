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

import io.micrometer.prometheusmetrics.PrometheusMeterRegistry;
import java.util.concurrent.atomic.AtomicReference;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.web.server.context.WebServerApplicationContext;
import org.springframework.context.SmartLifecycle;
import org.springframework.context.annotation.Configuration;
import reactor.core.publisher.Mono;
import reactor.netty.DisposableServer;
import reactor.netty.http.server.HttpServer;

/**
 * Dedicated Reactor Netty server for Prometheus metrics scraping, separate from the
 * main application port (8080).
 * 
 * Separated due to tech debt: we keep the management port same as the application port for "health" and "info" endpoints to avoid exposing the metrics endpoint too.
 *
 * Technically similar to OTEL agent's Prometheus endpoint since OTEL does not make use of Spring boot + web abstractions either
 * 
 */
@Configuration
@ConditionalOnProperty(name = "metrics.server.enabled", havingValue = "true")
@Slf4j
public class MetricsServerConfig implements SmartLifecycle {

    private static final String PROMETHEUS_CONTENT_TYPE = "text/plain; version=0.0.4; charset=utf-8"; // PrometheusMeterRegistry.Format.TEXT_004

    private final PrometheusMeterRegistry registry;
    private final int port;
    private final String path;
    private final AtomicReference<DisposableServer> server = new AtomicReference<>();

    // default match OTEL javaagent for backwards compatibility
    public MetricsServerConfig(
            PrometheusMeterRegistry registry,
            @Value("${metrics.server.port:9464}") int port,
            @Value("${metrics.server.path:/metrics}") String path) {
        if (port < 0 || port > 65535) {
            throw new IllegalArgumentException("Port must be between 0 (random port) and 65535, got: " + port);
        }
        if (path == null || path.trim().isEmpty()) {
            throw new IllegalArgumentException("Path must not be null or empty");
        }
        if (!path.startsWith("/")) {
            throw new IllegalArgumentException("Path must start with '/', got: " + path);
        }

        this.registry = registry;
        this.port = port;
        this.path = path;
    }

    @Override
    public void start() {
        if (isRunning()) {
            log.warn("Dedicated metrics server already running on port {}", boundPort());
            return;
        }

        synchronized (this) {        
            if (isRunning()) {
                log.warn("Dedicated metrics server already running on port {}", boundPort());
                return;
            }
            DisposableServer bound;

            try {
                bound = HttpServer.create()
                        .port(port)
                        .route(routes -> routes.get(path, (req, res) ->
                                res.header("Content-Type", PROMETHEUS_CONTENT_TYPE)
                                        .sendString(Mono.fromSupplier(registry::scrape))))
                        .bindNow();
            } catch (Exception ex) {
                throw new IllegalStateException(
                        "Failed to start metrics server on port " + port + ": " + ex.getMessage(),
                        ex);
            }
            server.set(bound);
            log.info("Dedicated metrics server started on port {}", bound.port());
        }
    }

    @Override
    public void stop() {
        if (!isRunning()) {
            log.warn("Server already stopped");
            return;
        }

        synchronized (this) {
            if (!isRunning()) {
                log.warn("Server already stopped");
                return;
            }
            var s = server.get();
            s.disposeNow();
            server.set(null);
            log.info("Dedicated metrics server stopped");
        }
    }

    @Override
    public boolean isRunning() {
        DisposableServer s = server.get();
        return s != null && !s.isDisposed();
    }

    /**
     * Lifecycle phase matches {@code ChildManagementContextInitializer} so the metrics
     * server stops around the same time as the management port server. Anchored to the web
     * server's graceful-shutdown phase and offset just below it so this server stops alongside
     * the management port server.
     */
    @Override
    public int getPhase() {
        return WebServerApplicationContext.GRACEFUL_SHUTDOWN_PHASE - 512;
    }

    int boundPort() {
        DisposableServer s = server.get();
        return s != null ? s.port() : -1;
    }
}
