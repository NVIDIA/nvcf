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
import static com.github.tomakehurst.wiremock.client.WireMock.get;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.core.WireMockConfiguration;
import java.net.URL;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class MockNvcfServer {

    @Getter
    private static WireMockServer mockNvcfServer;

    private static final String HEALTH_OK_RESPONSE =
            """
            {
                "status": "UP"
            }
            """;
    @SneakyThrows
    public static void start(URL url) {
        stop();
        var config = WireMockConfiguration.options().port(url.getPort());
        mockNvcfServer = new WireMockServer(config);
        mockNvcfServer.stubFor(get(urlMatching("/health"))
                                       .willReturn(aResponse().withStatus(200)
                                                           .withHeader("Content-Type",
                                                                       APPLICATION_JSON_VALUE)
                                                           .withBody(HEALTH_OK_RESPONSE)));
        mockNvcfServer.start();
    }

    public static void stop() {
        if (mockNvcfServer != null) {
            mockNvcfServer.stop();
        }
    }
}
