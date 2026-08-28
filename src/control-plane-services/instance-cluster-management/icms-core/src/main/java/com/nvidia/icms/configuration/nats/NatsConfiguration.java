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
package com.nvidia.icms.configuration.nats;

import static org.springframework.beans.factory.config.BeanDefinition.SCOPE_PROTOTYPE;

import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.tracing.Tracer;
import io.nats.client.Connection;
import io.nats.client.Connection.Status;
import io.nats.client.ConnectionListener;
import io.nats.client.ConnectionListener.Events;
import io.nats.client.ErrorListener;
import io.nats.client.ForceReconnectOptions;
import io.nats.client.JetStream;
import io.nats.client.JetStreamManagement;
import io.nats.client.Nats;
import io.nats.client.Options;
import io.nats.client.Statistics;
import io.nats.client.impl.TracedNatsConnection;
import java.io.IOException;
import java.time.Duration;
import java.util.AbstractCollection;
import java.util.Arrays;
import java.util.Collection;
import java.util.Iterator;
import java.util.List;
import java.util.Objects;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.ToDoubleFunction;
import java.util.function.ToLongFunction;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.RandomUtils;
import org.springframework.beans.factory.InitializingBean;
import org.springframework.context.ApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Scope;
import org.springframework.stereotype.Service;

@Slf4j
@Configuration(proxyBeanMethods = false)
public final class NatsConfiguration {

    private static Connection connect(Options options, Tracer tracer)
            throws IOException, InterruptedException {
        TracedNatsConnection conn = new TracedNatsConnection(options, tracer);
        conn.connect(false);
        return conn;
    }

    @Bean
    @Scope(SCOPE_PROTOTYPE)
    public Connection natsConnection(
            NatsConfigurationProperties natsProperties,
            // only required if the auth callout is enabled to establish ordering
            Tracer tracer)
            throws IOException, InterruptedException {


        var options = createDefaultOptions(natsProperties);
        return connect(options, tracer);
    }

