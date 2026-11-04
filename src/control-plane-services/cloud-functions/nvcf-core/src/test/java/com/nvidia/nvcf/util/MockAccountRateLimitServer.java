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
package com.nvidia.nvcf.util;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.RateLimitAttributes;
import com.nvidia.nvcf.service.apikeys.dto.ApiKeyValidationResponse;
import java.net.URI;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import tools.jackson.databind.json.JsonMapper;

@UtilityClass
public class MockAccountRateLimitServer {

    private static final JsonMapper OBJECT_MAPPER = new JsonMapper();
    @Getter
    private static WireMockServer mockAccountRateLimitServer;

    @SneakyThrows
    public static void start(String accountRateLimitBaseUrl) {
        stop();
        mockAccountRateLimitServer = new WireMockServer(new URI(accountRateLimitBaseUrl).getPort());
        mockAccountRateLimitServer.start();
        resetToDefault();
    }

    public static void stop() {
        if (mockAccountRateLimitServer != null) {
            mockAccountRateLimitServer.stop();
        }
    }

    @SneakyThrows
    public static void setTieredRateLimitResponse(RateLimitAttributes rateLimit) {
        var response = new ApiKeyValidationResponse("nvcf", "apikey.account_rate_limit", rateLimit);
        byte[] responseBytes = OBJECT_MAPPER.writeValueAsBytes(response);
        mockAccountRateLimitServer.stubFor(
                post(urlPathEqualTo("/v1/namespaces/nvcf/evaluations/apikey.account_rate_limit"))
                        .willReturn(aResponse().withStatus(200)
                                            .withHeader(HttpHeaders.CONTENT_TYPE,
                                                        MediaType.APPLICATION_JSON_VALUE)
                                            .withBody(responseBytes)));
    }

    public static void setUnavailable() {
        mockAccountRateLimitServer.stubFor(
                post(urlPathEqualTo("/v1/namespaces/nvcf/evaluations/apikey.account_rate_limit"))
                        .willReturn(aResponse().withStatus(503)));
    }

    public static void resetToDefault() {
        setTieredRateLimitResponse(new RateLimitAttributes(null, null));
    }
}
