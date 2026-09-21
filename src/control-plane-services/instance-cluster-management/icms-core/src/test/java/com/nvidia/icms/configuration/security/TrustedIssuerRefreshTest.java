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
package com.nvidia.icms.configuration.security;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.*;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.outbound.apikeys.ApiKeysService;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import jakarta.servlet.http.HttpServletRequest;
import java.util.Base64;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.cloud.autoconfigure.RefreshAutoConfiguration;
import org.springframework.cloud.context.refresh.ContextRefresher;
import org.springframework.context.annotation.AnnotationConfigApplicationContext;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.env.MapPropertySource;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.authentication.AuthenticationServiceException;

/**
 * Ties the {@code @RefreshScope} annotations on {@link AuthManagerResolver} and
 * {@link TrustedJwtIssuerProperties} to the behavior they exist for, by driving a real
 * {@link ContextRefresher#refresh()} over the real beans through the same resolver handle a
 * {@code SecurityFilterChain} holds.
 *
 * <p>This is not redundant with {@code AuthManagerResolverTest}: those tests call the factory
 * method directly and pass whether or not either annotation is present. Without this test,
 * removing {@code @RefreshScope} silently restores the defect this class exists to prevent —
 * configuration accepted, never applied, and revocation that appears to work but does not.</p>
 */
class TrustedIssuerRefreshTest {

    private static final String APPLICATION_NAME = "spring.application.name";
    private static final String ACTIVE_PROFILES = "spring.profiles.active";

    private static final String PRIMARY_ISSUER = "http://api.sis.svc.cluster.local";
    private static final String PRIMARY_JWKS = "http://openbao/v1/services/sis-api/jwt/jwks";
    private static final String ADDED_ISSUER = "http://api.external-nvcf.example.com";
    private static final String ADDED_JWKS = "http://openbao.external/v1/services/nvcf-api/jwt/jwks";

    private static final String TRUSTED_ISSUER_URI = "icms.security.jwt.trusted-issuers[0].issuer-uri";
    private static final String TRUSTED_JWK_SET_URI = "icms.security.jwt.trusted-issuers[0].jwk-set-uri";

    @Test
    void trustedIssuerAddedAndRemovedAtRuntime_appliesWithoutRestart() {
        Map<String, Object> source = new HashMap<>(Map.of(
                "spring.security.oauth2.resourceserver.jwt.issuer-uri", PRIMARY_ISSUER,
                "spring.security.oauth2.resourceserver.jwt.jwk-set-uri", PRIMARY_JWKS,
                "icms.nvca.api-key.enabled", "false"));

        // ContextRefresher re-reads the Environment through a SpringApplication, which this
        // codebase's ValidateEnvironmentPostProcessor gates on these two being set. Tests share
        // one JVM, so capture whatever was there and put it back rather than clearing blindly.
        String priorName = System.getProperty(APPLICATION_NAME);
        String priorProfiles = System.getProperty(ACTIVE_PROFILES);
        System.setProperty(APPLICATION_NAME, "trusted-issuer-refresh-test");
        System.setProperty(ACTIVE_PROFILES, "test");
        try (var ctx = new AnnotationConfigApplicationContext()) {
            ctx.getEnvironment().getPropertySources()
                    .addFirst(new MapPropertySource("test-source", source));
            ctx.register(RefreshAutoConfiguration.class, ResolverDependencies.class,
                         TrustedJwtIssuerProperties.class, AuthManagerResolver.class);
            ctx.refresh();

            @SuppressWarnings("unchecked")
            AuthenticationManagerResolver<HttpServletRequest> resolver =
                    ctx.getBean(AuthenticationManagerResolver.class);

            HttpServletRequest primary = requestWithIssuer(PRIMARY_ISSUER);
            HttpServletRequest added = requestWithIssuer(ADDED_ISSUER);

            assertNotNull(resolver.resolve(primary), "primary issuer must be trusted at startup");
            assertThrows(AuthenticationServiceException.class, () -> resolver.resolve(added),
                    "issuer must not be trusted before it is configured");

            // Add the issuer the way an operator would — a new entry in the property source.
            source.put(TRUSTED_ISSUER_URI, ADDED_ISSUER);
            source.put(TRUSTED_JWK_SET_URI, ADDED_JWKS);
            ctx.getBean(ContextRefresher.class).refresh();

            assertNotNull(resolver.resolve(added),
                    "issuer added at runtime must be accepted after a refresh, without a restart");
            assertNotNull(resolver.resolve(primary),
                    "primary issuer must still be trusted after the refresh");

            // Revocation must apply too — this is the case a stale set gets silently wrong.
            source.remove(TRUSTED_ISSUER_URI);
            source.remove(TRUSTED_JWK_SET_URI);
            ctx.getBean(ContextRefresher.class).refresh();

            assertEquals(List.of(),
                    ctx.getBean(TrustedJwtIssuerProperties.class).getTrustedIssuers(),
                    "the removal must reach the bound properties");
            assertThrows(AuthenticationServiceException.class, () -> resolver.resolve(added),
                    "issuer removed at runtime must stop being trusted after a refresh");
            assertNotNull(resolver.resolve(primary),
                    "primary issuer must survive the removal refresh");
        } finally {
            restoreProperty(APPLICATION_NAME, priorName);
            restoreProperty(ACTIVE_PROFILES, priorProfiles);
        }
    }

    @Configuration(proxyBeanMethods = false)
    static class ResolverDependencies {

        @Bean
        ApiKeysService apiKeysService() {
            return mock(ApiKeysService.class);
        }

        @Bean
        ClusterRepository clusterRepository() {
            return mock(ClusterRepository.class);
        }

        @Bean
        NvcaConfigurationProperties nvcaConfigurationProperties() {
            // Flag on so an untrusted iss falls through to cluster OIDC and throws on the missing
            // nvcf-icms:{clusterId} audience. That throw is what distinguishes "not trusted" here:
            // JwtIssuerAuthenticationManagerResolver.resolve() returns a manager either way and
            // defers the issuer lookup to authenticate().
            NvcaConfigurationProperties cfg = new NvcaConfigurationProperties();
            cfg.setOidcClusterIdentityEnabled(true);
            return cfg;
        }
    }

    private static HttpServletRequest requestWithIssuer(String issuer) {
        String payload = String.format("{\"iss\":\"%s\",\"sub\":\"probe\",\"aud\":[\"x\"]}", issuer);
        String b64 = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        HttpServletRequest req = mock(HttpServletRequest.class);
        when(req.getHeader("Authorization"))
                .thenReturn("Bearer eyJhbGciOiJFUzI1NiJ9." + b64 + ".sig");
        return req;
    }

    private static void restoreProperty(String key, String priorValue) {
        if (priorValue == null) {
            System.clearProperty(key);
        } else {
            System.setProperty(key, priorValue);
        }
    }
}
