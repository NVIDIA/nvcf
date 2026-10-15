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
package com.nvidia.nvct.util;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_OWNER_ID;
import static org.springframework.http.HttpHeaders.CONTENT_TYPE;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import tools.jackson.databind.json.JsonMapper;
import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.Policy;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvct.service.apikeys.dto.ApiKeyValidationResponse;
import java.net.URI;
import java.util.List;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;

@UtilityClass
public class MockApiKeysServer {

    public static final String EVALUATION_URI_PATH = "/v1/namespaces/nvct/evaluations/apikey.allow";

    private static final JsonMapper OBJECT_MAPPER = JsonMapper.builder().build();
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
        var result = new ApiKeyValidationResult(allowed,
                                                ncaId,
                                                ownerId,
                                                new Policy(resources,
                                                           scopes,
                                                           "nv-cloud-functions"));
        var response = new ApiKeyValidationResponse("nvct", "apikey.allow", result);
        byte[] responseBytes = OBJECT_MAPPER.writeValueAsBytes(response);
        mockApiKeysServer.stubFor(
                post(urlPathEqualTo(EVALUATION_URI_PATH))
                        .willReturn(aResponse().withStatus(200)
                                            .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
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
    }
}
