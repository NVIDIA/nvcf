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
package com.nvidia.nvcf.configuration.nats;

import static com.nvidia.nvcf.service.nats.AuthCalloutService.API_USER_ACCOUNT;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import java.io.IOException;
import org.junit.jupiter.api.Test;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.boot.env.YamlPropertySourceLoader;
import org.springframework.core.env.StandardEnvironment;
import org.springframework.core.io.ClassPathResource;

/**
 * Binds the shipped profiles, so the account the worker webhook returns is the one that ships.
 */
class NatsWorkerAccountConfigurationTest {

    @Test
    void ncpProfileReturnsTheAppAccountForWebhookWorkers() throws IOException {
        var properties = natsProperties("application-ncp.yaml", "application.yaml");

        assertThat(properties.getAuthCalloutWorkerAccount()).isEqualTo("APP");
    }

    @Test
    void baseConfigurationReturnsTheAccountWorkersRequest() throws IOException {
        var properties = natsProperties("application.yaml");

        assertThat(properties.getAuthCalloutWorkerAccount()).isEqualTo(API_USER_ACCOUNT);
    }

    private static NatsProperties natsProperties(String... resources) throws IOException {
        var environment = new StandardEnvironment();
        var sources = environment.getPropertySources();
        // The shipped YAML is the subject here, so drop the JVM and OS sources
        // StandardEnvironment ranks ahead of it.
        sources.remove(StandardEnvironment.SYSTEM_PROPERTIES_PROPERTY_SOURCE_NAME);
        sources.remove(StandardEnvironment.SYSTEM_ENVIRONMENT_PROPERTY_SOURCE_NAME);
        var loader = new YamlPropertySourceLoader();
        // Profile file first: the first source added wins, matching profile precedence.
        for (var resource : resources) {
            for (var source : loader.load(resource, new ClassPathResource(resource))) {
                sources.addLast(source);
            }
        }
        return Binder.get(environment).bindOrCreate("nvcf.nats", NatsProperties.class);
    }
}
