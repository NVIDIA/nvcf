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

import static org.apache.commons.lang3.StringUtils.isNotBlank;
import static org.springframework.beans.factory.config.BeanDefinition.SCOPE_PROTOTYPE;

import com.nvidia.nvcf.service.nats.AuthCalloutService;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.tracing.Tracer;
import io.nats.client.Connection;
import io.nats.client.Connection.Status;
import io.nats.client.ConnectionListener.Events;
import io.nats.client.ErrorListener;
import io.nats.client.ForceReconnectOptions;
import io.nats.client.JetStream;
import io.nats.client.JetStreamManagement;
import io.nats.client.Nats;
import io.nats.client.Options;
import io.nats.client.Statistics;
import io.nats.client.api.Placement;
import io.nats.client.api.StorageType;
import io.nats.client.impl.TracedNatsConnection;
import jakarta.annotation.Nullable;
import java.io.IOException;
import java.net.URI;
import java.time.Duration;
import java.util.Arrays;
import java.util.Collection;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.ToDoubleFunction;
import java.util.function.ToLongFunction;
import lombok.Data;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.RandomUtils;
import org.springframework.beans.factory.InitializingBean;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.ApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Scope;
import org.springframework.stereotype.Service;

@Slf4j
@Configuration(proxyBeanMethods = false)
public class NatsConfiguration {

    private static Connection connect(Options options, Tracer tracer)
            throws IOException, InterruptedException {
        TracedNatsConnection conn = new TracedNatsConnection(options, tracer);
        conn.connect(false);
        return conn;
    }

    @Bean
    @Scope(SCOPE_PROTOTYPE)
    public Connection natsConnection(
            NatsProperties natsProperties,
            // only required if the auth callout is enabled to establish ordering
            Optional<AuthCalloutService> authCalloutService,
            Optional<NatsTokenAuthProvider> natsTokenAuthProvider,
            Tracer tracer)
            throws IOException, InterruptedException {
        var optionsBuilder = Options.builder()
                .server(natsProperties.getUrl().toString());
        if (natsTokenAuthProvider.isPresent()) {
            optionsBuilder.tokenSupplier(natsTokenAuthProvider.get()::getToken);
        } else if (natsProperties.getApiUserSeed() != null) {
            optionsBuilder.authHandler(Nats.staticCredentials(null,
                                                              natsProperties.getApiUserSeed()
                                                                      .toCharArray()));
        } else {
            throw new IllegalArgumentException(
                    "Either OAuth2 properties or apiUserSeed must be provided");
        }

        var options = optionsBuilder
                .pingInterval(Duration.ofSeconds(5))
                .useDispatcherWithExecutor()
                .reconnectWait(Duration.ofMillis(100))
                .errorListener(new LoggingNatsErrorListener())
                .connectionListener(
                        (conn, type) -> {
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
                        })
                .build();
        return connect(options, tracer);
    }

    @Data
    @Configuration(proxyBeanMethods = false)
    @ConfigurationProperties(prefix = "nvcf.nats")
    public static class NatsProperties {

        private URI url;
        private String region;
        @Nullable
        private List<String> secondaryRegions;
        private int replicas;
        private URI workerUrl;
        private Duration managementTimeout;
        private StorageType storageType;
        private String regionPlacementTag;
        private int maxPoolSize = 8;
        @Nullable
        private String apiUser; // public nkey
        @Nullable
        private String apiUserSeed; // private nkey
        @Nullable
        private OAuth2Properties oauth2Properties;
        private boolean requestQueueLimitEnabled;

        // Using the value specified in helm charts used for deployment in the self-hosted env
        // as the default value. Overridden in the managed service.
        private String authCalloutPluginName = "oidc";

        public record OAuth2Properties(boolean enabled, String baseUrl, String clientId,
                                       String secretId) {

        }

        @Nullable
        public Placement getPlacement(String region) {
            if (isNotBlank(getRegionPlacementTag())) {
                return Placement.builder().tags(getRegionPlacementTag() + ":" + region).build();
            }
            return null;
        }
    }

    @Slf4j
    private static class LoggingNatsErrorListener implements ErrorListener {

        @Override
        public void errorOccurred(Connection conn, String error) {
            log.error("nats error occurred {} {}", conn.getServerInfo(), error);
        }

        @Override
        public void exceptionOccurred(Connection conn, Exception exp) {
            log.error("nats exception occurred {}", conn.getServerInfo(), exp);
        }

    }

    @Service
    public static class FixedNatsPool implements AutoCloseable {

        private final Connection[] connections;
        private final JetStream[] jetStreams;
        private final JetStreamManagement[] jetStreamManagements;
        private final AtomicInteger index = new AtomicInteger();

        public FixedNatsPool(ApplicationContext applicationContext, NatsProperties natsProperties)
                throws IOException {
            int processors = Runtime.getRuntime().availableProcessors();
            int poolSize = Math.min(processors, natsProperties.getMaxPoolSize());
            this.connections = new Connection[poolSize];
            this.jetStreams = new JetStream[connections.length];
            this.jetStreamManagements = new JetStreamManagement[connections.length];
            for (int i = 0; i < connections.length; i++) {
                connections[i] = applicationContext.getBean(Connection.class);
                jetStreams[i] = connections[i].jetStream();
                jetStreamManagements[i] = connections[i].jetStreamManagement();
                // force connection use
                connections[i].RTT();
            }
        }

        @Override
        public void close()
                throws Exception {
            for (Connection connection : connections) {
                connection.close();
            }
        }

        private int nextIndex() {
            return Math.abs(index.getAndIncrement()) % connections.length;
        }

        public Connection borrowConnection() {
            return connections[nextIndex()];
        }

        public JetStream borrowJetStream() {
            return jetStreams[nextIndex()];
        }

        public JetStreamManagement borrowJetStreamManagement() {
            return jetStreamManagements[nextIndex()];
        }

        public boolean healthy() {
            for (Connection connection : connections) {
                if (connection.getStatus() != Status.CONNECTED) {
                    log.warn("unhealthy nats connection {}", connection.getServerInfo());
                    return false;
                }
            }
            return true;
        }
    }

    @Configuration(proxyBeanMethods = false)
    static class NatsMetricsConfiguration implements InitializingBean {

        // keeping strong ref to statistics due to WeakReference in counter implementation
        private final Collection<Statistics> statistics;
        private final MeterRegistry meterRegistry;

        public NatsMetricsConfiguration(FixedNatsPool fixedNatsPool, MeterRegistry meterRegistry) {
            this.meterRegistry = meterRegistry;
            this.statistics = Arrays.stream(fixedNatsPool.connections)
                    .map(Connection::getStatistics)
                    .toList();
        }

        /**
         * this doesn't cover nats connections outside the fixed nats pool
         */
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
            meterRegistry.more().counter("nats.replies.received", List.of(),
                                         statistics, sumProperties(Statistics::getRepliesReceived));
            meterRegistry.more().counter("nats.replies.received.duplicate", List.of(),
                                         statistics,
                                         sumProperties(Statistics::getDuplicateRepliesReceived));
            meterRegistry.more().counter("nats.replies.received.orphan", List.of(),
                                         statistics,
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
            meterRegistry.gauge("nats.requests.outstanding",
                                statistics, sumProperties(Statistics::getOutstandingRequests));
        }

        private static <T> ToDoubleFunction<Collection<T>> sumProperties(
                ToLongFunction<T> propertyExtractor) {
            return collection -> collection.stream().mapToLong(propertyExtractor).sum();
        }
    }

}
