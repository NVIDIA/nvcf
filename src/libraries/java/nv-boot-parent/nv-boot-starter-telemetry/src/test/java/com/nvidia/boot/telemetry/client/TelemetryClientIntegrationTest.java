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
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.core.WireMockConfiguration.wireMockConfig;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.github.tomakehurst.wiremock.WireMockServer;
import java.time.Duration;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * End-to-end HTTP test: token exchange + telemetry POST with OAuth2 filter.
 */
class TelemetryClientIntegrationTest {

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
    void sendPostsCloudEventsWithBearerToken() throws Exception {
        wireMockServer.stubFor(post(urlPathEqualTo("/oauth/token"))
                .willReturn(aResponse()
                        .withHeader("Content-Type", "application/json")
                        .withBody("{\"access_token\":\"tok-xyz\",\"expires_in\":3600}")));
        wireMockServer.stubFor(post(urlPathEqualTo("/api/v2/topic/my-topic"))
                .willReturn(aResponse()
                        .withStatus(200)
                        .withHeader("Content-Type", "application/json")
                        .withBody("{\"result\":\"ok\"}")));

        var base = "http://localhost:%d".formatted(wireMockServer.port());
        var props = TelemetryTestFixtures.telemetryProperties(base, "/oauth/token");
        var connect = Duration.ofSeconds(5);
        var read = Duration.ofSeconds(10);
        var tokenClient = TelemetryWebClientFactory.createTokenWebClient(
                WebClient.builder(),connect, read);
        var oauthFilter = new OAuth2BearerFilter(props, tokenClient);
        var webClient = TelemetryWebClientFactory.createTelemetryWebClient(
                WebClient.builder(), base, connect, read, oauthFilter);
        var client = new TelemetryClient(props, JsonMapper.builder().build(), webClient);

        var response = client.send("my-topic", TelemetryTestFixtures.sampleCloudEvents());

        assertThat(response.getStatusCode()).isEqualTo(200);
        assertThat(response.getBody()).containsEntry("result", "ok");

        wireMockServer.verify(postRequestedFor(urlPathEqualTo("/oauth/token")));
        wireMockServer.verify(postRequestedFor(urlPathEqualTo("/api/v2/topic/my-topic"))
                .withHeader("Authorization", equalTo("Bearer tok-xyz"))
                .withHeader("Content-Type", containing("application/cloudevents-batch")));
    }

    @Test
    void sendNormalizesTrailingSlashOnPathPrefix() throws Exception {
        wireMockServer.stubFor(post(urlPathEqualTo("/oauth/token"))
                .willReturn(aResponse()
                        .withHeader("Content-Type", "application/json")
                        .withBody("{\"access_token\":\"tok-xyz\",\"expires_in\":3600}")));
        wireMockServer.stubFor(post(urlPathEqualTo("/api/v2/topic/my-topic"))
                .willReturn(aResponse()
                        .withStatus(200)
                        .withHeader("Content-Type", "application/json")
                        .withBody("{}")));

        var base = "http://localhost:%d".formatted(wireMockServer.port());
        var props = TelemetryTestFixtures.telemetryProperties(base, "/oauth/token");
        props.setPathPrefix("/api/v2/topic/");
        var connect = Duration.ofSeconds(5);
        var read = Duration.ofSeconds(10);
        var tokenClient = TelemetryWebClientFactory.createTokenWebClient(
                WebClient.builder(), connect, read);
        var oauthFilter = new OAuth2BearerFilter(props, tokenClient);
        var webClient = TelemetryWebClientFactory.createTelemetryWebClient(
                WebClient.builder(), base, connect, read, oauthFilter);
        var client = new TelemetryClient(props, JsonMapper.builder().build(), webClient);

        client.send("my-topic", TelemetryTestFixtures.sampleCloudEvents());

        wireMockServer.verify(1, postRequestedFor(urlPathEqualTo("/api/v2/topic/my-topic")));
    }
}
