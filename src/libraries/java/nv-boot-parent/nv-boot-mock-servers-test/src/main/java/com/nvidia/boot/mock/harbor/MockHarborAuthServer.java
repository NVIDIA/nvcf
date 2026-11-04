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

package com.nvidia.boot.mock.harbor;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;

public class MockHarborAuthServer {

    // Harbor token endpoint with scope - GET /service/token?service=harbor-registry&scope=...
    // The service is always harbor-registry and hard coded in harbor repo.
    // Used for artifact validation
    private static final String HARBOR_TOKEN_WITH_SCOPE_URL_PATTERN =
            "/service/token\\?service=harbor-registry&scope=.*";
    
    // Harbor token endpoint without scope - GET /service/token?service=harbor-registry
    // Used for credential validation only
    private static final String HARBOR_TOKEN_WITHOUT_SCOPE_URL_PATTERN =
            "/service/token\\?service=harbor-registry$";
    
    private static final String OAUTH2_TOKEN_RESPONSE = """
            {
                "token":"eyJhbGciOiJSUzI1NiIsImtpZCI6IlZXVUg6WFJSNjpBRFFQOjYzVEg6VUgyNjpOQVREOlNIQ1Y6NzRKRDpaNzdXOkpJWUE6N0xQSjpHREVJIiwidHlwIjoiSldUIn0.eyJpc3MiOiJoYXJib3ItdG9rZW4taXNzdWVyIiwic3ViIjoiaHVhd2VpYyIsImF1ZCI6ImhhcmJvci1yZWdpc3RyeSIsImV4cCI6MTc1ODU0MTg4MCwibmJmIjoxNzU4NTQwMDgwLCJpYXQiOjE3NTg1NDAwODAsImp0aSI6InZ4NFY5dTlPMkpaaWQxQnEiLCJhY2Nlc3MiOlt7InR5cGUiOiJyZXBvc2l0b3J5IiwibmFtZSI6Imh1YXdlaWMtdGVzdC9idXN5Ym94IiwiYWN0aW9ucyI6WyJwdWxsIiwicHVzaCIsImRlbGV0ZSJdfV19.m3sb4tGhZCkLHJm6XGoSVpKkT-_FE9vV7Rxh7Lt_1ziygTi7DBlytXEEwCoIsJVpClc_yPivxVlNDpqxEsll9bU4bun-2ZkI7DwLpjXyrGBC1G6qZ7NJRu4FqtbIsQtP6sSQUesIDY4KA0kRKJjxb8T8v77676so0DpCwhZ2qMvLndOz7cbkGPADxQ2cxX8bkQz08ZSFbIOy8mGRaMkjx4jrW5w4biI5x6vSH_0COs2FvObdoT1olZ2QVcjvgPTapHlO9LHs0hpPC4CJ-npyb3ch21SGmYcI-D04e1p96ujImzHoP4kLiGGksVoIRMJwLop8J5rciRKg6VLqcFXj2g",
                "access_token":"",
                "expires_in":1800
            }
            """;

    private static WireMockServer acrAuthMockServer;

    @SneakyThrows
    public static void start(String acrAuthBaseUrl) {
        stop();
        acrAuthMockServer = new WireMockServer(URI.create(acrAuthBaseUrl).getPort());
        acrAuthMockServer.start();

        // Harbor token endpoint with scope - for artifact validation
        acrAuthMockServer
                .stubFor(get(urlMatching(HARBOR_TOKEN_WITH_SCOPE_URL_PATTERN))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(
                                                             HttpHeaders.CONTENT_TYPE,
                                                             MediaType.APPLICATION_JSON_VALUE)
                                                     .withBody(OAUTH2_TOKEN_RESPONSE)));
        
        // Harbor token endpoint without scope - for credential validation
        acrAuthMockServer
                .stubFor(get(urlMatching(HARBOR_TOKEN_WITHOUT_SCOPE_URL_PATTERN))
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
