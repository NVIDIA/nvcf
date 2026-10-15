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

package com.nvidia.boot.mock.artifactory;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;

public class MockArtifactoryAuthServer {

    // Token endpoint with scope - /v2/token?service=...&scope=...
    // Used for artifact validation
    private static final String ARTIFACTORY_TOKEN_WITH_SCOPE_URL_PATTERN =
            "/v2/token\\?service=[^&]+&scope=.*";
    
    // Token endpoint without scope - /v2/token?service=...
    // Used for credential validation only
    private static final String ARTIFACTORY_TOKEN_WITHOUT_SCOPE_URL_PATTERN =
            "/v2/token\\?service=[^&]+$";
    
    private static final String ARTIFACTORY_TOKEN_RESPONSE = """
            {
                "token": "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiIsImtpZCI6InRlc3Qta2V5LWlkIn0.eyJhdWQiOiJ0ZXN0LWpmcm9nLmlvIiwiaXNzIjoiamZyb2ctYXJ0aWZhY3RvcnkiLCJpYXQiOjE2MDAwMDAwMDAsImV4cCI6MTYwMDAwOTAwMCwic2NvcGUiOiJyZXBvc2l0b3J5OnRlc3QtcmVwbzpwdWxsIiwic3ViIjoidGVzdC11c2VyIn0.mockSignature"
            }
            """;

    private static WireMockServer artifactoryAuthMockServer;

    @SneakyThrows
    public static void start(String artifactoryAuthBaseUrl) {
        stop();
        artifactoryAuthMockServer = new WireMockServer(URI.create(artifactoryAuthBaseUrl).getPort());
        artifactoryAuthMockServer.start();

        // Artifactory token endpoint with scope - for artifact validation
        artifactoryAuthMockServer
                .stubFor(get(urlMatching(ARTIFACTORY_TOKEN_WITH_SCOPE_URL_PATTERN))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(ARTIFACTORY_TOKEN_RESPONSE)));
        
        // Artifactory token endpoint without scope - for credential validation
        artifactoryAuthMockServer
                .stubFor(get(urlMatching(ARTIFACTORY_TOKEN_WITHOUT_SCOPE_URL_PATTERN))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(ARTIFACTORY_TOKEN_RESPONSE)));
    }

    public static void setResponse(String url, byte[] body) {
        artifactoryAuthMockServer
                .stubFor(get(urlPathEqualTo(url))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(body)));
    }

    public static void stop() {
        if (artifactoryAuthMockServer != null) {
            artifactoryAuthMockServer.stop();
        }
    }
}
