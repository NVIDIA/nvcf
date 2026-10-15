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
package com.nvidia.nvct.service.apikeys;

import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvct.util.MockApiKeysServer.EVALUATION_URI_PATH;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientApiKeysProperties;
import com.nvidia.nvct.util.MockApiKeysServer;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.web.reactive.function.client.WebClient;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ApiKeysClientTest {

    @Value("${nvct.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @BeforeEach
    void setUp() {
        MockApiKeysServer.start(apiKeysBaseUrl);
    }

    @AfterEach
    void reset() {
        MockApiKeysServer.resetToDefault();
    }

    @AfterAll
    void tearDown() {
        MockApiKeysServer.stop();
    }

    @Test
    void fetchApiKeyValidationResultPostsToConfiguredEvaluationUri() {
        var baseUrl = MockApiKeysServer.getMockApiKeysServer().baseUrl();
        var staticProps = new StaticClientApiKeysProperties();
        staticProps.setToken("static-token");
        var client = new ApiKeysClient(
                baseUrl,
                EVALUATION_URI_PATH,
                "apiKey",
                "unused-client",
                "unused-secret",
                "",
                "http://localhost:1/unused-token",
                Optional.of(staticProps),
                WebClient.builder(),
                JsonMapper.builder().build());

        client.fetchApiKeyValidationResult("my-api-key");

        MockApiKeysServer.getMockApiKeysServer()
                .verify(1, postRequestedFor(urlPathEqualTo(EVALUATION_URI_PATH)));
    }
}
