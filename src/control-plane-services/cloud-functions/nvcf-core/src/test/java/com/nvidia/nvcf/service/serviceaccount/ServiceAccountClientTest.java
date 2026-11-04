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
package com.nvidia.nvcf.service.serviceaccount;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static org.assertj.core.api.Assertions.assertThat;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientServiceAccountProperties;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.RateLimitAttributes;
import java.util.Optional;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.reactive.function.client.WebClient;
import tools.jackson.databind.json.JsonMapper;

class ServiceAccountClientTest {

    private static final String EVALUATION_URI = "/v1/namespaces/nvcf/evaluations/ssa.allow";

    private WireMockServer server;
    private ServiceAccountClient client;

    @BeforeEach
    void setUp() {
        server = new WireMockServer(WireMockConfiguration.options().dynamicPort());
        server.start();
        var staticProperties = new StaticClientServiceAccountProperties();
        staticProperties.setToken("test-token");
        client = new ServiceAccountClient(
                server.baseUrl(), EVALUATION_URI, "tiered_rate_key",
                "unused", "unused", "unused", "unused",
                Optional.of(staticProperties), WebClient.builder(), new JsonMapper());
    }

    @AfterEach
    void tearDown() {
        server.stop();
    }

    private void stubResponse(String body) {
        server.stubFor(post(urlPathEqualTo(EVALUATION_URI))
                               .willReturn(aResponse().withStatus(200)
                                                   .withHeader(HttpHeaders.CONTENT_TYPE,
                                                               MediaType.APPLICATION_JSON_VALUE)
                                                   .withBody(body)));
    }

    @Test
    void fetchTieredRateLimit_returnsRates() {
        stubResponse("""
                {"namespace":"nvcf","rule_name":"ssa.allow",
                 "result":{"inputTokenRateLimit":"1000-M","outputTokenRateLimit":"500-M"}}
                """);

        assertThat(client.fetchTieredRateLimit("nca-1"))
                .isEqualTo(new RateLimitAttributes("1000-M", "500-M"));
    }

    @Test
    void fetchTieredRateLimit_returnsNoLimitWhenResultAbsent() {
        stubResponse("""
                {"namespace":"nvcf","rule_name":"ssa.allow"}
                """);

        assertThat(client.fetchTieredRateLimit("nca-1"))
                .isEqualTo(new RateLimitAttributes(null, null));
    }
}
