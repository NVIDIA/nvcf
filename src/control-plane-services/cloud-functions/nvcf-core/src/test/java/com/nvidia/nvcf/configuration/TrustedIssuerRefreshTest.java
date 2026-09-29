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
package com.nvidia.nvcf.configuration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.ECDSASigner;
import com.nimbusds.jose.jwk.Curve;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.gen.ECKeyGenerator;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.nvcf.service.apikeys.ApiKeysService;
import jakarta.servlet.http.HttpServletRequest;
import java.util.HashMap;
import java.util.Map;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.test.context.assertj.AssertableApplicationContext;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.cloud.autoconfigure.ConfigurationPropertiesRebinderAutoConfiguration;
import org.springframework.cloud.autoconfigure.RefreshAutoConfiguration;
import org.springframework.cloud.context.refresh.ContextRefresher;
import org.springframework.core.env.MapPropertySource;
import org.springframework.http.HttpHeaders;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.authentication.AuthenticationServiceException;
import org.springframework.security.oauth2.server.resource.InvalidBearerTokenException;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;

/**
 * Covers picking up {@code nvcf.security.jwt.trusted-issuers[]} changes at runtime.
 *
 * <p>Boots a real (minimal) Spring context with {@link RefreshAutoConfiguration} so
 * {@code @RefreshScope} is honoured, then drives {@link ContextRefresher#refresh()} — the same
 * entry point Spring Cloud Kubernetes ConfigMap reload and the reloadable-properties file
 * watcher both call. {@link JwtAuthManagerConfigurationTest} calls the bean factory method
 * directly and so passes whether or not the {@code @RefreshScope} annotations are present;
 * this is the test that fails if one is dropped.
 */
class TrustedIssuerRefreshTest {

    private static final String PRIMARY_ISS = "http://api.nvcf.svc.cluster.local";
    private static final String ADDED_ISS = "https://idp.example.com";
    private static final String JWKS = "http://127.0.0.1:1/jwks";

    private static final String ISSUER_URI_KEY = "nvcf.security.jwt.trusted-issuers[0].issuer-uri";
    private static final String JWK_SET_URI_KEY =
            "nvcf.security.jwt.trusted-issuers[0].jwk-set-uri";

    private static final String OVERRIDES_SOURCE = "trusted-issuer-overrides";

    /** The property source below wraps this instance, so mutating it changes the environment. */
    private static final Map<String, Object> OVERRIDES = new HashMap<>();

    private static ECKey signingKey;

    @BeforeAll
    static void generateSigningKey() throws Exception {
        signingKey = new ECKeyGenerator(Curve.P_256).keyID("test").generate();
    }

    private final ApplicationContextRunner runner = new ApplicationContextRunner()
            .withConfiguration(AutoConfigurations.of(
                    RefreshAutoConfiguration.class,
                    ConfigurationPropertiesRebinderAutoConfiguration.class))
            .withUserConfiguration(
                    TrustedJwtIssuerProperties.class,
                    JwtAuthManagerConfiguration.class,
                    AuthManagerResolverConfiguration.class)
            .withBean(ApiKeysService.class, () -> mock(ApiKeysService.class))
            .withInitializer(context -> context.getEnvironment().getPropertySources()
                    .addFirst(new MapPropertySource(OVERRIDES_SOURCE, OVERRIDES)))
            // ContextRefresher.copyEnvironment() keeps only the default sources, so everything
            // the rebuilt environment still needs has to live in a source it is told to retain.
            .withPropertyValues("spring.cloud.refresh.additional-property-sources-to-retain="
                    + OVERRIDES_SOURCE);

    /** Everything the rebuilt environment needs, minus the trusted issuer under test. */
    private static void resetOverrides() {
        OVERRIDES.clear();
        // nv-boot's ValidateEnvironmentPostProcessor rejects an environment without these.
        OVERRIDES.put("spring.application.name", "nvcf-api");
        OVERRIDES.put("spring.profiles.active", "test");
        OVERRIDES.put("spring.security.oauth2.resourceserver.jwt.issuer-uri", PRIMARY_ISS);
        OVERRIDES.put("spring.security.oauth2.resourceserver.jwt.jwk-set-uri", JWKS);
        OVERRIDES.put("spring.security.oauth2.resourceserver.jwt.jws-algorithms", "ES256");
    }

    @Test
    void issuerAddedThenRemovedAtRuntimeAppliesWithoutRestart() {
        resetOverrides();
        runner.run(context -> {
            assertRouted(context, PRIMARY_ISS);
            assertNotTrusted(context, ADDED_ISS);

            OVERRIDES.put(ISSUER_URI_KEY, ADDED_ISS);
            OVERRIDES.put(JWK_SET_URI_KEY, JWKS);
            context.getBean(ContextRefresher.class).refresh();

            assertRouted(context, ADDED_ISS);
            assertRouted(context, PRIMARY_ISS);

            // Revocation is the half a stale issuer set gets silently wrong.
            OVERRIDES.remove(ISSUER_URI_KEY);
            OVERRIDES.remove(JWK_SET_URI_KEY);
            context.getBean(ContextRefresher.class).refresh();

            assertNotTrusted(context, ADDED_ISS);
            assertRouted(context, PRIMARY_ISS);
        });
    }

    /** A rotated JWKS must take effect too, not just an added or removed issuer. */
    @Test
    void jwkSetUriChangedAtRuntimeIsRebound() {
        resetOverrides();
        OVERRIDES.put(ISSUER_URI_KEY, ADDED_ISS);
        OVERRIDES.put(JWK_SET_URI_KEY, JWKS);
        runner.run(context -> {
            assertRouted(context, ADDED_ISS);

            OVERRIDES.put(JWK_SET_URI_KEY, "http://127.0.0.1:2/rotated/jwks");
            context.getBean(ContextRefresher.class).refresh();

            assertThat(context.getBean(TrustedJwtIssuerProperties.class).getTrustedIssuers())
                    .singleElement()
                    .satisfies(entry -> assertThat(entry.getJwkSetUri())
                            .isEqualTo("http://127.0.0.1:2/rotated/jwks"));
            assertRouted(context, ADDED_ISS);
        });
    }

    /** Reaching the (absent) JWKS server proves the issuer was routed to a manager. */
    private static void assertRouted(AssertableApplicationContext context, String issuer) {
        assertThatThrownBy(() -> authenticate(context, issuer))
                .isInstanceOf(AuthenticationServiceException.class);
    }

    private static void assertNotTrusted(AssertableApplicationContext context, String issuer) {
        assertThatThrownBy(() -> authenticate(context, issuer))
                .isInstanceOf(InvalidBearerTokenException.class)
                .hasMessage("Invalid issuer");
    }

    @SuppressWarnings("unchecked")
    private static void authenticate(AssertableApplicationContext context, String issuer) {
        var resolver = (AuthenticationManagerResolver<HttpServletRequest>)
                context.getBean(AuthenticationManagerResolver.class);
        var token = signedToken(issuer);
        var request = new MockHttpServletRequest();
        request.addHeader(HttpHeaders.AUTHORIZATION, "Bearer " + token);
        resolver.resolve(request).authenticate(new BearerTokenAuthenticationToken(token));
    }

    private static String signedToken(String issuer) {
        try {
            var jwt = new SignedJWT(
                    new JWSHeader.Builder(JWSAlgorithm.ES256).keyID(signingKey.getKeyID()).build(),
                    new JWTClaimsSet.Builder().issuer(issuer).subject("someone").build());
            jwt.sign(new ECDSASigner(signingKey));
            return jwt.serialize();
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }
}
