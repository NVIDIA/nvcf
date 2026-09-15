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
 * WITHOUT WARRANTIES OR ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package com.nvidia.boot.cassandra.configuration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.mockStatic;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.CqlSessionBuilder;
import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.datastax.oss.driver.api.core.cql.Row;
import java.util.Map;
import javax.net.ssl.SSLContext;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.mockito.MockedStatic;
import org.springframework.boot.autoconfigure.ssl.PemSslBundleProperties;
import org.springframework.boot.autoconfigure.ssl.PropertiesSslBundle;
import org.springframework.boot.autoconfigure.ssl.SslProperties;
import org.springframework.boot.ssl.SslBundle;
import org.springframework.boot.ssl.SslBundles;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.ApplicationContext;
import org.springframework.mock.env.MockEnvironment;

/**
 * Ensures {@link CassandraSslBundleConfiguration.RefreshingCqlSession#handleRefreshComplete} builds
 * TLS from {@link SslProperties} and {@link PropertiesSslBundle} when PEM material changes, so the
 * session does not keep using stale {@link SslBundles} from startup.
 */
class RefreshingCqlSessionSslPropertiesRefreshTest {

    @Test
    @DisplayName("When SSL PEM changes and SslProperties has the bundle, uses PropertiesSslBundle.get and withSslContext")
    void whenSslMaterialChangesAndSslPropertiesPresentUsesFreshSslBundle() {
        var environment = new MockEnvironment();
        environment.setProperty("spring.cassandra.username", "u");
        environment.setProperty("spring.cassandra.password", "p");
        environment.setProperty("spring.ssl.bundle.pem.cassandra-ssl.keystore.certificate", "cert-v1");
        environment.setProperty("spring.ssl.bundle.pem.cassandra-ssl.keystore.private-key", "key-v1");

        var pemProps = mock(PemSslBundleProperties.class);
        var bundles = mock(SslProperties.Bundles.class);
        when(bundles.getPem()).thenReturn(Map.of("cassandra-ssl", pemProps));
        var sslProperties = mock(SslProperties.class);
        when(sslProperties.getBundle()).thenReturn(bundles);

        var delegateSession = mock(CqlSession.class);
        var newSession = mock(CqlSession.class);
        var resultSet = mock(ResultSet.class);
        var row = mock(Row.class);
        when(resultSet.one()).thenReturn(row);
        when(newSession.execute(anyString())).thenReturn(resultSet);

        var sslContext = mock(SSLContext.class);
        var freshBundle = mock(SslBundle.class);
        when(freshBundle.createSslContext()).thenReturn(sslContext);

        var builder = mock(CqlSessionBuilder.class);
        when(builder.withSslContext(sslContext)).thenReturn(builder);
        when(builder.build()).thenReturn(newSession);

        var applicationContext = mock(ApplicationContext.class);
        when(applicationContext.getBean(CqlSessionBuilder.class)).thenReturn(builder);

        var sslBundles = mock(SslBundles.class);
        var session = new CassandraSslBundleConfiguration.RefreshingCqlSession(
                applicationContext,
                sslBundles,
                environment,
                sslProperties,
                "cassandra-ssl",
                delegateSession,
                null);

        environment.setProperty("spring.ssl.bundle.pem.cassandra-ssl.keystore.certificate", "cert-v2");

        try (MockedStatic<PropertiesSslBundle> sslBundleStatic = mockStatic(PropertiesSslBundle.class)) {
            sslBundleStatic
                    .when(() -> PropertiesSslBundle.get(eq(pemProps), eq(applicationContext)))
                    .thenReturn(freshBundle);

            session.handleRefreshComplete(new RefreshScopeRefreshedEvent());

            sslBundleStatic.verify(() -> PropertiesSslBundle.get(eq(pemProps), eq(applicationContext)));
        }

        verify(builder).withSslContext(sslContext);
        verify(delegateSession).close();
        assertThat(session.getDelegate()).isSameAs(newSession);
    }
}
