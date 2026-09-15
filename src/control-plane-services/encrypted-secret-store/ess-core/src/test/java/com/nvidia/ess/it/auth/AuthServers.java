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
package com.nvidia.ess.it.auth;

import static com.github.tomakehurst.wiremock.client.WireMock.exactly;
import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.ess.auth.AuthProperties;
import com.nvidia.ess.it.IntegrationTestProperties;
import com.nvidia.ess.it.IntegrationTestProperties.OperatorAuth;
import com.nvidia.ess.it.IntegrationTestProperties.TenantAuth;
import com.nvidia.ess.it.multioauth.MultiOAuthMockServer;
import com.nvidia.ess.it.notary.NotaryMockServer;
import jakarta.annotation.PreDestroy;
import java.util.List;
import java.util.Objects;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Supplier;
import lombok.Builder;
import lombok.Getter;
import lombok.NonNull;


/**
 * <p>Inject all mocked auth-servers as a singleton that lives for the lifetime of the JVM
 * so that they don't get instantiated separately for each test-suite.</p>
 * 
 * <p>Since the baseURLs of each server don't change across tests and the served JWKs are cached by URL
 * across tests, it is necessary to ensure that test-cases obtain tokens signed by the same JWKs as those
 * cached. This in turn means that the tokens need to be signed by the same JWKs across all test-suites.</p>
 * 
 * <p>Therefore, the `MultiOAuthMockServer` and `NotaryMockServer` instances used to fetch signed tokens need
 * to be shared across test-suites. The alternative would be to clear all caches in between tests, which
 * is a contrivance and should be avoided.</p>
 * 
 */
public final class AuthServers {

    @Builder
    @Getter
    public static class CreationArgs {

        @NonNull
        private final IntegrationTestProperties integrationTestProperties;

        @NonNull
        private final AuthProperties authProperties;
    }

    private static final AtomicReference<AuthServers> instance = new AtomicReference<>();

    /**
     * 
     * Gets the singleton instance of {@link AuthServers} or creates one if it doesn't
     * exist yet and then returns it.
     * 
     * @param creationArgs
     * @return
     */
    public static AuthServers get(Supplier<CreationArgs> creationArgs) {
        
        var ret = get();
        if (Objects.isNull(ret)) {
            synchronized(AuthServers.class) {
                ret = get();
                if (Objects.isNull(ret)) {
                    var args = creationArgs.get();
                    ret = new AuthServers(args.getIntegrationTestProperties(),
                            args.getAuthProperties());
                    instance.set(ret);
                }
            }
        }
        return ret;
    }

    /**
     * 
     * Gets the singleton instance of {@link AuthServers} or {@literal null} if it
     * doesn't exist yet.
     * 
     * @return
     */
    public static AuthServers get() {
        return instance.get();
    }

    @Getter
    private MultiOAuthMockServer operatorOauth2MockServer;

    @Getter
    private WireMockServer operatorOauth2WireMockServer;

    @Getter
    private MultiOAuthMockServer tenantOauth2MockServer;

    @Getter
    private WireMockServer tenantOauth2WireMockServer;

    @Getter
    private MultiOAuthMockServer notarySignAuthOauth2MockServer;

    @Getter
    private WireMockServer notarySignAuthOauth2WireMockServer;

    @Getter
    private NotaryMockServer notaryMockServer;

    @Getter
    private WireMockServer notaryWireMockServer;

    private int notarySignAuthOAuth2ServiceJwksPollCount = 0;

    private AuthServers(IntegrationTestProperties integrationTestProperties,
            AuthProperties authProperties) {

        configureEssOperatorOauth2(integrationTestProperties.getOperator());
        configureEssTenantOauth2(integrationTestProperties.getTenant());
        configureTenantNotarySignAuthOauth2(integrationTestProperties.getTenant());
        configureTenantNotary(integrationTestProperties.getTenant(), authProperties.getServiceId());
    }

    private void configureEssOperatorOauth2(OperatorAuth operatorAuth) {

        operatorOauth2MockServer = new MultiOAuthMockServer(operatorAuth.getOauth2Server(),
                List.of(operatorAuth.getOauth2Client()));
        operatorOauth2WireMockServer = operatorOauth2MockServer.start();
    }


    private void configureEssTenantOauth2(TenantAuth tenantAuth) {

        tenantOauth2MockServer = new MultiOAuthMockServer(tenantAuth.getOauth2Server(), List.of(tenantAuth.getNsAdmin(),
                tenantAuth.getEntityAdmin(), tenantAuth.getSecretAdmin(), tenantAuth.getSecretConsumer()));
        tenantOauth2WireMockServer = tenantOauth2MockServer.start();
    }

    private void configureTenantNotarySignAuthOauth2(TenantAuth tenantAuth) {

        notarySignAuthOauth2MockServer = new MultiOAuthMockServer(tenantAuth.getNotarySignServer(),
                List.of(tenantAuth.getNotarySignClient()));
        notarySignAuthOauth2WireMockServer = notarySignAuthOauth2MockServer.start();
    }

    private void configureTenantNotary(TenantAuth tenantAuth, String audience) {

        notaryMockServer = new NotaryMockServer(tenantAuth.getNotary().getJwks(),
                tenantAuth.getNotary().getIss(),
                List.of(audience),
                "http://localhost:" + notarySignAuthOauth2WireMockServer.port());
        notaryWireMockServer = notaryMockServer.start();
    }

    public synchronized void verifyNotarySignAuthOauth2ServerJwksPolled(int count) {

        notarySignAuthOauth2WireMockServer.verify(exactly(notarySignAuthOAuth2ServiceJwksPollCount + count),
                getRequestedFor(urlEqualTo("/.well-known/jwks.json")));

        notarySignAuthOAuth2ServiceJwksPollCount += count;
    }

    @PreDestroy
    public void shutDown() {
        operatorOauth2MockServer.stop();
        tenantOauth2MockServer.stop();
        notaryMockServer.stop();
        notarySignAuthOauth2MockServer.stop();
    }
}