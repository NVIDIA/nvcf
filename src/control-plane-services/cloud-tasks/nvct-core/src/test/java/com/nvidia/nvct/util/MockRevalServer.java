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
import static com.github.tomakehurst.wiremock.client.WireMock.absent;
import static com.github.tomakehurst.wiremock.client.WireMock.containing;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.matchingJsonPath;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.core.WireMockConfiguration.options;

import com.github.tomakehurst.wiremock.WireMockServer;
import java.net.URI;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class MockRevalServer {
    private static WireMockServer wireMockServer;

    public static void start(URI listenUrl) {
        stop();

        wireMockServer = new WireMockServer(options().port(listenUrl.getPort()));

        var validationOk = aResponse()
                .withStatus(200)
                .withHeader("Content-Type", "application/json")
                .withBody("{\"valid\": true}");

        var validationError = aResponse()
                .withStatus(200)
                .withHeader("Content-Type", "application/json")
                .withBody("""
                {"valid": false, "validationErrors": ["spam", "eggs"]}
                """);

        wireMockServer.stubFor(
                post("/v1/validate")
                        .withRequestBody(
                                matchingJsonPath(
                                        "$.helmChart",
                                        containing("invalid-helm-chart")))
                        .willReturn(validationError));
        wireMockServer.stubFor(
                post("/v1/validate")
                        .atPriority(4)
                        .withRequestBody(
                                matchingJsonPath("$.configuration.fail", equalTo("fail")))
                        .willReturn(validationError));
        wireMockServer.stubFor(
                post("/v1/validate")
                        .withRequestBody(
                                matchingJsonPath(
                                        "$.helmChart",
                                        containing("test-helm-chart")))
                        .willReturn(validationOk));

        wireMockServer.stubFor(
                post("/v1/validate")
                        .withHeader("Authorization", absent())
                        .willReturn(
                                aResponse()
                                        .withStatus(401)
                                        .withBody("Authorization header required")));

        wireMockServer.start();

    }

    public static void stop() {
        if (wireMockServer != null && wireMockServer.isRunning()) {
            wireMockServer.stop();
        }
    }
}
