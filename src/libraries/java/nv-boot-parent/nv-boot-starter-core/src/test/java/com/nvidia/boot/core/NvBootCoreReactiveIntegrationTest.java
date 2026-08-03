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

package com.nvidia.boot.core;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.core.cors.ServletCoreCorsConfiguration;
import com.nvidia.boot.core.openapi.ServletOpenApiCorsConfiguration;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webtestclient.autoconfigure.AutoConfigureWebTestClient;
import org.springframework.context.ApplicationContext;
import org.springframework.test.web.reactive.server.WebTestClient;

@SpringBootTest(
        classes = TestApplication.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.application.name=test-app",
                "spring.application.version=1.0.0",
                "spring.profiles.active=test",
                "spring.main.web-application-type=reactive"
        })
@AutoConfigureWebTestClient
class NvBootCoreReactiveIntegrationTest {

    @Autowired
    private WebTestClient webTestClient;

    @Autowired
    private ApplicationContext context;

    @Test
    void servletConfigurationsAreNotLoaded() {
        assertThat(context.getBeansOfType(ServletCoreCorsConfiguration.class)).isEmpty();
        assertThat(context.getBeansOfType(ServletOpenApiCorsConfiguration.class)).isEmpty();
    }

    @Test
    void healthEndpointReturnsStatusOnly() {
        webTestClient.get().uri("/health")
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .jsonPath("$.status").isEqualTo("UP");
    }

    @Test
    void corsHeadersPresentOnHealthEndpoint() {
        var origin = "https://example.com";
        webTestClient.get().uri("/health")
                .header("Origin", origin)
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals("Access-Control-Allow-Origin", origin)
                .expectHeader().valueEquals("Access-Control-Allow-Credentials", "true");
    }

    @Test
    void corsPreflightSucceeds() {
        var origin = "https://example.com";
        webTestClient.options().uri("/health")
                .header("Origin", origin)
                .header("Access-Control-Request-Method", "GET")
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals("Access-Control-Allow-Origin", origin)
                .expectHeader().valueEquals("Access-Control-Allow-Credentials", "true")
                .expectHeader().valueEquals("Access-Control-Max-Age", "86400");
    }
}
