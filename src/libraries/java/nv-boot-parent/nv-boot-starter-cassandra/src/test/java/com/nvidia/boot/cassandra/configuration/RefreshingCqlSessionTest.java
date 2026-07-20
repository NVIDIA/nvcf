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
import com.datastax.oss.driver.api.core.cql.Statement;
import io.micrometer.observation.ObservationRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Nested;
import org.junit.jupiter.api.Test;
import org.springframework.boot.ssl.SslBundle;
import org.springframework.boot.ssl.SslBundles;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.ApplicationContext;
import org.springframework.mock.env.MockEnvironment;

class RefreshingCqlSessionTest {

    private ApplicationContext applicationContext;
    private CassandraSslBundleConfiguration.RefreshingCqlSession session;
    private CqlSession delegateSession;
    private MockEnvironment environment;

    @BeforeEach
    void setUp() {
        applicationContext = mock(ApplicationContext.class);
        delegateSession = mock(CqlSession.class);
        environment = new MockEnvironment();
        environment.setProperty("spring.cassandra.username", "user1");
        environment.setProperty("spring.cassandra.password", "pass1");

        var sslBundles = mock(SslBundles.class);
        session = new CassandraSslBundleConfiguration.RefreshingCqlSession(
                applicationContext,
                sslBundles,
                environment,
                null,
                "",
                delegateSession,
                null);
    }

    @Test
    @DisplayName("Skips rebuild when credentials and SSL PEM properties unchanged")
    void handleRefreshCompleteSkipsRebuildWhenCredentialsUnchanged() {
        var event = new RefreshScopeRefreshedEvent();

        session.handleRefreshComplete(event);

        verify(delegateSession, never()).close();
    }

    @Test
    @DisplayName("Skips rebuild when credentials unchanged and SSL bundle PEM unchanged")
    void handleRefreshCompleteSkipsWhenSslBundleMaterialUnchanged() {
        environment.setProperty("spring.ssl.bundle.pem.cassandra-ssl.keystore.certificate", "cert-v1");
        environment.setProperty("spring.ssl.bundle.pem.cassandra-ssl.keystore.private-key", "key-v1");
        var sslBundles = mock(SslBundles.class);
        var sessionWithBundle = new CassandraSslBundleConfiguration.RefreshingCqlSession(
                applicationContext,
                sslBundles,
                environment,
                null,
                "cassandra-ssl",
                delegateSession,
                null);

        sessionWithBundle.handleRefreshComplete(new RefreshScopeRefreshedEvent());

        verify(delegateSession, never()).close();
    }

    @Test
    @DisplayName("Rebuilds when SSL bundle PEM material changes but credentials unchanged")
    void handleRefreshCompleteRebuildsWhenSslMaterialChanged() {
        environment.setProperty("spring.ssl.bundle.pem.cassandra-ssl.keystore.certificate", "cert-v1");
        environment.setProperty("spring.ssl.bundle.pem.cassandra-ssl.keystore.private-key", "key-v1");

        var sslBundles = mock(SslBundles.class);
        var sessionWithBundle = new CassandraSslBundleConfiguration.RefreshingCqlSession(
                applicationContext,
                sslBundles,
                environment,
                null,
                "cassandra-ssl",
                delegateSession,
                null);

        var newSession = mock(CqlSession.class);
        var resultSet = mock(ResultSet.class);
        var row = mock(Row.class);
        when(resultSet.one()).thenReturn(row);
        when(newSession.execute(anyString())).thenReturn(resultSet);

        var builder = mock(CqlSessionBuilder.class);
        when(builder.build()).thenReturn(newSession);
        when(applicationContext.getBean(CqlSessionBuilder.class)).thenReturn(builder);

        environment.setProperty("spring.ssl.bundle.pem.cassandra-ssl.keystore.certificate", "cert-v2");

        sessionWithBundle.handleRefreshComplete(new RefreshScopeRefreshedEvent());

        verify(delegateSession).close();
        verify(newSession).execute(anyString());
    }

    @Test
    @DisplayName("Rebuilds when credentials changed")
    void handleRefreshCompleteRebuildsWhenCredentialsChanged() {
        var newSession = mock(CqlSession.class);
        var resultSet = mock(ResultSet.class);
        var row = mock(Row.class);
        when(resultSet.one()).thenReturn(row);
        when(newSession.execute(anyString())).thenReturn(resultSet);

        var builder = mock(CqlSessionBuilder.class);
        when(builder.build()).thenReturn(newSession);
        when(applicationContext.getBean(CqlSessionBuilder.class)).thenReturn(builder);

        environment.setProperty("spring.cassandra.username", "user2");
        environment.setProperty("spring.cassandra.password", "pass2");

        var event = new RefreshScopeRefreshedEvent();
        session.handleRefreshComplete(event);

        verify(delegateSession).close();
        verify(newSession).execute(anyString());
    }

    @Test
    @DisplayName("Delegates execute to session")
    void executeDelegatesToSession() {
        var resultSet = mock(ResultSet.class);
        when(delegateSession.execute(anyString())).thenReturn(resultSet);

        var result = session.execute("SELECT 1");
        assertThat(result).isSameAs(resultSet);
        verify(delegateSession).execute("SELECT 1");
    }

    @Nested
    @DisplayName("With ObservationRegistry (observability wrapping)")
    class WithObservationRegistry {

