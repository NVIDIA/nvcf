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
package com.nvidia.nvcf.configuration.notary;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.nvcf.configuration.IssuerAuthenticationManagerEntry;
import com.nvidia.nvcf.configuration.notary.NotaryConfiguration.NotaryConfigurationProperties;
import com.nvidia.nvcf.util.MockNotaryServer;
import com.nvidia.nvcf.util.NotaryTokenUtils;
import java.io.IOException;
import java.net.URI;
import java.util.Date;
import java.util.Map;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.boot.env.YamlPropertySourceLoader;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.StandardEnvironment;
import org.springframework.core.io.ClassPathResource;
import org.springframework.security.oauth2.core.OAuth2AuthenticationException;
import org.springframework.security.oauth2.jose.jws.SignatureAlgorithm;
import org.springframework.security.oauth2.server.resource.authentication.BearerTokenAuthenticationToken;
import org.springframework.test.util.ReflectionTestUtils;

/**
 * Resolves the shipped application.yaml so the defaults under test are the ones that ship, then
 * drives the real validator with them. Covers both the default chain and an operator override.
 */
class NotaryIssuerConfigurationTest {

    private static final String BASE_URL = "nvcf.notary.base-url";
    private static final String ISSUER_URI = "nvcf.notary.jwt.issuer-uri";
    private static final String JWK_SET_URI = "nvcf.notary.jwt.jwk-set-uri";

    private static final String NCP_NOTARY = "http://notary.nvcf.svc.cluster.local:8080";
    private static final String PUBLIC_ISSUER = "https://notary.public.example";
    private static final String MOCK_NOTARY = "http://localhost:9397";
    private static final String AUDIENCE = "nvcf-api";
    private static final String CLIENT_ID = "test-client";
    private static final String ASSERTION =
            "{\"ncaId\":\"test-nca\",\"clientId\":\"" + CLIENT_ID + "\"}";

    @BeforeAll
    static void startNotary() {
        MockNotaryServer.start(MOCK_NOTARY, CLIENT_ID, AUDIENCE);
    }

    @AfterAll
    static void stopNotary() {
        MockNotaryServer.stop();
    }

    @Test
    void issuerUriDefaultsToTheNotaryBaseUrl() throws IOException {
        var environment = environment(Map.of());

        assertThat(environment.getProperty(ISSUER_URI))
                .isEqualTo(environment.getProperty(BASE_URL))
                .isEqualTo(NCP_NOTARY);
    }

    @Test
    void jwkSetUriDefaultsToTheNotaryBaseUrl() throws IOException {
        var environment = environment(Map.of());

        assertThat(environment.getProperty(JWK_SET_URI))
                .isEqualTo(NCP_NOTARY + "/.well-known/jwks.json");
    }

    @Test
    void issuerUriFollowsAnOverriddenBaseUrl() throws IOException {
        var environment = environment(Map.of(BASE_URL, "http://notary.other.svc:8080"));

        assertThat(environment.getProperty(ISSUER_URI)).isEqualTo("http://notary.other.svc:8080");
    }

    // Setting the issuer alone also moves key fetching onto it, so a deployment pointing the
    // issuer at a public host must pin jwk-set-uri or it fetches signing keys over that host.
    @Test
    void jwkSetUriFollowsAnOverriddenIssuerUri() throws IOException {
        var environment = environment(Map.of(ISSUER_URI, PUBLIC_ISSUER));

        assertThat(environment.getProperty(BASE_URL)).isEqualTo(NCP_NOTARY);
        assertThat(environment.getProperty(JWK_SET_URI))
                .isEqualTo(PUBLIC_ISSUER + "/.well-known/jwks.json");
    }

    @Test
    void defaultConfigurationRegistersAndAcceptsTheNotaryBaseUrl() throws Exception {
        var manager = authManager(MOCK_NOTARY);

        assertThat(manager.issuer()).isEqualTo(MOCK_NOTARY);
        assertThatCode(() -> manager.authenticationManager().authenticate(bearer(MOCK_NOTARY)))
                .doesNotThrowAnyException();
    }

    @Test
    void overriddenConfigurationAcceptsTheIssuerUriAndRejectsTheBaseUrl() throws Exception {
        var manager = authManager(PUBLIC_ISSUER);

        assertThat(manager.issuer()).isEqualTo(PUBLIC_ISSUER);
        assertThatCode(() -> manager.authenticationManager().authenticate(bearer(PUBLIC_ISSUER)))
                .doesNotThrowAnyException();
        assertThatThrownBy(() -> manager.authenticationManager().authenticate(bearer(MOCK_NOTARY)))
                .isInstanceOf(OAuth2AuthenticationException.class);
    }

    private static IssuerAuthenticationManagerEntry authManager(String issuerUri) {
        var properties = new NotaryConfigurationProperties();
        properties.setIssuerUri(issuerUri);
        properties.setJwkSetUri(MOCK_NOTARY + "/.well-known/jwks.json");
        properties.setJwsAlgorithms(SignatureAlgorithm.ES256);

        var configuration = new NotaryAuthManagerConfiguration(properties);
        ReflectionTestUtils.setField(configuration, "nvcfAudience", AUDIENCE);
        return configuration.notaryAuthManager();
    }

    private static BearerTokenAuthenticationToken bearer(String issuer) throws Exception {
        return new BearerTokenAuthenticationToken(NotaryTokenUtils.getJwt(
                CLIENT_ID, ASSERTION, URI.create(issuer).toURL(), AUDIENCE, new Date()));
    }

    private static StandardEnvironment environment(Map<String, Object> overrides) throws IOException {
        var environment = new StandardEnvironment();
        var sources = environment.getPropertySources();
        // The shipped YAML is the subject here, so drop the JVM and OS sources
        // StandardEnvironment ranks ahead of it.
        sources.remove(StandardEnvironment.SYSTEM_PROPERTIES_PROPERTY_SOURCE_NAME);
        sources.remove(StandardEnvironment.SYSTEM_ENVIRONMENT_PROPERTY_SOURCE_NAME);
        if (!overrides.isEmpty()) {
            sources.addFirst(new MapPropertySource("operator", overrides));
        }
        var loader = new YamlPropertySourceLoader();
        // ncp before the base file: first source added wins, matching profile precedence.
        for (var source : loader.load("ncp", new ClassPathResource("application-ncp.yaml"))) {
            sources.addLast(source);
        }
        for (var source : loader.load("base", new ClassPathResource("application.yaml"))) {
            sources.addLast(source);
        }
        return environment;
    }
}
