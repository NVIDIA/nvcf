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
package com.nvidia.icms.outbound.ngc;

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.github.tomakehurst.wiremock.core.WireMockConfiguration.wireMockConfig;
import static org.junit.jupiter.api.Assertions.assertEquals;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.icms.util.OAuth2ClientUtils;
import com.nvidia.icms.util.OAuth2ClientUtils.ManagedHttpResources;
import java.util.UUID;
import java.util.concurrent.Executors;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * Integration test: {@link NgcClient} must obtain a client-credentials token and attach it as a
 * bearer header even when the call originates off the request thread.
 *
 * <p>Regression guard for spring-projects/spring-security#19405. Spring Security 7.1.0 made
 * {@code ServletOAuth2AuthorizedClientExchangeFilterFunction} skip the token exchange when no
 * {@code HttpServletRequest} is resolvable and send the request with no {@code Authorization}
 * header, which made every {@code InstanceServiceEventListener.sendNcaIdAccountNameEventAsync} call
 * fail with HTTP 401 from UIS.
 */
class NgcClientIntegrationTest {

    private static final String CLIENT_ID = "ngc-client";
    private static final String CLIENT_SECRET = "ngc-secret";
    private static final String SCOPE = "getUisOrgInfo";
    private static final String ACCESS_TOKEN = "test-ngc-access-token";

    private WireMockServer ngcServer;
    private ManagedHttpResources httpResources;

    @BeforeEach
    void startServer() {
        ngcServer = new WireMockServer(wireMockConfig().dynamicPort());
        ngcServer.start();
        httpResources = OAuth2ClientUtils.getClientHttpConnectorManaged("ngc-it");
    }

    @AfterEach
    void stopServer() {
        if (httpResources != null) {
            httpResources.close();
        }
        if (ngcServer != null) {
            ngcServer.stop();
        }
    }

    @Test
    void getOrgInfo_attachesBearerTokenWhenCalledOffTheRequestThread() throws Exception {
        var ncaId = UUID.randomUUID().toString();
        ngcServer.stubFor(post(urlPathEqualTo("/oauth/token"))
                .willReturn(aResponse()
                        .withStatus(200)
                        .withHeader("Content-Type", "application/json")
                        .withBody("{"
                                + "\"access_token\":\"" + ACCESS_TOKEN + "\","
                                + "\"token_type\":\"Bearer\","
                                + "\"expires_in\":3600"
                                + "}")));
        ngcServer.stubFor(get(urlPathEqualTo("/v2/accounts/" + ncaId + "/uisorginfo"))
                .willReturn(aResponse()
                        .withStatus(200)
                        .withHeader("Content-Type", "application/json")
                        .withBody("{\"organization\":{"
                                + "\"id\":1,"
                                + "\"name\":\"acme\","
                                + "\"displayName\":\"Acme\","
                                + "\"idpId\":\"idp-1\""
                                + "}}")));

        var client = new NgcClient(
                WebClient.builder(),
                httpResources,
                ngcServer.baseUrl(),
                CLIENT_ID,
                CLIENT_SECRET,
                SCOPE,
                ngcServer.baseUrl() + "/oauth/token");

        // Mirrors the @Async("taskExecutor") listener: a plain pool thread with no servlet request
        // and no security context.
        try (var executor = Executors.newSingleThreadExecutor()) {
            var response = executor.submit(() -> client.getOrgInfo(ncaId)).get();
            assertEquals("Acme", response.getOrganization().getDisplayName());
        }

        ngcServer.verify(getRequestedFor(urlPathEqualTo("/v2/accounts/" + ncaId + "/uisorginfo"))
                .withHeader("Authorization", equalTo("Bearer " + ACCESS_TOKEN)));
    }
}
