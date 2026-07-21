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
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.options;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.header;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.nvidia.boot.core.cors.ReactiveCoreCorsConfiguration;
import com.nvidia.boot.core.health.HealthController;
import com.nvidia.boot.core.openapi.ReactiveOpenApiCorsConfiguration;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.ApplicationContext;
import org.springframework.test.web.servlet.MockMvc;

/**
 * Integration test for nv-boot-starter-core with a minimal web application.
 */
@SpringBootTest(
        classes = TestApplication.class,
        properties = {
                "spring.application.name=test-app",
                "spring.application.version=1.0.0",
                "spring.profiles.active=test",
                "spring.main.web-application-type=servlet"
        })
@AutoConfigureMockMvc
class NvBootCoreIntegrationTest {

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private HealthController healthController;

    @Autowired
    private ApplicationContext context;

    @Test
    void reactiveConfigurationsAreNotLoaded() {
        assertThat(context.getBeansOfType(ReactiveCoreCorsConfiguration.class)).isEmpty();
        assertThat(context.getBeansOfType(ReactiveOpenApiCorsConfiguration.class)).isEmpty();
    }

    @Test
    void healthEndpointReturnsStatusOnly() throws Exception {
        mockMvc.perform(get("/health"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("UP"));
    }

    @Test
    void corsHeadersPresentOnHealthEndpoint() throws Exception {
        var origin = "https://example.com";
        mockMvc.perform(get("/health").header("Origin", origin))
                .andExpect(status().isOk())
                .andExpect(header().string("Access-Control-Allow-Origin", origin))
                .andExpect(header().string("Access-Control-Allow-Credentials", "true"));
    }

    @Test
    void corsPreflightSucceeds() throws Exception {
        var origin = "https://example.com";
        mockMvc.perform(options("/health")
                        .header("Origin", origin)
                        .header("Access-Control-Request-Method", "GET"))
                .andExpect(status().isOk())
                .andExpect(header().string("Access-Control-Allow-Origin", origin))
                .andExpect(header().string("Access-Control-Allow-Methods", "GET"))
                .andExpect(header().string("Access-Control-Allow-Credentials", "true"))
                .andExpect(header().string("Access-Control-Max-Age", "86400"));
    }
}
