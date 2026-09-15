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
import static com.github.tomakehurst.wiremock.client.WireMock.delete;
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.put;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathMatching;
import static com.nvidia.nvcf.util.EssResponseTransformer.NAME;
import static org.springframework.http.HttpHeaders.CONTENT_TYPE;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import com.github.tomakehurst.wiremock.matching.EqualToPattern;
import java.net.URI;
import java.time.Instant;
import java.util.UUID;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;

@UtilityClass
public class MockEssServer {

    @Getter
    private static WireMockServer mockEssServer;

    private static EssResponseTransformer essResponseTransformer;

    @SneakyThrows
    public static void start(String essBaseUrl) {
        stop();

        essResponseTransformer = new EssResponseTransformer();
        var configuration = new WireMockConfiguration()
                .port(URI.create(essBaseUrl).getPort())
                .extensions(essResponseTransformer);
        mockEssServer = new WireMockServer(configuration);

        var saveOrUpdateSecretsResponse = """
                                {
                                  "data": {
                                     "created_time": "%s",
                                     "version": "%s"
                                  }
                                }
                                """.formatted(Instant.now(), UUID.randomUUID());
        mockEssServer.stubFor(put(urlPathMatching("/v1/functions/(.+)?/versions/(.+)"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withBody(saveOrUpdateSecretsResponse)
                                                          .withTransformers(NAME)));
        mockEssServer.stubFor(get(urlPathMatching("/v1/functions/(.+)?/versions/(.+)"))
                                      .withQueryParam("query_type",
                                                      new EqualToPattern("fetch_secret"))
                                      .willReturn(aResponse().withStatus(200)
                                                          .withHeader(CONTENT_TYPE,
                                                                      APPLICATION_JSON_VALUE)
                                                          .withTransformers(NAME)));
        mockEssServer.stubFor(delete(urlPathMatching("/v1/functions/(.+)?/versions/(.+)"))
                                      .willReturn(aResponse().withStatus(204)
                                                          .withTransformers(NAME)));
        mockEssServer.stubFor(delete(urlPathMatching("/v1/functions/(.+)"))
                                      .willReturn(aResponse().withStatus(204)
                                                          .withTransformers(NAME)));
        // Stubs for ncaId-based paths (telemetry, registry credentials)
        mockEssServer.stubFor(put(urlPathMatching("/v1/accounts/(.+)"))
                .willReturn(aResponse().withStatus(200)
                        .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                        .withBody(saveOrUpdateSecretsResponse)
                        .withTransformers(NAME)));
        mockEssServer.stubFor(get(urlPathMatching("/v1/accounts/(.+)"))
                .withQueryParam("query_type",
                        new EqualToPattern("fetch_secret"))
                .willReturn(aResponse().withStatus(200)
                        .withHeader(CONTENT_TYPE, APPLICATION_JSON_VALUE)
                        .withTransformers(NAME)));
        mockEssServer.stubFor(delete(urlPathMatching("/v1/accounts/(.+)"))
                .willReturn(aResponse().withStatus(204)
                        .withTransformers(NAME)));
        mockEssServer.start();
    }

    public static void stop() {
        if (mockEssServer != null) {
            clearSecrets();
            mockEssServer.stop();
        }
    }

    public static void clearSecrets() {
        essResponseTransformer.clearSecrets();
    }

}
