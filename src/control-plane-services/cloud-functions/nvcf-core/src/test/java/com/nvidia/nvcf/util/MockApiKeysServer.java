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
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.apikeys.dto.ApiKeyValidationResponse;
import java.net.URI;
import java.util.List;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import tools.jackson.databind.json.JsonMapper;

@UtilityClass
public class MockApiKeysServer {

    private static final JsonMapper OBJECT_MAPPER = new JsonMapper();
    @Getter
    private static WireMockServer mockApiKeysServer;

    @SneakyThrows
    public static void start(String apiKeysBaseUrl) {
        stop();
        mockApiKeysServer = new WireMockServer(new URI(apiKeysBaseUrl).getPort());
        mockApiKeysServer.start();
        resetToDefault();
    }

    public static void stop() {
        if (mockApiKeysServer != null) {
            mockApiKeysServer.stop();
        }
    }

    @SneakyThrows
    public static void setApiKeyValidationResponse(
            String ncaId,
            String ownerId,
            List<Resource> resources,
            List<String> scopes,
            boolean allowed) {
        setApiKeyValidationResponse(ncaId, ownerId, resources, scopes, allowed, null);
    }

    @SneakyThrows
    public static void setApiKeyValidationResponse(
            String ncaId,
            String ownerId,
            List<Resource> resources,
            List<String> scopes,
            boolean allowed,
            ApiKeyValidationResult.RateLimitAttributes accountTokenRateLimit) {
        stub("apikey.allow", "/v1/namespaces/nvcf/evaluations/apikey.allow",
             ncaId, ownerId, resources, scopes, allowed, accountTokenRateLimit);
    }

    // Same WireMockServer as apikey.allow, different path - LlmApiKeyClient defaults to
    // ApiKeysClient's own base-url.
    @SneakyThrows
    public static void setLlmApiKeyValidationResponse(
            String ncaId,
            String ownerId,
            List<Resource> resources,
            List<String> scopes,
            boolean allowed,
            ApiKeyValidationResult.RateLimitAttributes accountTokenRateLimit) {
        stub("apikey.llm_allow", "/v1/namespaces/nvcf/evaluations/apikey.llm_allow",
             ncaId, ownerId, resources, scopes, allowed, accountTokenRateLimit);
    }

    @SneakyThrows
    private static void stub(
            String policyName,
            String path,
            String ncaId,
            String ownerId,
            List<Resource> resources,
            List<String> scopes,
            boolean allowed,
            ApiKeyValidationResult.RateLimitAttributes accountTokenRateLimit) {
        var response = new ApiKeyValidationResponse("nvcf", policyName,
                                                       new ApiKeyValidationResult(allowed,
                                                              ncaId,
                                                              ownerId,
                                                              new ApiKeyValidationResult.Policy(resources,
                                                                                         scopes,
                                                                                         "nv-cloud-functions"),
                                                              accountTokenRateLimit
                                          ));
        byte[] responseBytes = OBJECT_MAPPER.writeValueAsBytes(response);
        mockApiKeysServer.stubFor(
                post(urlPathEqualTo(path))
                        .willReturn(aResponse().withStatus(200)
                                            .withHeader(HttpHeaders.CONTENT_TYPE,
                                                        MediaType.APPLICATION_JSON_VALUE)
                                            .withBody(responseBytes)));
    }

    @SneakyThrows
    public static void setResponse(
            String ncaId,
            String ownerId,
            List<ApiKeyValidationResult.Resource> resources,
            List<String> scopes) {
        setApiKeyValidationResponse(ncaId, ownerId, resources, scopes, true);
    }

    public static void resetToDefault() {
        setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(), List.of());
        setLlmApiKeyValidationResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(), List.of(), true, null);
    }
}
