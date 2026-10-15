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

package com.nvidia.boot.cassandra.configuration;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import com.google.common.annotations.VisibleForTesting;
import io.micrometer.common.util.StringUtils;
import io.micrometer.observation.ObservationRegistry;
import jakarta.annotation.PostConstruct;
import java.util.Objects;
import java.util.concurrent.locks.ReentrantLock;
import lombok.Getter;
import lombok.experimental.Delegate;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.cassandra.autoconfigure.CqlSessionBuilderCustomizer;
import org.springframework.boot.autoconfigure.condition.AnyNestedCondition;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.autoconfigure.ssl.PropertiesSslBundle;
import org.springframework.boot.autoconfigure.ssl.SslProperties;
import org.springframework.boot.ssl.SslBundle;
import org.springframework.boot.ssl.SslBundles;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.ApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Conditional;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.context.event.EventListener;
import org.springframework.core.env.Environment;
import org.springframework.data.cassandra.observability.ObservableCqlSessionFactory;
import org.springframework.data.cassandra.observability.ObservationRequestTracker;

/**
 * Cassandra session wiring for the SSL-bundle / refreshable session path. Active when either
 * {@code spring.cassandra.ssl.bundle} is specified (including empty, for PEM bundle name only) or
 * when {@code spring.cassandra.ssl.enabled=false} so local plaintext clusters do not need a dummy
 * {@code spring.cassandra.ssl.bundle=} property. When a non-empty bundle name is configured, SSL is
 * applied on the {@link CqlSessionBuilder}; otherwise the builder is left without client TLS.
 * <p>
 * Also registers a {@code @Primary} {@link #refreshableCqlSession refreshing session} and (when
 * Spring Data Cassandra observability types are on the classpath) {@link ObservationRequestTracker}
 * on the builder so {@link ObservableCqlSessionFactory} observations start and complete on the driver.
 */
@Slf4j
@Configuration
@Conditional(CassandraSslBundleConfiguration.ActivationConditions.class)
public class CassandraSslBundleConfiguration {

    /**
     * Enables this configuration when apps use PEM SSL bundles
     * ({@code spring.cassandra.ssl.bundle}) or disable Cassandra TLS explicitly for plaintext
     * endpoints ({@code spring.cassandra.ssl.enabled=false}).
     */
    static final class ActivationConditions extends AnyNestedCondition {

        ActivationConditions() {
            super(ConfigurationPhase.REGISTER_BEAN);
        }

        @ConditionalOnProperty(name = "spring.cassandra.ssl.bundle")
        static class OnBundleProperty {
        }

        @ConditionalOnProperty(name = "spring.cassandra.ssl.enabled", matchIfMissing = false,
                havingValue = "false")
        static class OnCassandraSslDisabled {
        }
    }

    @Value("${spring.cassandra.ssl.bundle:}")
    private String sslBundleName;

    /**
     * Registers Spring Data Cassandra's {@link ObservationRequestTracker} on the shared
     * {@link CqlSessionBuilder} so observations started by {@link ObservableCqlSessionFactory#wrap}
     * are stopped when each request finishes.
     */
    @Bean
    @ConditionalOnClass(name = "org.springframework.data.cassandra.observability.ObservationRequestTracker")
    public CqlSessionBuilderCustomizer cassandraObservationRequestTrackerCustomizer() {
        return builder -> builder.addRequestTracker(ObservationRequestTracker.INSTANCE);
    }

    @Bean
    public CqlSessionBuilderCustomizer cassandraSslCustomizer(SslBundles sslBundles) {
        return builder -> {
            if (!sslBundleName.isEmpty()) {
                log.info("Configuring Cassandra SSL with bundle: {}", sslBundleName);
                var sslBundle = sslBundles.getBundle(sslBundleName);
                var sslContext = sslBundle.createSslContext();
                builder.withSslContext(sslContext);
            }
        };
    }

