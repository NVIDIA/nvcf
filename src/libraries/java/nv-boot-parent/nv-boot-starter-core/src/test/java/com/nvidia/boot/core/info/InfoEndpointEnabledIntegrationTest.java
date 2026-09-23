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

package com.nvidia.boot.core.info;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import com.nvidia.boot.core.TestApplication;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.context.ApplicationContext;
import org.springframework.test.web.servlet.MockMvc;

/**
 * Same setup as {@link InfoEndpointAbsentPropertyIntegrationTest}, but with
 * {@code nv-boot.info.enabled=true}. Confirms exactly one {@link InfoController} bean is
 * registered (guarding against the earlier double-registration bug where classpath component
 * scanning and InfoConfiguration's own @Bean method could both try to create it) and that
 * GET /info serves the real response.
 */
@SpringBootTest(
        classes = TestApplication.class,
        properties = {
                "spring.application.name=test-app",
                "spring.application.version=1.0.0",
                "spring.profiles.active=test",
                "spring.main.web-application-type=servlet",
                "nv-boot.info.enabled=true"
        })
@AutoConfigureMockMvc
class InfoEndpointEnabledIntegrationTest {

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ApplicationContext context;

    @Test
    void infoControllerBeanIsPresentExactlyOnce() {
        assertThat(context.getBeansOfType(InfoController.class)).hasSize(1);
    }

    @Test
    void infoEndpointReturnsServiceVersionAndCommit() throws Exception {
        mockMvc.perform(get("/info"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.service").value("test-app"))
                .andExpect(jsonPath("$.version").value("1.0.0"))
                .andExpect(jsonPath("$.commit").exists());
    }
}
