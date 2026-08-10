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
package com.nvidia.notary;

import static org.assertj.core.api.Assertions.assertThat;

import java.net.URI;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.web.server.LocalManagementPort;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.web.client.RestTemplate;

/**
 * OSS executable ({@link App} + {@code notary-core}) smoke test. Boots the real service
 * application context under the shared {@code integrationtest} profile — its signing keys
 * (vault-agent fixture) and OAuth2 issuer config are sourced from the {@code notary-core}
 * test-jar — and asserts the service serves its public JWKS and reports healthy. Parallels
 * the {@code nvcf-service} and {@code nvct-service} integration smoke tests.
 *
 * <p>No OAuth2 token server is required: the asserted endpoints are unauthenticated
 * ({@code permitAll}) and the {@code JwtDecoder} fetches the JWK set lazily, so the context
 * boots without an issuer reachable on the wire.
 */
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@AutoConfigureTestRestTemplate
class NotaryServiceIntegrationTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @LocalManagementPort
    private int managementPort;

    @Test
    void jwksEndpointReturnsOk() {
        var response = testRestTemplate.exchange(
                RequestEntity.get(URI.create("/.well-known/jwks.json")).build(),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody()).contains("keys");
    }

    @Test
    void actuatorHealthReturnsOk() {
        var endpoint = URI.create("http://localhost:" + managementPort + "/actuator/health");
        var response = new RestTemplate().exchange(RequestEntity.get(endpoint).build(), String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody()).containsIgnoringCase("status");
    }
}