    /**
     * Creates a {@link RefreshingCqlSession} that validates new sessions before switching,
     * handles SSL bundle updates (cert rotation), and credential rotation.
     * <p>
     * When an {@link ObservationRegistry} bean is present (typically from Spring Boot Micrometer
     * observation auto-configuration when observation dependencies are on the classpath) and
     * {@link RefreshingCqlSessionObservabilityProperties} does not disable wrapping, the session
     * is wrapped with {@link ObservableCqlSessionFactory#wrap} so CQL participates in observations.
     * {@link #cassandraObservationRequestTrackerCustomizer()} in this same configuration registers
     * {@link ObservationRequestTracker} on the builder so those observations complete.
     * <p>
     * Apps that need to instrument observability manually (e.g. {@code ObservableReactiveSessionFactoryBean}
     * in mutually exclusive) can expose a {@link RefreshingCqlSessionObservabilityProperties} bean with
     * {@code enabled=false} to opt out and avoid duplicate or orphan spans.
     */
    @Bean("cassandraSession")
    @Primary
    public RefreshingCqlSession refreshableCqlSession(
            ApplicationContext applicationContext,
            SslBundles sslBundles,
            Environment environment,
            @Autowired(required = false) SslProperties sslProperties,
            CqlSessionBuilder cqlSessionBuilder,
            @Autowired(required = false) ObservationRegistry observationRegistry,
            @Autowired(required = false) RefreshingCqlSessionObservabilityProperties observationProperties) {
        log.info("Creating RefreshingCqlSession with SSL bundle and credentials");
        var shouldWrap = observationProperties == null || observationProperties.isEnabled();
        var actualObservationRegistry = shouldWrap ? observationRegistry : null;

        var initialSession = cqlSessionBuilder.build();
        var sessionToUse = wrapWithObservability(initialSession, actualObservationRegistry);
        return new RefreshingCqlSession(applicationContext, sslBundles, environment, sslProperties,
                sslBundleName, sessionToUse, actualObservationRegistry);
    }

    private static CqlSession wrapWithObservability(
            CqlSession session,
            ObservationRegistry observationRegistry) {
        if (observationRegistry != null) {
            return ObservableCqlSessionFactory.wrap(session, observationRegistry);
        }
        return session;
    }

    /**
     * CqlSession implementation that refreshes on SSL bundle updates and credential changes.
     */
    @Slf4j
    public static class RefreshingCqlSession implements CqlSession {

        private static final String SESSION_TEST_QUERY = "SELECT cluster_name FROM system.local";

        private final ApplicationContext applicationContext;
        private final SslBundles sslBundles;
        private final Environment environment;
        private final SslProperties sslProperties;
        private final String sslBundleName;
        private final ObservationRegistry observationRegistry;
        private final ReentrantLock sessionLock = new ReentrantLock();

        @Delegate
        @Getter(onMethod_ = @VisibleForTesting)
        private volatile CqlSession delegate;

        private String lastKnownUsername;
        private String lastKnownPassword;
        private SslMaterialSnapshot lastKnownSslMaterial;

        RefreshingCqlSession(
                ApplicationContext applicationContext,
                SslBundles sslBundles,
                Environment environment,
                SslProperties sslProperties,
                String sslBundleName,
                CqlSession initialSession,
                ObservationRegistry observationRegistry) {
            this.applicationContext = applicationContext;
            this.sslBundles = sslBundles;
            this.environment = environment;
            this.sslProperties = sslProperties;
            this.sslBundleName = sslBundleName;
            this.observationRegistry = observationRegistry;
            this.delegate = initialSession;
            this.lastKnownUsername = environment.getProperty("spring.cassandra.username");
            this.lastKnownPassword = environment.getProperty("spring.cassandra.password");
            this.lastKnownSslMaterial = SslMaterialSnapshot.from(environment, sslBundleName);
        }

        @PostConstruct
        public void registerSslBundleUpdateHandler() {
            if (sslBundleName != null && !sslBundleName.isEmpty()) {
                sslBundles.addBundleUpdateHandler(sslBundleName, this::handleSslBundleUpdate);
                log.info("Registered SSL bundle update handler for RefreshingCqlSession");
            }
        }

