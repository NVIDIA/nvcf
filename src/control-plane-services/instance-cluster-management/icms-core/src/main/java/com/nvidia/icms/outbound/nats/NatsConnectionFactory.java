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

import com.nvidia.icms.configuration.bean.NatsConfigurationProperties;
import io.nats.client.Connection;
import io.nats.client.ConnectionListener;
import io.nats.client.ConnectionListener.Events;
import io.nats.client.ErrorListener;
import io.nats.client.ForceReconnectOptions;
import io.nats.client.Nats;
import io.nats.client.Options;
import java.io.IOException;
import java.time.Duration;
import java.util.concurrent.CompletableFuture;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.RandomUtils;
import org.springframework.stereotype.Component;

/**
 * Factory class for managing connections to a NATS server.
 * This class provides methods to establish, configure, and manage a connection to the NATS server.
 * It ensures a single connection instance is reused and handles reconnection logic when necessary.
 */
@Component
@Slf4j
public class NatsConnectionFactory {

    private final NatsConfigurationProperties natsConfigurationProperties;

    // Instance of the NATS connection
    // volatile needs to safely read without synchronization in getCachedConnection method
    private volatile Connection natsConnection;

    /**
     * Constructor to initialize the NATS connection factory with configuration properties.
     *
     * @param natsConfigurationProperties Configuration properties for NATS connection.
     */
    public NatsConnectionFactory(NatsConfigurationProperties natsConfigurationProperties) {
        this.natsConfigurationProperties = natsConfigurationProperties;
    }

    /**
     * Establishes a connection to the NATS server.
     * If a connection already exists, it reuses the existing connection.
     *
     * @return Connection object representing the active connection to the NATS server.
     * @throws IOException          If an I/O error occurs during the connection process.
     * @throws InterruptedException If the connection attempt is interrupted.
     */
    public synchronized Connection createConnectionIfNeeded() throws IOException, InterruptedException {
        if (natsConnection == null || natsConnection.getStatus() == Connection.Status.CLOSED) {
            natsConnection = connectToNats();
        }
        return natsConnection;
    }

    /**
     * Drops the cached connection so the next {@link #createConnectionIfNeeded()} call
     * re-handshakes. Used during startup retry when a prior connect failed
     * (e.g. NATS auth-callout was not yet ready) — the cached failure
     * connection is unusable and must be discarded before the retry.
     */
    public synchronized void resetConnection() {
        if (natsConnection != null) {
            try {
                natsConnection.close();
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                log.warn("Interrupted while closing stale NATS connection", e);
            } finally {
                // finally block will be executed even if we interrupt the tread
                natsConnection = null;
            }
        }
    }

    /**
     * Removes a terminal connection from the cache without disturbing a newer connection that
     * may already have replaced it.
     */
    synchronized void invalidateClosedConnection(Connection connection) {
        if (natsConnection == connection && connection.getStatus() == Connection.Status.CLOSED) {
            natsConnection = null;
        }
    }

    /**
     * Returns the current connection without creating one. Intended for health reporting.
     */
    Connection getCachedConnection() {
        return natsConnection;
    }

    Connection connectToNats()
            throws IOException, InterruptedException {
        if (natsConfigurationProperties.isNatsEnabled()) {
            Options options = createDefaultOptions(natsConfigurationProperties.isReconnectAllowed());

            try {
                log.info("NATS: Connecting to server {}", options.getServers().getFirst());
                Connection connection = Nats.connect(options);
                log.info("NATS: Successfully connected to server {}",
                         options.getServers().getFirst());
                return connection;
            } catch (IOException e) {
                log.error("NATS: Failed to connect to server {}: {}", options.getServers().getFirst(), e.getMessage(), e);
                throw e;
            }
        }
        else {
            return null;
        }
    }

