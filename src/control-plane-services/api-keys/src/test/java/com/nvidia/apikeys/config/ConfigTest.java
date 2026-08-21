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

package com.nvidia.apikeys.config;

import static org.assertj.core.api.AssertionsForClassTypes.assertThat;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.web.BaseIntegrationTest;
import com.nvidia.boot.jwt.services.JwtService;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.test.context.ContextConfiguration;

@Slf4j
@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ConfigTest extends BaseIntegrationTest {

    @Autowired
    private ConfigurableEnvironment environment;

    @Autowired
    private JwtService jwtService;

    @Test
    void vaultPropertiesShouldBeSet() {
        assertThat(environment.getProperty("kv.jwe-key-mapping.unused_jwe_kid"))
                .isEqualTo("fb902679-8841-4c03-ad62-2597084a6d58");
        assertThat(environment.getProperty("kv.jwe-key-mapping.payload_jwe_kid"))
                .isEqualTo("92f5fb39-0b6b-4472-9b88-0575870294c0");

        assertThat(environment.getProperty("kv.private-key-jwks.keys")).isNotEmpty();
    }

    @Test
    void serviceIdMapShouldContainRegisteredServices() {
        assertThat(environment.getProperty("apikeys.service-id-map.nvcf"))
                .isEqualTo("nvidia-cloud-functions-ncp-service-id-aketm");
        assertThat(environment.getProperty("apikeys.service-id-map.nvct"))
                .isEqualTo("nvidia-cloud-tasks-ncp-service-id-nvcttasks");
        assertThat(environment.getProperty("apikeys.service-id-map.event-ledger"))
                .isEqualTo("nvidia-cloud-events-ncp-service-id-ckozoh6f");
    }

    @Test
    void shouldBeAbleToEncryptAndDecrypt() {
        String encryptedText = jwtService.encryptWithKeyId("92f5fb39-0b6b-4472-9b88-0575870294c0",
                                                           "text");
        assertThat(encryptedText).isNotEmpty();
        assertThat(encryptedText).isNotEqualTo("text");

        assertThat(jwtService.decrypt(encryptedText)).isEqualTo("text");
    }

}