        @Test
        @DisplayName("Wraps new session with observability on credential refresh")
        void handleRefreshCompleteWrapsNewSessionWithObservability() throws Exception {
            var newSession = mock(CqlSession.class);
            var resultSet = mock(ResultSet.class);
            var row = mock(Row.class);
            when(resultSet.one()).thenReturn(row);
            when(newSession.execute(any(Statement.class))).thenReturn(resultSet);

            var builder = mock(CqlSessionBuilder.class);
            when(builder.build()).thenReturn(newSession);
            when(applicationContext.getBean(CqlSessionBuilder.class)).thenReturn(builder);

            var sslBundles = mock(SslBundles.class);
            var sessionWithObservability = new CassandraSslBundleConfiguration.RefreshingCqlSession(
                    applicationContext,
                    sslBundles,
                    environment,
                    null,
                    "",
                    delegateSession,
                    ObservationRegistry.create());

            environment.setProperty("spring.cassandra.username", "user2");
            environment.setProperty("spring.cassandra.password", "pass2");

            sessionWithObservability.handleRefreshComplete(new RefreshScopeRefreshedEvent());

            verify(delegateSession).close();
            verify(newSession).execute(any(Statement.class));

            var delegate = sessionWithObservability.getDelegate();
            assertThat(delegate).isNotNull().isNotSameAs(newSession);
        }

        @Test
        @DisplayName("Wraps new session with observability on SSL bundle update")
        void handleSslBundleUpdateWrapsNewSessionWithObservability() throws Exception {
            var newSession = mock(CqlSession.class);
            var resultSet = mock(ResultSet.class);
            var row = mock(Row.class);
            when(resultSet.one()).thenReturn(row);
            when(newSession.execute(any(Statement.class))).thenReturn(resultSet);

            var builder = mock(CqlSessionBuilder.class);
            when(builder.withSslContext(any())).thenReturn(builder);
            when(builder.build()).thenReturn(newSession);
            when(applicationContext.getBean(CqlSessionBuilder.class)).thenReturn(builder);

            var sslBundle = mock(SslBundle.class);
            when(sslBundle.createSslContext()).thenReturn(null);

            var sslBundles = mock(SslBundles.class);
            var sessionWithObservability = new CassandraSslBundleConfiguration.RefreshingCqlSession(
                    applicationContext,
                    sslBundles,
                    environment,
                    null,
                    "test-bundle",
                    delegateSession,
                    ObservationRegistry.create());

            sessionWithObservability.handleSslBundleUpdate(sslBundle);

            verify(delegateSession).close();
            verify(newSession).execute(any(Statement.class));

            var delegate = sessionWithObservability.getDelegate();
            assertThat(delegate).isNotNull().isNotSameAs(newSession);
        }
    }

    @Nested
    @DisplayName("When observation disabled (null ObservationRegistry)")
    class WhenObservationDisabled {

        @Test
        @DisplayName("Keeps new session unwrapped on credential refresh")
        void handleRefreshCompleteKeepsNewSessionUnwrapped() {
            var newSession = mock(CqlSession.class);
            var resultSet = mock(ResultSet.class);
            var row = mock(Row.class);
            when(resultSet.one()).thenReturn(row);
            when(newSession.execute(anyString())).thenReturn(resultSet);

            var builder = mock(CqlSessionBuilder.class);
            when(builder.build()).thenReturn(newSession);
            when(applicationContext.getBean(CqlSessionBuilder.class)).thenReturn(builder);

            var sslBundles = mock(SslBundles.class);
            var sessionWithoutObservability =
                    new CassandraSslBundleConfiguration.RefreshingCqlSession(
                            applicationContext,
                            sslBundles,
                            environment,
                            null,
                            "",
                            delegateSession,
                            null);

            environment.setProperty("spring.cassandra.username", "user2");
            environment.setProperty("spring.cassandra.password", "pass2");

            sessionWithoutObservability.handleRefreshComplete(new RefreshScopeRefreshedEvent());

            verify(delegateSession).close();
            verify(newSession).execute(anyString());

            assertThat(sessionWithoutObservability.getDelegate()).isSameAs(newSession);
        }

        @Test
        @DisplayName("Keeps new session unwrapped on SSL bundle update")
        void handleSslBundleUpdateKeepsNewSessionUnwrapped() {
            var newSession = mock(CqlSession.class);
            var resultSet = mock(ResultSet.class);
            var row = mock(Row.class);
            when(resultSet.one()).thenReturn(row);
            when(newSession.execute(anyString())).thenReturn(resultSet);

            var builder = mock(CqlSessionBuilder.class);
            when(builder.withSslContext(any())).thenReturn(builder);
            when(builder.build()).thenReturn(newSession);
            when(applicationContext.getBean(CqlSessionBuilder.class)).thenReturn(builder);

            var sslBundle = mock(SslBundle.class);
            when(sslBundle.createSslContext()).thenReturn(null);

            var sslBundles = mock(SslBundles.class);
            var sessionWithoutObservability =
                    new CassandraSslBundleConfiguration.RefreshingCqlSession(
                            applicationContext,
                            sslBundles,
                            environment,
                            null,
                            "test-bundle",
                            delegateSession,
                            null);

            sessionWithoutObservability.handleSslBundleUpdate(sslBundle);

            verify(delegateSession).close();
            verify(newSession).execute(anyString());

            assertThat(sessionWithoutObservability.getDelegate()).isSameAs(newSession);
        }
    }
}
