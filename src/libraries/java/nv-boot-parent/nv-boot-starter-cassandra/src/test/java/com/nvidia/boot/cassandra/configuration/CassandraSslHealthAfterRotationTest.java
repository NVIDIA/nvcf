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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.datastax.oss.driver.api.core.cql.Row;
import java.time.Duration;
import java.time.Instant;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.ssl.PemSslBundleProperties;
import org.springframework.boot.autoconfigure.ssl.PropertiesSslBundle;
import org.springframework.boot.autoconfigure.ssl.SslProperties;
import org.springframework.boot.health.application.SslHealthIndicator;
import org.springframework.boot.health.contributor.Status;
import org.springframework.boot.info.SslInfo;
import org.springframework.boot.ssl.DefaultSslBundleRegistry;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.support.GenericApplicationContext;
import org.springframework.mock.env.MockEnvironment;

/**
 * Verifies that after a certificate rotation {@code /actuator/health/ssl} is computed from the
 * rotated material rather than from the certificates loaded at startup.
 * <p>
 * The health indicator is built the same way Spring Boot's {@code SslHealthContributorAutoConfiguration}
 * builds it: {@link SslHealthIndicator} over {@link SslInfo} over the application's
 * {@link org.springframework.boot.ssl.SslBundles}. Certificates are generated in memory: the "stale"
 * one expired yesterday and the "fresh" one is valid for ten years.
 */
class CassandraSslHealthAfterRotationTest {

    private static final String BUNDLE_NAME = "cassandra-ssl";
    private static final String CERT_PROPERTY =
            "spring.ssl.bundle.pem." + BUNDLE_NAME + ".keystore.certificate";
    private static final Instant NOW = Instant.now();
    private static final TestCertificates.PemMaterial STALE = TestCertificates.selfSigned(
            "stale-cassandra-client", NOW.minus(Duration.ofDays(730)), NOW.minus(Duration.ofDays(1)));
    private static final TestCertificates.PemMaterial FRESH = TestCertificates.selfSigned(
            "rotated-cassandra-client", NOW.minus(Duration.ofDays(1)), NOW.plus(Duration.ofDays(3650)));

    private GenericApplicationContext applicationContext;
    private DefaultSslBundleRegistry registry;
    private SslHealthIndicator healthIndicator;
    private MockEnvironment environment;
    private SslProperties sslProperties;
    private CqlSession delegateSession;
    private CqlSession rebuiltSession;
    private CqlSession afterBundleUpdateSession;
    private CassandraSslBundleConfiguration.RefreshingCqlSession session;

    @BeforeEach
    void setUp() {
        rebuiltSession = validSession();
        afterBundleUpdateSession = validSession();

        var builder = mock(CqlSessionBuilder.class);
        when(builder.withSslContext(any())).thenReturn(builder);
        when(builder.build()).thenReturn(rebuiltSession, afterBundleUpdateSession);

        applicationContext = new GenericApplicationContext();
        applicationContext.registerBean(CqlSessionBuilder.class, () -> builder);
        applicationContext.refresh();

        sslProperties = new SslProperties();
        sslProperties.getBundle().getPem().put(BUNDLE_NAME, pemProperties(STALE));

        registry = new DefaultSslBundleRegistry();
        registry.registerBundle(BUNDLE_NAME, PropertiesSslBundle.get(
                sslProperties.getBundle().getPem().get(BUNDLE_NAME), applicationContext));

        healthIndicator = new SslHealthIndicator(
                new SslInfo(registry), Duration.ofDays(14));

        environment = new MockEnvironment();
        environment.setProperty("spring.cassandra.username", "u");
        environment.setProperty("spring.cassandra.password", "p");
        environment.setProperty(CERT_PROPERTY, STALE.certificate());

        delegateSession = mock(CqlSession.class);
        session = new CassandraSslBundleConfiguration.RefreshingCqlSession(
                applicationContext, registry, registry, environment, sslProperties, BUNDLE_NAME,
                delegateSession, null);
        session.registerSslBundleUpdateHandler();
    }

    @AfterEach
    void tearDown() {
        applicationContext.close();
    }