    /**
     * Creates default connection options for the NATS client.
     * Allows customization of reconnection behavior and other connection parameters.
     *
     * @return Options object containing the connection configuration.
     */
    Options createDefaultOptions(NatsConfigurationProperties natsConfigurationProperties) {
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
        if (!natsConfigurationProperties.isReconnectAllowed()) {
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
        return (conn, type) -> {
            log.info("nats connection event {} {}", type,
                     conn.getServerInfo());
            if (type == Events.LAME_DUCK) {
                CompletableFuture.runAsync(() -> {
                    try {
                        // jitter
                        Thread.sleep(RandomUtils.secure().randomInt(0, 5000));
                        // this may cause issues, but hopefully the active force
                        // reconnection is a smaller error window than waiting to get
                        // booted and detecting it normally.
                        log.info("client id {} force reconnecting to nats",
                                 conn.getServerInfo().getClientId());
                        conn.forceReconnect(ForceReconnectOptions.builder()
                                                    .flush(Duration.ofSeconds(5))
                                                    .build());
                        log.info("client id {} reconnected to nats",
                                 conn.getServerInfo().getClientId());
                    } catch (InterruptedException e) {
                        Thread.currentThread().interrupt();
                        log.warn("client id {} reconnect interrupted",
                                 conn.getServerInfo().getClientId(), e);
                        throw new RuntimeException(e);
                    } catch (Exception e) {
                        log.warn("client id {} failed to reconnect to nats",
                                 conn.getServerInfo().getClientId(), e);
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

    @Slf4j
    @Service
    public static class FixedNatsPool implements AutoCloseable {

        private final Connection[] connections;
        private final JetStream[] jetStreams;
        private final JetStreamManagement[] jetStreamManagements;
        private final ApplicationContext applicationContext;
        private final AtomicInteger index = new AtomicInteger();

        public FixedNatsPool(ApplicationContext applicationContext,
                             NatsConfigurationProperties natsProperties) {
            int poolSize = natsProperties.isNatsEnabled()
                    ? Math.min(Runtime.getRuntime().availableProcessors(),
                               natsProperties.getMaxPoolSize())
                    : 0;
            this.applicationContext = applicationContext;
            this.connections = new Connection[poolSize];
            this.jetStreams = new JetStream[poolSize];
            this.jetStreamManagements = new JetStreamManagement[poolSize];
        }

        @Override
        public void close() throws Exception {
            for (Connection connection : connections) {
                if (connection != null) {
                    connection.close();
                }
            }
        }

        private int nextIndex() {
            if (connections.length == 0) {
                throw new IllegalStateException("NATS is not enabled");
            }
            return Math.floorMod(index.getAndIncrement(), connections.length);
        }

        public Connection borrowConnection() throws IOException, InterruptedException {
            return connection(nextIndex());
        }

        public JetStream borrowJetStream() throws IOException, InterruptedException {
            int slot = nextIndex();
            connection(slot);
            return jetStreams[slot];
        }

        public JetStreamManagement borrowJetStreamManagement()
                throws IOException, InterruptedException {
            int slot = nextIndex();
            connection(slot);
            return jetStreamManagements[slot];
        }

        private synchronized Connection connection(int slot)
                throws IOException, InterruptedException {
            Connection connection = connections[slot];
            if (connection == null || connection.getStatus() == Status.CLOSED) {
                connection = applicationContext.getBean(Connection.class);
                try {
                    connection.RTT();
                    JetStream jetStream = connection.jetStream();
                    JetStreamManagement jetStreamManagement =
                            connection.jetStreamManagement();
                    connections[slot] = connection;
                    jetStreams[slot] = jetStream;
                    jetStreamManagements[slot] = jetStreamManagement;
                } catch (IOException | RuntimeException e) {
                    try {
                        connection.close();
                    } catch (InterruptedException closeError) {
                        Thread.currentThread().interrupt();
                        e.addSuppressed(closeError);
                    }
                    throw e;
                }
            }
            return connection;
        }

        public boolean healthy() {
            if (connections.length == 0) {
                return false;
            }
            if (Arrays.stream(connections).allMatch(Objects::isNull)) {
                try {
                    connection(0);
                } catch (IOException | InterruptedException | RuntimeException e) {
                    if (e instanceof InterruptedException) {
                        Thread.currentThread().interrupt();
                    }
                    log.warn("Unable to initialize NATS connection during health check", e);
                    return false;
                }
            }
            boolean initialized = false;
            for (Connection connection : connections) {
                if (connection == null) {
                    continue;
                }
                initialized = true;
                if (connection.getStatus() != Status.CONNECTED) {
                    log.warn("Unhealthy NATS connection {}", connection.getServerInfo());
                    return false;
                }
            }
            return initialized;
        }

        Collection<Statistics> statistics() {
            return new AbstractCollection<>() {
                @Override
                public Iterator<Statistics> iterator() {
                    return Arrays.stream(connections)
                            .filter(Objects::nonNull)
                            .map(Connection::getStatistics)
                            .iterator();
                }

                @Override
                public int size() {
                    return (int) Arrays.stream(connections).filter(Objects::nonNull).count();
                }
            };
        }
    }

    @Configuration(proxyBeanMethods = false)
    static class NatsMetricsConfiguration implements InitializingBean {

        private final Collection<Statistics> statistics;
        private final MeterRegistry meterRegistry;

        NatsMetricsConfiguration(FixedNatsPool fixedNatsPool, MeterRegistry meterRegistry) {
            this.meterRegistry = meterRegistry;
            this.statistics = fixedNatsPool.statistics();
        }

        @Override
        public void afterPropertiesSet() {
            meterRegistry.more().counter("nats.pings", List.of(),
                                         statistics, sumProperties(Statistics::getPings));
            meterRegistry.more().counter("nats.reconnects", List.of(),
                                         statistics, sumProperties(Statistics::getReconnects));
            meterRegistry.more().counter("nats.dropped", List.of(),
                                         statistics, sumProperties(Statistics::getDroppedCount));
            meterRegistry.more().counter("nats.oks", List.of(),
                                         statistics, sumProperties(Statistics::getOKs));
            meterRegistry.more().counter("nats.errs", List.of(),
                                         statistics, sumProperties(Statistics::getErrs));
            meterRegistry.more().counter("nats.exceptions", List.of(),
                                         statistics, sumProperties(Statistics::getExceptions));
            meterRegistry.more().counter("nats.requests.sent", List.of(),
                                         statistics, sumProperties(Statistics::getRequestsSent));
            meterRegistry.more().counter("nats.replies.received", List.of(), statistics,
                                         sumProperties(Statistics::getRepliesReceived));
            meterRegistry.more().counter("nats.replies.received.duplicate", List.of(), statistics,
                                         sumProperties(Statistics::getDuplicateRepliesReceived));
            meterRegistry.more().counter("nats.replies.received.orphan", List.of(), statistics,
                                         sumProperties(Statistics::getOrphanRepliesReceived));
            meterRegistry.more().counter("nats.msgs.in", List.of(),
                                         statistics, sumProperties(Statistics::getInMsgs));
            meterRegistry.more().counter("nats.msgs.out", List.of(),
                                         statistics, sumProperties(Statistics::getOutMsgs));
            meterRegistry.more().counter("nats.bytes.in", List.of(),
                                         statistics, sumProperties(Statistics::getInBytes));
            meterRegistry.more().counter("nats.bytes.out", List.of(),
                                         statistics, sumProperties(Statistics::getOutBytes));
            meterRegistry.more().counter("nats.flush", List.of(),
                                         statistics, sumProperties(Statistics::getFlushCounter));
            meterRegistry.gauge("nats.requests.outstanding", statistics,
                                sumProperties(Statistics::getOutstandingRequests));
        }

        private static <T> ToDoubleFunction<Collection<T>> sumProperties(
                ToLongFunction<T> propertyExtractor) {
            return collection -> collection.stream().mapToLong(propertyExtractor).sum();
        }
    }
}
