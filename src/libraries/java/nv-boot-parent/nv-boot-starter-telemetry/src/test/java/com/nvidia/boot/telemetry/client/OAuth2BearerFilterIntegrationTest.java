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

package com.nvidia.boot.telemetry.client;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.containing;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.matching;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.core.WireMockConfiguration.wireMockConfig;
import static org.assertj.core.api.Assertions.assertThat;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.time.Duration;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * Integration test: {@link OAuth2BearerFilter} fetches token then forwards request with Bearer
 * header.
 */
class OAuth2BearerFilterIntegrationTest {

    private WireMockServer wireMockServer;

    @BeforeEach
    void startServer() {
        wireMockServer = new WireMockServer(wireMockConfig().dynamicPort());
        wireMockServer.start();
    }

    @AfterEach
    void stopServer() {
        if (wireMockServer != null) {
            wireMockServer.stop();
        }
    }

    @Test
    void addsBearerTokenFromTokenEndpoint() {
        wireMockServer.stubFor(post(urlPathEqualTo("/oauth/token"))
                .willReturn(aResponse()
                        .withHeader("Content-Type", "application/json")
                        .withBody("{\"access_token\":\"test-access-token\",\"expires_in\":3600}")));
        wireMockServer.stubFor(get(urlPathEqualTo("/api/resource"))
                .willReturn(aResponse().withBody("payload")));

        var base = "http://localhost:%d".formatted(wireMockServer.port());
        var props = TelemetryTestFixtures.telemetryProperties(base, "/oauth/token");
        var tokenClient = TelemetryWebClientFactory.createTokenWebClient(
                WebClient.builder(),
                Duration.ofSeconds(5),
                Duration.ofSeconds(5));
        var filter = new OAuth2BearerFilter(props, tokenClient);

        var client = WebClient.builder()
                .baseUrl(base)
                .filter(filter)
                .build();

        var body = client.get()
                .uri("/api/resource")
                .retrieve()
                .bodyToMono(String.class)
                .block();

        assertThat(body).isEqualTo("payload");

        wireMockServer.verify(postRequestedFor(urlPathEqualTo("/oauth/token"))
                .withRequestBody(containing("grant_type=client_credentials")));
        wireMockServer.verify(getRequestedFor(urlPathEqualTo("/api/resource"))
                .withHeader("Authorization", equalTo("Bearer test-access-token")));
    }

    @Test
    void addsBearerTokenUsingClientSecretBasic() {
        wireMockServer.stubFor(post(urlPathEqualTo("/oauth/token"))
                .willReturn(aResponse()
                        .withHeader("Content-Type", "application/json")
                        .withBody("{\"access_token\":\"basic-token\",\"expires_in\":3600}")));
        wireMockServer.stubFor(get(urlPathEqualTo("/api/resource"))
                .willReturn(aResponse().withBody("payload")));

        var base = "http://localhost:%d".formatted(wireMockServer.port());
        var props = TelemetryTestFixtures.telemetryProperties(base, "/oauth/token");
        props.getOauth2().setAuthMethod(OAuth2AuthMethod.CLIENT_SECRET_BASIC);

        var tokenClient = TelemetryWebClientFactory.createTokenWebClient(
                WebClient.builder(),
                Duration.ofSeconds(5),
                Duration.ofSeconds(5));
        var filter = new OAuth2BearerFilter(props, tokenClient);

        var client = WebClient.builder()
                .baseUrl(base)
                .filter(filter)
                .build();

        var body = client.get()
                .uri("/api/resource")
                .retrieve()
                .bodyToMono(String.class)
                .block();

        assertThat(body).isEqualTo("payload");

        wireMockServer.verify(postRequestedFor(urlPathEqualTo("/oauth/token"))
                .withHeader("Authorization", matching("Basic .*"))
                .withRequestBody(equalTo("grant_type=client_credentials&scope=telemetry")));
        wireMockServer.verify(getRequestedFor(urlPathEqualTo("/api/resource"))
                .withHeader("Authorization", equalTo("Bearer basic-token")));
    }

    @Test
    void clientSecretPostIsDefaultBehavior() {
        wireMockServer.stubFor(post(urlPathEqualTo("/oauth/token"))
                .willReturn(aResponse()
                        .withHeader("Content-Type", "application/json")
                        .withBody("{\"access_token\":\"post-token\",\"expires_in\":3600}")));
        wireMockServer.stubFor(get(urlPathEqualTo("/api/resource"))
                .willReturn(aResponse().withBody("ok")));

        var base = "http://localhost:%d".formatted(wireMockServer.port());
        var props = TelemetryTestFixtures.telemetryProperties(base, "/oauth/token");

        var tokenClient = TelemetryWebClientFactory.createTokenWebClient(
                WebClient.builder(),
                Duration.ofSeconds(5),
                Duration.ofSeconds(5));
        var filter = new OAuth2BearerFilter(props, tokenClient);

        var client = WebClient.builder()
                .baseUrl(base)
                .filter(filter)
                .build();

        client.get().uri("/api/resource").retrieve().bodyToMono(String.class).block();

        wireMockServer.verify(postRequestedFor(urlPathEqualTo("/oauth/token"))
                .withRequestBody(containing("client_id="))
                .withRequestBody(containing("client_secret="))
                .withoutHeader("Authorization"));
    }
}