        @EventListener(RefreshScopeRefreshedEvent.class)
        public void handleRefreshComplete(RefreshScopeRefreshedEvent event) {
            sessionLock.lock();
            try {
                var username = environment.getProperty("spring.cassandra.username");
                var password = environment.getProperty("spring.cassandra.password");

                if (!hasConnectionMaterialChanged(username, password)) {
                    log.info("Cassandra creds and SSL bundle material unchanged - no refresh needed");
                    return;
                }

                log.info("Cassandra creds or SSL bundle material changed - rebuilding session");
                var oldSession = this.delegate;
                try {
                    var builder = applicationContext.getBean(CqlSessionBuilder.class);
                    var currentSslSnapshot = SslMaterialSnapshot.from(environment, sslBundleName);
                    var sslMaterialChanged = !StringUtils.isBlank(sslBundleName)
                            && !Objects.equals(lastKnownSslMaterial, currentSslSnapshot);
                    CqlSession newSession;
                    if (sslMaterialChanged && sslProperties != null) {
                        var pem = sslProperties.getBundle().getPem().get(sslBundleName);
                        if (pem != null) {
                            var freshBundle = PropertiesSslBundle.get(pem, applicationContext);
                            var sslContext = freshBundle.createSslContext();
                            var sslSessionBuilder = builder.withSslContext(sslContext);
                            newSession = wrapWithObservability(sslSessionBuilder.build());
                        } else {
                            log.warn("SSL PEM material changed in Environment but bundle '{}' is "
                                            + "missing from SslProperties; rebuilding with "
                                            + "CqlSessionBuilder defaults (registered SslBundles "
                                            + "may still reflect old material)",
                                    sslBundleName);
                            newSession = wrapWithObservability(builder.build());
                        }
                    } else {
                        if (sslMaterialChanged && sslProperties == null) {
                            log.warn("SSL PEM material changed but SslProperties bean is not"
                                             + "available; rebuilding with CqlSessionBuilder "
                                             + "defaults (SslBundles may be stale)");
                        }
                        newSession = wrapWithObservability(builder.build());
                    }
                    if (isSessionValid(newSession)) {
                        log.info("Session validation successful - switching to new session");
                        this.delegate = newSession;
                        lastKnownUsername = username;
                        lastKnownPassword = password;
                        lastKnownSslMaterial = SslMaterialSnapshot.from(environment, sslBundleName);
                        oldSession.close();
                    } else {
                        log.error("New session validation failed - keeping old session");
                        newSession.close();
                    }
                } catch (Exception e) {
                    log.error("Failed to build new session - keeping old session", e);
                }
            } finally {
                sessionLock.unlock();
            }
        }

        private CqlSession wrapWithObservability(CqlSession session) {
            return CassandraSslBundleConfiguration.wrapWithObservability(session,
                    observationRegistry);
        }

        /**
         * Detects username/password or PEM bundle property changes under
         * {@code spring.ssl.bundle.pem.<bundleName>.*} (keystore cert/key, truststore cert),
         * by comparing them to their corresponding last known values.
         */
        private boolean hasConnectionMaterialChanged(String username, String password) {
            if (hasCredentialsChanged(username, password)) {
                return true;
            }
            if (StringUtils.isBlank(sslBundleName)) {
                return false;
            }
            var currentSsl = SslMaterialSnapshot.from(environment, sslBundleName);
            return !Objects.equals(lastKnownSslMaterial, currentSsl);
        }

        private boolean hasCredentialsChanged(String username, String password) {
            return !Objects.equals(username, lastKnownUsername)
                    || !Objects.equals(password, lastKnownPassword);
        }

        private record SslMaterialSnapshot(
                String keystoreCertificate,
                String keystorePrivateKey,
                String truststoreCertificate) {

            static SslMaterialSnapshot from(Environment environment, String bundleName) {
                if (bundleName == null || bundleName.isEmpty()) {
                    return null;
                }
                var prefix = "spring.ssl.bundle.pem." + bundleName + ".";
                return new SslMaterialSnapshot(
                        environment.getProperty(prefix + "keystore.certificate"),
                        environment.getProperty(prefix + "keystore.private-key"),
                        environment.getProperty(prefix + "truststore.certificate"));
            }
        }

        /*
         * This get invoked by Spring when reload-on-update is true and when Spring detects
         * changes to the files specified under SSL bundle.
         */
        @VisibleForTesting
        void handleSslBundleUpdate(SslBundle updatedBundle) {
            sessionLock.lock();
            try {
                log.info("SSL bundle updated - rebuilding Cassandra session");
                var oldSession = this.delegate;
                try {
                    var sslContext = updatedBundle.createSslContext();
                    var builder = applicationContext.getBean(CqlSessionBuilder.class);
                    var newSession = wrapWithObservability(builder.withSslContext(sslContext)
                                                                   .build());
                    if (isSessionValid(newSession)) {
                        log.info("Session validation successful - switching to new session");
                        this.delegate = newSession;
                        oldSession.close();
                    } else {
                        log.error("New session validation failed - keeping old session");
                        newSession.close();
                    }
                } catch (Exception e) {
                    log.error("Failed to build new session - keeping old session", e);
                }
            } finally {
                sessionLock.unlock();
            }
        }

        private boolean isSessionValid(CqlSession session) {
            try {
                var resultSet = session.execute(SESSION_TEST_QUERY);
                return resultSet.one() != null;
            } catch (Exception e) {
                log.error("Session validation failed", e);
                return false;
            }
        }
    }
}
