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

package com.nvidia.boot.mock.docker;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;

public class MockDockerRegistryAuthServer {

    // Proxy auth endpoint with scope - for artifact validation
    private static final String PROXY_AUTH_URL = "/token\\?service=registry\\.docker\\" +
            ".io&scope=repository:.*:pull";
    
    // Credential validation endpoint without scope - for credential validation only
    private static final String FETCH_TOKEN_URL = "/token\\?service=registry\\.docker\\.io$";
    
    private static final String PROXY_AUTH = """
            {
                "token": "mockBearerToken"
            }
            """;
    private static WireMockServer dockerRegistryAuthMockServer;

    @SneakyThrows
    public static void start(String ngcRegistryBaseUrl) {
        stop();
        dockerRegistryAuthMockServer = new WireMockServer(URI.create(ngcRegistryBaseUrl).getPort());
        dockerRegistryAuthMockServer.start();

        // Proxy auth endpoint with scope (artifact validation)
        dockerRegistryAuthMockServer
                .stubFor(get(urlMatching(PROXY_AUTH_URL))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(PROXY_AUTH)));
        
        // Fetch token endpoint without scope (credential validation)
        dockerRegistryAuthMockServer
                .stubFor(get(urlMatching(FETCH_TOKEN_URL))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(PROXY_AUTH)));
    }

    public static void setResponse(String url, byte[] body) {
        dockerRegistryAuthMockServer
                .stubFor(get(urlPathEqualTo(url))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(body)));
    }

    public static void stop() {
        if (dockerRegistryAuthMockServer != null) {
            dockerRegistryAuthMockServer.stop();
        }
    }
}