    @Test
    @DisplayName("Baseline: bundle loaded at startup holds an expired certificate, so ssl health is OUT_OF_SERVICE")
    void healthReportsExpiredCertificateBeforeRotation() {
        var health = healthIndicator.health();

        assertThat(health.getStatus()).isEqualTo(Status.OUT_OF_SERVICE);
        assertThat(health.getDetails().toString()).contains("invalidChains");
        assertThat(subjectOfFirstCertificate()).contains("stale-cassandra-client");
    }

    @Test
    @DisplayName("After PEM rotation and a successful session rebuild, ssl health is UP and reports the rotated certificate")
    void healthReportsRotatedCertificateAfterRefresh() {
        rotateCertificate();

        session.handleRefreshComplete(new RefreshScopeRefreshedEvent());

        var health = healthIndicator.health();
        assertThat(health.getStatus()).isEqualTo(Status.UP);
        assertThat(subjectOfFirstCertificate()).contains("rotated-cassandra-client");
        assertThat(health.getDetails().toString())
                .contains("validChains")
                .doesNotContain("stale-cassandra-client");
        assertThat(session.getDelegate()).isSameAs(afterBundleUpdateSession);
        verify(delegateSession).close();
        verify(rebuiltSession).close();
        verify(afterBundleUpdateSession, never()).close();
    }

    @Test
    @DisplayName("Without a SslBundleRegistry bean, rotation still rebuilds the session and the registry is left untouched")
    void rotationWithoutRegistryStillRebuildsSession() {
        var sessionWithoutRegistry = new CassandraSslBundleConfiguration.RefreshingCqlSession(
                applicationContext, registry, null, environment, sslProperties, BUNDLE_NAME,
                delegateSession, null);
        rotateCertificate();

        sessionWithoutRegistry.handleRefreshComplete(new RefreshScopeRefreshedEvent());

        assertThat(sessionWithoutRegistry.getDelegate()).isSameAs(rebuiltSession);
        verify(delegateSession).close();
        assertThat(healthIndicator.health().getStatus()).isEqualTo(Status.OUT_OF_SERVICE);
    }

    @Test
    @DisplayName("Registry is not updated when the rebuilt session fails validation, so health keeps reporting the bundle the live session still uses")
    void healthKeepsReportingOldBundleWhenSessionValidationFails() {
        when(rebuiltSession.execute(anyString())).thenThrow(new IllegalStateException("auth failed"));
        rotateCertificate();

        session.handleRefreshComplete(new RefreshScopeRefreshedEvent());

        assertThat(session.getDelegate()).isSameAs(delegateSession);
        assertThat(healthIndicator.health().getStatus()).isEqualTo(Status.OUT_OF_SERVICE);
        assertThat(subjectOfFirstCertificate()).contains("stale-cassandra-client");
    }

    @Test
    @DisplayName("Credential-only refresh leaves the registered SSL bundle untouched")
    void credentialOnlyRefreshDoesNotReplaceBundle() {
        var bundleBefore = registry.getBundle(BUNDLE_NAME);
        environment.setProperty("spring.cassandra.password", "p2");

        session.handleRefreshComplete(new RefreshScopeRefreshedEvent());

        assertThat(session.getDelegate()).isNotSameAs(delegateSession);
        assertThat(registry.getBundle(BUNDLE_NAME).getStores().getKeyStore())
                .isSameAs(bundleBefore.getStores().getKeyStore());
        assertThat(healthIndicator.health().getStatus()).isEqualTo(Status.OUT_OF_SERVICE);
    }

    private static CqlSession validSession() {
        var validSession = mock(CqlSession.class);
        var resultSet = mock(ResultSet.class);
        when(resultSet.one()).thenReturn(mock(Row.class));
        when(validSession.execute(anyString())).thenReturn(resultSet);
        return validSession;
    }

    private void rotateCertificate() {
        sslProperties.getBundle().getPem().put(BUNDLE_NAME, pemProperties(FRESH));
        environment.setProperty(CERT_PROPERTY, FRESH.certificate());
    }

    private String subjectOfFirstCertificate() {
        return new SslInfo(registry)
                .getBundle(BUNDLE_NAME)
                .getCertificateChains().get(0)
                .getCertificates().get(0)
                .getSubject();
    }

    private static PemSslBundleProperties pemProperties(TestCertificates.PemMaterial pem) {
        var props = new PemSslBundleProperties();
        props.getKeystore().setCertificate(pem.certificate());
        props.getKeystore().setPrivateKey(pem.privateKey());
        return props;
    }
}
