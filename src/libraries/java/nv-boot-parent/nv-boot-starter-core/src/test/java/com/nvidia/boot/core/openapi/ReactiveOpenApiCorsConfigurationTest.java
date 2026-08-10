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

package com.nvidia.boot.core.openapi;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.webtestclient.autoconfigure.AutoConfigureWebTestClient;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.ApplicationContext;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@SpringBootTest(properties = {
        "spring.profiles.active=test",
        "spring.main.web-application-type=reactive"
})
@AutoConfigureWebTestClient
class ReactiveOpenApiCorsConfigurationTest {

    @Autowired
    private WebTestClient webTestClient;

    @Autowired
    private ApplicationContext context;

    @Test
    void servletOpenApiCorsIsNotLoaded() {
        assertThat(context.getBeansOfType(ServletOpenApiCorsConfiguration.class)).isEmpty();
    }

    @Test
    void corsPresentOnOpenApi() {
        webTestClient.get().uri("/v3/openapi")
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals("Access-Control-Allow-Origin", "*")
                .expectHeader().valueEquals("Access-Control-Allow-Methods", "*")
                .expectHeader().valueEquals("Access-Control-Allow-Headers", "*")
                .expectHeader().valueEquals("Access-Control-Allow-Credentials", "true")
                .expectHeader().valueEquals("Access-Control-Max-Age", "3600");
    }

    @Test
    void corsMissingOnRegularEndpoint() {
        webTestClient.get().uri("/")
                .exchange()
                .expectStatus().isOk()
                .expectHeader().doesNotExist("Access-Control-Allow-Origin")
                .expectHeader().doesNotExist("Access-Control-Allow-Methods")
                .expectHeader().doesNotExist("Access-Control-Allow-Headers")
                .expectHeader().doesNotExist("Access-Control-Allow-Credentials")
                .expectHeader().doesNotExist("Access-Control-Max-Age");
    }

    @SpringBootApplication
    static class TestApplication {

        @RestController
        static class TestController {

            @GetMapping("/")
            public String get() {
                return "";
            }

            @GetMapping("/v3/openapi")
            public String openapi() {
                return "{}";
            }
        }
    }
}
