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
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.core.WireMockConfiguration.wireMockConfig;
import static org.assertj.core.api.Assertions.assertThat;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.time.Duration;
import java.util.Map;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * Smoke test for {@link TelemetryWebClientFactory} against WireMock.
 */
class TelemetryWebClientFactoryIntegrationTest {

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
    void createTelemetryWebClientSendsRequest() {
        wireMockServer.stubFor(get(urlPathEqualTo("/ping"))
                .willReturn(aResponse().withBody("pong")));

        var base = "http://localhost:%d".formatted(wireMockServer.port());
        ExchangeFilterFunction noOp = (request, next) -> next.exchange(request);
        var webClient = TelemetryWebClientFactory.createTelemetryWebClient(
                WebClient.builder(),
                base,
                Duration.ofSeconds(5),
                Duration.ofSeconds(5),
                noOp);

        var body = webClient.get()
                .uri("/ping")
                .retrieve()
                .bodyToMono(String.class)
                .block();

        assertThat(body).isEqualTo("pong");
        wireMockServer.verify(getRequestedFor(urlPathEqualTo("/ping")));
    }

    @Test
    void createTokenWebClientPostsToAbsoluteUri() {
        wireMockServer.stubFor(post(urlPathEqualTo("/token"))
                .willReturn(aResponse()
                        .withHeader("Content-Type", "application/json")
                        .withBody("{\"access_token\":\"t\",\"expires_in\":60}")));

        var base = "http://localhost:%d".formatted(wireMockServer.port());
        var tokenUri = base + "/token";
        var webClient = TelemetryWebClientFactory.createTokenWebClient(
                WebClient.builder(),
                Duration.ofSeconds(5),
                Duration.ofSeconds(5));

        var map = webClient.post()
                .uri(tokenUri)
                .header("Content-Type", "application/x-www-form-urlencoded")
                .bodyValue("grant_type=client_credentials")
                .retrieve()
                .bodyToMono(new ParameterizedTypeReference<Map<String, Object>>() {})
                .block();

        assertThat(map).containsEntry("access_token", "t");
        wireMockServer.verify(postRequestedFor(urlPathEqualTo("/token")));
    }
}
