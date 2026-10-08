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
package com.nvidia.nvct.service.token;

import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvct.configuration.NotaryJwtConfiguration;
import com.nvidia.nvct.util.MockNotaryServer;
import java.io.IOException;
import java.time.Clock;
import java.time.Instant;
import java.util.Map;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.boot.env.YamlPropertySourceLoader;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.StandardEnvironment;
import org.springframework.core.io.ClassPathResource;
import tools.jackson.databind.json.JsonMapper;

/**
 * Resolves the shipped application.yaml so the defaults under test are the ones that ship, then
 * drives the real validator with them. Covers both the default chain and an operator override.
 */
class NotaryIssuerConfigurationTest {

    private static final String BASE_URL = "nvct.notary.base-url";
    private static final String ISSUER_URI = "nvct.notary.jwt.issuer-uri";
    private static final String JWK_SET_URI = "nvct.notary.jwt.jwk-set-uri";

    private static final String NCP_NOTARY = "http://notary.nvcf.svc.cluster.local:8080";
    private static final String PUBLIC_ISSUER = "https://notary.public.example";
    private static final String MOCK_NOTARY = "http://localhost:9397";
    private static final String CLIENT_ID = "test-client";

    @BeforeAll
    static void startNotary() {
        MockNotaryServer.start(MOCK_NOTARY, CLIENT_ID);
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
    void defaultConfigurationAcceptsAnAssertionFromTheNotaryBaseUrl() {
        var validator = validator(MOCK_NOTARY);

        assertThatCode(() -> validator.validate(assertion(MOCK_NOTARY), TEST_NCA_ID, TEST_TASK_ID_1))
                .doesNotThrowAnyException();
    }

    @Test
    void overriddenConfigurationAcceptsTheIssuerUriAndRejectsTheBaseUrl() {
        var validator = validator(PUBLIC_ISSUER);

        assertThatCode(() -> validator.validate(assertion(PUBLIC_ISSUER), TEST_NCA_ID, TEST_TASK_ID_1))
                .doesNotThrowAnyException();
        assertThatThrownBy(() -> validator.validate(assertion(MOCK_NOTARY), TEST_NCA_ID, TEST_TASK_ID_1))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("issuer");
    }

    private static WorkerAssertionValidator validator(String issuerUri) {
        var decoder = new NotaryJwtConfiguration()
                .notaryJwtDecoder(MOCK_NOTARY + "/.well-known/jwks.json");
        return new WorkerAssertionValidator(
                decoder, new JsonMapper(), Clock.systemUTC(), issuerUri, CLIENT_ID);
    }

    private static String assertion(String issuer) {
        return MockNotaryServer.generateSignedWorkerAssertion(
                issuer, CLIENT_ID, TEST_NCA_ID, TEST_TASK_ID_1, Instant.now());
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
