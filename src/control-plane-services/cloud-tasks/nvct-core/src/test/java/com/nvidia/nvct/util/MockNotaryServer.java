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
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvct.util.NotaryServiceResponseTransformer.NAME;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import tools.jackson.databind.json.JsonMapper;
import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import java.net.URI;
import java.time.Instant;
import java.util.UUID;
import lombok.SneakyThrows;
import org.springframework.http.HttpHeaders;

public class MockNotaryServer {

    private static final JsonMapper OBJECT_MAPPER = JsonMapper.builder().build();

    private static NotaryServiceResponseTransformer notaryServiceResponseTransformer;

    private static WireMockServer mockNotaryServer;

    @SneakyThrows
    public static void start(String notaryBaseUrl, String notaryClientId) {
        stop();
        notaryServiceResponseTransformer = new NotaryServiceResponseTransformer(notaryBaseUrl,
                                                                                notaryClientId);
        var configuration = new WireMockConfiguration()
                .port(URI.create(notaryBaseUrl).getPort())
                .extensions(notaryServiceResponseTransformer);

        mockNotaryServer = new WireMockServer(configuration);
        mockNotaryServer
                .stubFor(post(urlPathEqualTo("/sign"))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 APPLICATION_JSON_VALUE)
                                                     .withTransformers(NAME)));
        mockNotaryServer
                .stubFor(get(urlPathEqualTo("/.well-known/jwks.json"))
                                 .willReturn(aResponse().withStatus(200)
                                                     .withHeader(HttpHeaders.CONTENT_TYPE,
                                                                 APPLICATION_JSON_VALUE)
                                                     .withBody(notaryServiceResponseTransformer.getJwksJson())));
        mockNotaryServer.start();
    }

    public static void stop() {
        if (mockNotaryServer != null) {
            mockNotaryServer.stop();
        }
    }

    @SneakyThrows
    public static String generateSignedWorkerAssertion(String issuer,
                                                       String subject,
                                                       String ncaId,
                                                       UUID taskId,
                                                       Instant issuedAt) {
        return NotaryServiceResponseTransformer.generateSignedWorkerAssertion(
                issuer, subject, ncaId, taskId, issuedAt, OBJECT_MAPPER);
    }
}
