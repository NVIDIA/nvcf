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
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_4;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_5;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID_6;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_WITHOUT_REGISTRY_CREDENTIALS_6;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_WITH_1_MAX_ALLOWED_TASKS_5;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_WITH_TELEMETRIES_4;
import static com.nvidia.nvct.util.TestUtil.readFileAsString;
import static org.springframework.http.HttpHeaders.CONTENT_TYPE;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import com.github.tomakehurst.wiremock.extension.ResponseTransformerV2;
import com.github.tomakehurst.wiremock.http.Response;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import java.net.URL;
import java.time.Instant;
import java.util.Map;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class MockNvcfServer {

    @Getter
    private static WireMockServer mockNvcfServer;

    private static final String ACCOUNT_RESPONSE =
            readFileAsString("fixtures/nvcf/account-response.json");
    private static final String ACCOUNT_WITH_TELEMETRIES_RESPONSE =
            readFileAsString("fixtures/nvcf/account-with-telemetries-response.json");
    private static final String ACCOUNT_WITHOUT_REGISTRY_CREDENTIALS_RESPONSE =
            readFileAsString("fixtures/nvcf/account-without-registry-credentials-response.json");
    private static final String CLIENT_RESPONSE =
            readFileAsString("fixtures/nvcf/client-response.json");

    @SneakyThrows
    public static void start(URL url) {
        stop();
        var accountExtension = new AccountResponseTransformer();
        var clientResponseExtension = new ClientResponseTransformer();
        var config = WireMockConfiguration.options()
                .port(url.getPort())
                .extensions(accountExtension, clientResponseExtension);
        mockNvcfServer = new WireMockServer(config);
        mockNvcfServer.stubFor(get(urlMatching("/v2/nvcf/accounts/(.*)"))
                                       .willReturn(aResponse().withStatus(200)
                                                           .withTransformers(accountExtension.getName())
                                                           .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                                           .withBody("{}")));
        mockNvcfServer.stubFor(get(urlMatching("/v2/nvcf/clients/(.*)"))
                                       .willReturn(aResponse().withStatus(200)
                                                           .withTransformers(clientResponseExtension.getName())
                                                           .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                                                           .withBody("{}")));
        mockNvcfServer.start();
    }

    public static void stop() {
        if (mockNvcfServer != null) {
            mockNvcfServer.stop();
        }
    }

    public static class AccountResponseTransformer implements ResponseTransformerV2 {
        private static final Map<String, String> ncaIdToClientIdMap =
                Map.of(TEST_NCA_ID, TEST_CLIENT_ID,
                       TEST_NCA_ID_2, TEST_CLIENT_ID_2,
                       TEST_NCA_ID_3, TEST_CLIENT_ID_3,
                       TEST_NCA_ID_WITH_TELEMETRIES_4, TEST_CLIENT_ID_4,
                       TEST_NCA_ID_WITH_1_MAX_ALLOWED_TASKS_5, TEST_CLIENT_ID_5,
                       TEST_NCA_ID_WITHOUT_REGISTRY_CREDENTIALS_6, TEST_CLIENT_ID_6);

        @Override
        public Response transform(Response response, ServeEvent serveEvent) {
            var request = serveEvent.getRequest();
            var index = request.getUrl().lastIndexOf("/");
            var ncaId = request.getUrl().substring(index + 1);
            var clientId = ncaIdToClientIdMap.get(ncaId);
            if (clientId == null) {
                return Response.Builder.like(response).status(404).body("").build();
            }
            String rawBody;
            if (ncaId.contains("without-registry-credentials")) {
                rawBody = ACCOUNT_WITHOUT_REGISTRY_CREDENTIALS_RESPONSE;
            } else if (ncaId.contains("telemetries")) {
                rawBody = ACCOUNT_WITH_TELEMETRIES_RESPONSE;
            } else {
                rawBody = ACCOUNT_RESPONSE;
            }
            var maxTasksAllowed = ncaId.contains("1-max-allowed-tasks") ? 1 : 50;
            var body = rawBody.formatted(ncaId, clientId, ncaId + "-name", maxTasksAllowed)
                              .replace("_instant_", Instant.now().toString());
            return Response.Builder.like(response).body(body).build();
        }

        @Override
        public boolean applyGlobally() {
            return false;
        }

        @Override
        public String getName() {
            return "account-response-transformer";
        }
    }

    public static class ClientResponseTransformer implements ResponseTransformerV2 {
        private static final Map<String, String> clientIdToNcaIdMap =
                Map.of(TEST_CLIENT_ID, TEST_NCA_ID,
                       TEST_CLIENT_ID_2, TEST_NCA_ID_2,
                       TEST_CLIENT_ID_3, TEST_NCA_ID_3,
                       TEST_CLIENT_ID_4, TEST_NCA_ID_WITH_TELEMETRIES_4,
                       TEST_CLIENT_ID_5, TEST_NCA_ID_WITH_1_MAX_ALLOWED_TASKS_5,
                       TEST_CLIENT_ID_6, TEST_NCA_ID_WITHOUT_REGISTRY_CREDENTIALS_6);

        @Override
        public Response transform(Response response, ServeEvent serveEvent) {
            var request = serveEvent.getRequest();
            var index = request.getUrl().lastIndexOf("/");
            var clientId = request.getUrl().substring(index + 1);
            var ncaId = clientIdToNcaIdMap.get(clientId);
            if (ncaId == null) {
                return Response.Builder.like(response).status(404).body("").build();
            }
            var body = CLIENT_RESPONSE.formatted(clientId, ncaId, ncaId + "-name");
            return Response.Builder.like(response).body(body).build();
        }

        @Override
        public boolean applyGlobally() {
            return false;
        }

        @Override
        public String getName() {
            return "client-response-transformer";
        }
    }
}
