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
package com.nvidia.nvcf.service.apikeys;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.equalToJson;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientApiKeysProperties;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.RateLimitAttributes;
import java.util.Optional;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.reactive.function.client.WebClient;
import tools.jackson.databind.json.JsonMapper;

class ApiKeysClientTest {

    private static final String EVALUATION_URI = "/v1/namespaces/nvcf/evaluations/apikey.allow";
    private static final String LLM_EVALUATION_URI = "/v1/namespaces/nvcf/evaluations/apikey.llm_allow";
    private static final String ACCOUNT_RATE_LIMIT_EVALUATION_URI =
            "/v1/namespaces/nvcf/evaluations/apikey.account_rate_limit";

    private WireMockServer server;
    private ApiKeysClient client;

    @BeforeEach
    void setUp() {
        server = new WireMockServer(WireMockConfiguration.options().dynamicPort());
        server.start();
        var staticProperties = new StaticClientApiKeysProperties();
        staticProperties.setToken("test-token");
        client = new ApiKeysClient(
                server.baseUrl(), EVALUATION_URI, "key",
                LLM_EVALUATION_URI,
                ACCOUNT_RATE_LIMIT_EVALUATION_URI, "account",
                "unused", "unused", "unused", "unused",
                Optional.of(staticProperties), WebClient.builder(), new JsonMapper());
    }

    @AfterEach
    void tearDown() {
        server.stop();
    }

    private void stubResponse(String uri, String requestBody, String responseBody) {
        server.stubFor(post(urlPathEqualTo(uri))
                               .withRequestBody(equalToJson(requestBody))
                               .willReturn(aResponse().withStatus(200)
                                                   .withHeader(HttpHeaders.CONTENT_TYPE,
                                                               MediaType.APPLICATION_JSON_VALUE)
                                                   .withBody(responseBody)));
    }

    @Test
    void fetchApiKeyValidationResult_usesGenericEvaluation() {
        stubResponse(EVALUATION_URI, """
                {"input":{"key":"key-1"}}
                """, """
                {"namespace":"nvcf","rule_name":"apikey.allow",
                 "result":{"allowed":true,"ncaId":"nca-1","ownerId":"owner-1","policy":{}}}
                """);

        assertThat(client.fetchApiKeyValidationResult("key-1").ncaId()).isEqualTo("nca-1");
    }

    @Test
    void fetchApiKeyValidationResult_forbiddenWhenNotAllowed() {
        stubResponse(EVALUATION_URI, """
                {"input":{"key":"key-1"}}
                """, """
                {"namespace":"nvcf","rule_name":"apikey.allow","result":{"allowed":false}}
                """);

        assertThatThrownBy(() -> client.fetchApiKeyValidationResult("key-1"))
                .isInstanceOf(ForbiddenException.class);
    }

    @Test
    void fetchApiKeyValidationResult_forbiddenWhenResultAbsent() {
        stubResponse(EVALUATION_URI, """
                {"input":{"key":"key-1"}}
                """, """
                {"namespace":"nvcf","rule_name":"apikey.allow"}
                """);

        assertThatThrownBy(() -> client.fetchApiKeyValidationResult("key-1"))
                .isInstanceOf(ForbiddenException.class);
    }

    @Test
    void fetchLlmApiKeyValidationResult_usesLlmEvaluation() {
        stubResponse(LLM_EVALUATION_URI, """
                {"input":{"key":"key-1"}}
                """, """
                {"namespace":"nvcf","rule_name":"apikey.llm_allow",
                 "result":{"allowed":true,"ncaId":"nca-1","ownerId":"owner-1","policy":{},
                           "accountTokenRateLimit":{"inputTokenRateLimit":"1000-M","outputTokenRateLimit":"500-M"}}}
                """);

        var result = client.fetchLlmApiKeyValidationResult("key-1");

        assertThat(result.ncaId()).isEqualTo("nca-1");
        assertThat(result.accountTokenRateLimit()).isEqualTo(new RateLimitAttributes("1000-M", "500-M"));
    }

    @Test
    void fetchTieredRateLimit_returnsRates() {
        stubResponse(ACCOUNT_RATE_LIMIT_EVALUATION_URI, """
                {"input":{"account":"nca-1"}}
                """, """
                {"namespace":"nvcf","rule_name":"apikey.account_rate_limit",
                 "result":{"inputTokenRateLimit":"1000-M","outputTokenRateLimit":"500-M"}}
                """);

        assertThat(client.fetchTieredRateLimit("nca-1"))
                .isEqualTo(new RateLimitAttributes("1000-M", "500-M"));
    }

    @Test
    void fetchTieredRateLimit_returnsNoLimitWhenResultAbsent() {
        stubResponse(ACCOUNT_RATE_LIMIT_EVALUATION_URI, """
                {"input":{"account":"nca-1"}}
                """, """
                {"namespace":"nvcf","rule_name":"apikey.account_rate_limit"}
                """);

        assertThat(client.fetchTieredRateLimit("nca-1"))
                .isEqualTo(new RateLimitAttributes(null, null));
    }
}
