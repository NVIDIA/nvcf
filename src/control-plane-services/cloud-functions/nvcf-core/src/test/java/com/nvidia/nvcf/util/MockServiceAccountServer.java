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
public class MockServiceAccountServer {

    private static final JsonMapper OBJECT_MAPPER = new JsonMapper();
    @Getter
    private static WireMockServer mockServiceAccountServer;

    @SneakyThrows
    public static void start(String serviceAccountBaseUrl) {
        stop();
        mockServiceAccountServer = new WireMockServer(new URI(serviceAccountBaseUrl).getPort());
        mockServiceAccountServer.start();
        resetToDefault();
    }

    public static void stop() {
        if (mockServiceAccountServer != null) {
            mockServiceAccountServer.stop();
        }
    }

    @SneakyThrows
    public static void setTieredRateLimitResponse(RateLimitAttributes rateLimit) {
        var response = new ApiKeyValidationResponse("nvcf", "ssa.allow", rateLimit);
        byte[] responseBytes = OBJECT_MAPPER.writeValueAsBytes(response);
        mockServiceAccountServer.stubFor(
                post(urlPathEqualTo("/v1/namespaces/nvcf/evaluations/ssa.allow"))
                        .willReturn(aResponse().withStatus(200)
                                            .withHeader(HttpHeaders.CONTENT_TYPE,
                                                        MediaType.APPLICATION_JSON_VALUE)
                                            .withBody(responseBytes)));
    }

    public static void setUnavailable() {
        mockServiceAccountServer.stubFor(
                post(urlPathEqualTo("/v1/namespaces/nvcf/evaluations/ssa.allow"))
                        .willReturn(aResponse().withStatus(503)));
    }

    public static void resetToDefault() {
        setTieredRateLimitResponse(new RateLimitAttributes(null, null));
    }
}
