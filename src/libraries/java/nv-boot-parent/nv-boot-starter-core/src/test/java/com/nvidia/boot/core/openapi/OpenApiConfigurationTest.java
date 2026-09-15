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

import io.swagger.v3.oas.models.OpenAPI;
import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.core.env.StandardEnvironment;

class OpenApiConfigurationTest {

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withPropertyValues(
                    "spring.application.name=my-api",
                    "spring.application.version=2.1.0",
                    "springdoc.info.title=Custom API",
                    "springdoc.info.description=Custom description",
                    "springdoc.info.version=3.0.0")
            .withUserConfiguration(OpenApiConfiguration.class);

    @Test
    void createsOpenApiBeanWithSpringDocInfo() {
        contextRunner.run(context -> {
            assertThat(context).hasSingleBean(OpenAPI.class);
            var openApi = context.getBean(OpenAPI.class);
            var info = openApi.getInfo();

            assertThat(info.getTitle()).isEqualTo("Custom API");
            assertThat(info.getDescription()).isEqualTo("Custom description");
            assertThat(info.getVersion()).isEqualTo("3.0.0");
            assertThat(info.getContact().getName()).isEqualTo("NVIDIA");
            assertThat(info.getContact().getUrl()).isEqualTo("https://www.nvidia.com/");
            assertThat(info.getTermsOfService()).isEqualTo("https://www.nvidia.com/en-us/legal_info");
        });
    }

    @Test
    void usesDefaultsWhenSpringDocInfoBlank() {
        var runner = new ApplicationContextRunner()
                .withPropertyValues(
                        "spring.application.name=my-service",
                        "spring.application.version=1.0.0")
                .withUserConfiguration(OpenApiConfiguration.class);

        runner.run(context -> {
            var openApi = context.getBean(OpenAPI.class);
            var info = openApi.getInfo();

            assertThat(info.getTitle()).isEqualTo("My Service");
            assertThat(info.getVersion()).isEqualTo("1.0.0");
            assertThat(info.getContact().getName()).isEqualTo("NVIDIA");
            assertThat(info.getContact().getUrl()).isEqualTo("https://www.nvidia.com/");
        });
    }

    @Test
    void usesCustomContactWhenConfigured() {
        var runner = new ApplicationContextRunner()
                .withPropertyValues(
                        "spring.application.name=test",
                        "spring.application.version=1.0.0",
                        "springdoc.info.contact.name=My Team",
                        "springdoc.info.contact.email=team@example.com",
                        "springdoc.info.contact.url=https://example.com")
                .withUserConfiguration(OpenApiConfiguration.class);

        runner.run(context -> {
            var openApi = context.getBean(OpenAPI.class);
            var contact = openApi.getInfo().getContact();

            assertThat(contact.getName()).isEqualTo("My Team");
            assertThat(contact.getEmail()).isEqualTo("team@example.com");
            assertThat(contact.getUrl()).isEqualTo("https://example.com");
        });
    }

    @Test
    void fallsBackToSpringApplicationVersionWhenInfoVersionNotSet() {
        var runner = new ApplicationContextRunner()
                .withPropertyValues(
                        "spring.application.name=my-service",
                        "spring.application.version=5.2.0")
                .withInitializer(context -> context.getEnvironment().getPropertySources()
                        .remove(StandardEnvironment.SYSTEM_PROPERTIES_PROPERTY_SOURCE_NAME))
                .withUserConfiguration(OpenApiConfiguration.class);

        runner.run(context -> {
            var openApi = context.getBean(OpenAPI.class);
            assertThat(openApi.getInfo().getVersion()).isEqualTo("5.2.0");
        });
    }

    @Test
    void usesHardcodedDefaultWhenNoVersionConfigured() {
        var runner = new ApplicationContextRunner()
                .withPropertyValues("spring.application.name=my-service")
                .withInitializer(context -> context.getEnvironment().getPropertySources()
                        .remove(StandardEnvironment.SYSTEM_PROPERTIES_PROPERTY_SOURCE_NAME))
                .withUserConfiguration(OpenApiConfiguration.class);

        runner.run(context -> {
            var openApi = context.getBean(OpenAPI.class);
            assertThat(openApi.getInfo().getVersion()).isEqualTo("1.0.0");
        });
    }

    @Test
    void explicitInfoVersionTakesPriorityOverApplicationVersion() {
        var runner = new ApplicationContextRunner()
                .withPropertyValues(
                        "spring.application.name=my-service",
                        "spring.application.version=5.2.0",
                        "springdoc.info.version=9.0.0")
                .withUserConfiguration(OpenApiConfiguration.class);

        runner.run(context -> {
            var openApi = context.getBean(OpenAPI.class);
            assertThat(openApi.getInfo().getVersion()).isEqualTo("9.0.0");
        });
    }
}
