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

package com.nvidia.boot.telemetry;

import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.telemetry.client.CloudEventBuilderProvider;
import com.nvidia.boot.telemetry.client.TelemetryClient;
import com.nvidia.boot.telemetry.client.TelemetryProperties;
import com.nvidia.boot.telemetry.client.TelemetryTestFixtures;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.SpringBootConfiguration;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Import;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * Spring context integration test for {@link TelemetryAutoConfiguration}.
 */
@SpringBootTest(
        classes = TelemetryAutoConfigurationIntegrationTest.TestApplication.class,
        webEnvironment = SpringBootTest.WebEnvironment.NONE)
class TelemetryAutoConfigurationIntegrationTest {

    @Autowired
    private TelemetryClient telemetryClient;

    @Autowired
    private CloudEventBuilderProvider cloudEventBuilderProvider;

    @Test
    void autoConfiguresTelemetryBeansWhenPropertiesPresent() {
        assertThat(telemetryClient).isNotNull();
        assertThat(cloudEventBuilderProvider).isNotNull();
    }

    /**
     * {@link TelemetryAutoConfiguration} is {@code @ConditionalOnBean(TelemetryProperties.class)}.
     * Register {@link TelemetryProperties} in a separate imported config so it exists before the
     * auto-configuration is processed.
     */
    @SpringBootConfiguration
    @Import({TelemetryPropertiesConfig.class, TelemetryAutoConfiguration.class})
    static class TestApplication {

        /**
         * {@link TelemetryAutoConfiguration#telemetryClient} requires {@link WebClient.Builder}.
         * A full app picks this up from WebFlux auto-configuration; this test imports only
         * {@link TelemetryAutoConfiguration}, so we register the builder explicitly.
         */
        @Bean
        WebClient.Builder webClientBuilder() {
            return WebClient.builder();
        }

        @Bean
        JsonMapper jsonMapper() {
            return JsonMapper.builder().build();
        }
    }

    @Configuration
    static class TelemetryPropertiesConfig {

        @Bean
        TelemetryProperties telemetryProperties() {
            return TelemetryTestFixtures.telemetryProperties(
                    "http://127.0.0.1:9",
                    "/oauth/token");
        }
    }
}