    /**
     * Creates default connection options for the NATS client.
     * Allows customization of reconnection behavior and other connection parameters.
     *
     * @param allowReconnect Boolean flag to enable or disable reconnections.
     * @return Options object containing the connection configuration.
     */
    Options createDefaultOptions(Boolean allowReconnect) {
        Options.Builder builder = new Options.Builder()
                // Set the NATS server URL
                .server(natsConfigurationProperties.getNatsUrl())
                // Set connection timeout
                .connectionTimeout(natsConfigurationProperties.getConnectionTimeout())
                // Set ping interval
                .pingInterval(natsConfigurationProperties.getPingInterval())
                .useDispatcherWithExecutor()
                .reconnectWait(natsConfigurationProperties.getReconnectWait())
                .errorListener(new LoggingNatsErrorListener())
                .connectionListener(getNatsConnectionListener());

        if (natsConfigurationProperties.getReconnectJitter().isPositive()) {
            Duration reconnectJitter = natsConfigurationProperties.getReconnectJitter();
            builder.reconnectJitter(reconnectJitter).reconnectJitterTls(reconnectJitter);
        }

        // Configure reconnection behavior based on the allowReconnect flag
        if (!allowReconnect) {
            builder = builder.noReconnect(); // Disable reconnections
        } else {
            builder = builder.maxReconnects(-1); // Allow unlimited reconnections
        }
        if (natsConfigurationProperties.getNkeySeed().isPresent()) {
            var authHandler = Nats.staticCredentials(null,
                                                     natsConfigurationProperties.getNkeySeed()
                                                             .get().toCharArray());
            builder = builder.authHandler(authHandler);
        }

        return builder.build();
    }

    /**
     * Provides a connection listener to handle NATS connection events.
     * Specifically handles "LAME_DUCK" events by forcing a reconnection with a jittered delay.
     *
     * @return ConnectionListener instance to handle connection events.
     */
    private ConnectionListener getNatsConnectionListener() {
        return (connection, events) -> {
            log.info("NATS connection event {} {}", connection.getServerInfo(), events);
            if (events == Events.CLOSED) {
                invalidateClosedConnection(connection);
            } else if (events == Events.LAME_DUCK) {
                CompletableFuture.runAsync(() -> {
                    try {
                        // Add jitter to avoid simultaneous reconnections
                        Thread.sleep(RandomUtils.nextInt(0, 5000));
                        log.info("Client ID {} force reconnecting to NATS",
                                 connection.getServerInfo().getClientId());
                        connection.forceReconnect(
                                ForceReconnectOptions.builder()
                                        .flush(natsConfigurationProperties.getForceReconnectFlush())
                                        .build());
                        log.info("Client ID {} reconnected to NATS",
                                 connection.getServerInfo().getClientId());
                    } catch (InterruptedException e) {
                        Thread.currentThread().interrupt();
                        log.warn("Client ID {} reconnect interrupted",
                                 connection.getServerInfo().getClientId(), e);
                        throw new RuntimeException(e);
                    } catch (Exception e) {
                        log.warn("Client ID {} failed to reconnect to NATS",
                                 connection.getServerInfo().getClientId(), e);
                        throw new RuntimeException(e);
                    }
                });
            }
        };
    }

    /**
     * Custom error listener for logging NATS errors and exceptions.
     */
    @Slf4j
    static class LoggingNatsErrorListener implements ErrorListener {

        /**
         * Logs errors that occur during the NATS connection lifecycle.
         *
         * @param conn  The NATS connection where the error occurred.
         * @param error The error message.
         */
        @Override
        public void errorOccurred(Connection conn, String error) {
            log.error("NATS error occurred {} {}", conn.getServerInfo(), error);
        }

        /**
         * Logs exceptions that occur during the NATS connection lifecycle.
         *
         * @param conn The NATS connection where the exception occurred.
         * @param exp  The exception instance.
         */
        @Override
        public void exceptionOccurred(Connection conn, Exception exp) {
            log.error("For NATS or server: {} error {} : ", conn.getServerInfo(), exp.getMessage(), exp);
        }
    }
}
