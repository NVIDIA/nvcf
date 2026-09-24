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
import static org.springframework.test.context.support.TestPropertySourceUtils.INLINED_PROPERTIES_PROPERTY_SOURCE_NAME;

import com.nvidia.icms.integration.IntegrationTest;
import jakarta.servlet.http.HttpServletRequest;
import java.util.Base64;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.refresh.ContextRefresher;
import org.springframework.context.ApplicationContextInitializer;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.core.env.MapPropertySource;
import org.springframework.mock.web.MockHttpServletRequest;
import org.springframework.security.authentication.AuthenticationManagerResolver;
import org.springframework.security.authentication.AuthenticationServiceException;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.TestPropertySource;

/**
 * Not redundant with {@code AuthManagerResolverTest}, which calls the factory method directly and
 * passes whether or not the {@code @RefreshScope} annotations are present. Removing one would
 * silently restore the defect they exist to prevent, and this is what catches that.
 */
// ContextRefresher.copyEnvironment() keeps only commandLineArgs and defaultProperties, so the
// properties @SpringBootTest inlines — spring.profiles.active among them — are dropped on every
// refresh and ValidateEnvironmentPostProcessor then fails it. Retain that source explicitly.
@TestPropertySource(properties = "spring.cloud.refresh.additional-property-sources-to-retain="
        + INLINED_PROPERTIES_PROPERTY_SOURCE_NAME)
@ContextConfiguration(initializers = TrustedIssuerRefreshIntegrationTest.MutableOverrides.class)
class TrustedIssuerRefreshIntegrationTest extends IntegrationTest {

    private static final String ADDED_ISSUER = "http://api.external-nvcf.example.com";
    private static final String ADDED_JWKS = "http://openbao.external/v1/services/nvcf-api/jwt/jwks";

    private static final String TRUSTED_ISSUER_URI = "icms.security.jwt.trusted-issuers[0].issuer-uri";
    private static final String TRUSTED_JWK_SET_URI = "icms.security.jwt.trusted-issuers[0].jwk-set-uri";

    /** The property source below wraps this instance, so mutating it changes the environment. */
    private static final Map<String, Object> OVERRIDES = new HashMap<>();

    @Autowired
    private ContextRefresher contextRefresher;

    @Autowired
    private AuthenticationManagerResolver<HttpServletRequest> authenticationManagerResolver;

    @Value("${spring.security.oauth2.resourceserver.jwt.issuer-uri}")
    private String primaryIssuer;

    @Test
    void trustedIssuerAddedAndRemovedAtRuntime_appliesWithoutRestart() {
        HttpServletRequest primary = requestWithIssuer(primaryIssuer);
        HttpServletRequest added = requestWithIssuer(ADDED_ISSUER);

        // resolve() returns a manager for trusted and untrusted alike, deferring the issuer
        // lookup to authenticate(). The throw below is the discriminator: an untrusted iss falls
        // through to cluster OIDC, which rejects the missing nvcf-icms:{clusterId} audience.
        assertNotNull(authenticationManagerResolver.resolve(primary),
                "primary issuer must be trusted at startup");
        assertThrows(AuthenticationServiceException.class,
                () -> authenticationManagerResolver.resolve(added),
                "issuer must not be trusted before it is configured");

        OVERRIDES.put(TRUSTED_ISSUER_URI, ADDED_ISSUER);
        OVERRIDES.put(TRUSTED_JWK_SET_URI, ADDED_JWKS);
        contextRefresher.refresh();

        assertNotNull(authenticationManagerResolver.resolve(added),
                "issuer added at runtime must be accepted after a refresh, without a restart");
        assertNotNull(authenticationManagerResolver.resolve(primary),
                "primary issuer must still be trusted after the refresh");

        // Revocation is the half a stale set gets silently wrong.
        OVERRIDES.remove(TRUSTED_ISSUER_URI);
        OVERRIDES.remove(TRUSTED_JWK_SET_URI);
        contextRefresher.refresh();

        assertThrows(AuthenticationServiceException.class,
                () -> authenticationManagerResolver.resolve(added),
                "issuer removed at runtime must stop being trusted after a refresh");
        assertNotNull(authenticationManagerResolver.resolve(primary),
                "primary issuer must survive the removal refresh");
    }

    /** {@link IntegrationTest.Initializer} uses static TestPropertyValues; this one can change. */
    public static class MutableOverrides
            implements ApplicationContextInitializer<ConfigurableApplicationContext> {

        @Override
        public void initialize(ConfigurableApplicationContext applicationContext) {
            applicationContext.getEnvironment().getPropertySources()
                    .addFirst(new MapPropertySource("trusted-issuer-overrides", OVERRIDES));
        }
    }

    private static HttpServletRequest requestWithIssuer(String issuer) {
        String payload = String.format("{\"iss\":\"%s\",\"sub\":\"probe\",\"aud\":[\"x\"]}", issuer);
        String b64 = Base64.getUrlEncoder().withoutPadding().encodeToString(payload.getBytes());
        MockHttpServletRequest request = new MockHttpServletRequest();
        request.addHeader("Authorization", "Bearer eyJhbGciOiJFUzI1NiJ9." + b64 + ".sig");
        return request;
    }
}
