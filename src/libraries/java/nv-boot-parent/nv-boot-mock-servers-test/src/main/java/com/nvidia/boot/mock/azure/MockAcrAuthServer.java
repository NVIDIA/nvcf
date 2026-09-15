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

package com.nvidia.boot.mock.azure;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;

public class MockAcrAuthServer {

    // OAuth2 token endpoint with scope - GET /oauth2/token?service=...&scope=...
    // Used for artifact validation
    private static final String OAUTH2_TOKEN_WITH_SCOPE_URL_PATTERN = 
            "/oauth2/token\\?service=[^&]+&scope=.*";
    
    // OAuth2 token endpoint without scope - GET /oauth2/token?service=...
    // Used for credential validation only
    private static final String OAUTH2_TOKEN_WITHOUT_SCOPE_URL_PATTERN = 
            "/oauth2/token\\?service=[^&]+$";
    
    private static final String OAUTH2_TOKEN_RESPONSE = """
            {
                "access_token": "eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiIsImtpZCI6IkJCOENlRlZqTWNObEJEajhMYktWME1oV2dEQSJ9.eyJhdWQiOiJ0ZXN0YWNycmVnLmF6dXJlY3IuaW8iLCJpc3MiOiJodHRwczovL2xvZ2luLm1pY3Jvc29mdG9ubGluZS5jb20vdGVzdC10ZW5hbnQtaWQvdjIuMCIsImlhdCI6MTYwMDAwMDAwMCwiZXhwIjoxNjAwMDAzNjAwLCJzY29wZSI6InJlcG9zaXRvcnk6dGVzdC1hY3ItcmVwbzpwdWxsIiwic3ViIjoidGVzdC11c2VyIn0.mockSignature"
            }
            """;

    private static WireMockServer acrAuthMockServer;

    @SneakyThrows
    public static void start(String acrAuthBaseUrl) {
        stop();
        acrAuthMockServer = new WireMockServer(URI.create(acrAuthBaseUrl).getPort());
        acrAuthMockServer.start();

        // OAuth2 token endpoint with scope - for artifact validation
        acrAuthMockServer
                .stubFor(get(urlMatching(OAUTH2_TOKEN_WITH_SCOPE_URL_PATTERN))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(OAUTH2_TOKEN_RESPONSE)));
        
        // OAuth2 token endpoint without scope - for credential validation
        acrAuthMockServer
                .stubFor(get(urlMatching(OAUTH2_TOKEN_WITHOUT_SCOPE_URL_PATTERN))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(OAUTH2_TOKEN_RESPONSE)));
    }

    public static void setResponse(String url, byte[] body) {
        acrAuthMockServer
                .stubFor(get(urlPathEqualTo(url))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(body)));
    }

    public static void stop() {
        if (acrAuthMockServer != null) {
            acrAuthMockServer.stop();
        }
    }
}
