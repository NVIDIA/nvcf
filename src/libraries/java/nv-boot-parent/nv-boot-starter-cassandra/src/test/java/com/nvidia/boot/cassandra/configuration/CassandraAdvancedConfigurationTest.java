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

package com.nvidia.boot.cassandra.configuration;

import static org.assertj.core.api.Assertions.assertThat;

import com.datastax.oss.driver.api.core.config.DriverConfigLoader;
import org.junit.jupiter.api.Test;
import org.springframework.mock.env.MockEnvironment;

class CassandraAdvancedConfigurationTest {

    private final CassandraAdvancedConfiguration config = new CassandraAdvancedConfiguration();

    @Test
    void advancedPropertiesReturnsCustomizer() {
        var env = new MockEnvironment();
        var customizer = config.advancedProperties(env);
        assertThat(customizer).isNotNull();
    }

    @Test
    void advancedPropertiesAppliesIntegerFromEnvironment() {
        var env = new MockEnvironment();
        env.setProperty("spring.cassandra.advanced.connection.max-requests-per-connection", "10000");

        var customizer = config.advancedProperties(env);
        var builder = DriverConfigLoader.programmaticBuilder();
        customizer.customize(builder);
        var loader = builder.build();
        assertThat(loader).isNotNull();
    }

    @Test
    void advancedPropertiesAppliesDurationFromEnvironment() {
        var env = new MockEnvironment();
        env.setProperty("spring.cassandra.advanced.basic.request.timeout", "5000");

        var customizer = config.advancedProperties(env);
        var builder = DriverConfigLoader.programmaticBuilder();
        customizer.customize(builder);
        var loader = builder.build();
        assertThat(loader).isNotNull();
    }

    @Test
    void advancedPropertiesIgnoresNonAdvancedProperties() {
        var env = new MockEnvironment();
        env.setProperty("spring.cassandra.contact-points", "localhost");
        env.setProperty("spring.cassandra.advanced.connection.max-requests-per-connection", "100");

        var customizer = config.advancedProperties(env);
        var builder = DriverConfigLoader.programmaticBuilder();
        customizer.customize(builder);
        assertThat(builder.build()).isNotNull();
    }

    @Test
    void advancedPropertiesIgnoresBlankProperties() {
        var env = new MockEnvironment();
        env.setProperty("spring.cassandra.advanced.connection.max-requests-per-connection", "   ");

        var customizer = config.advancedProperties(env);
        var builder = DriverConfigLoader.programmaticBuilder();
        customizer.customize(builder);
        assertThat(builder.build()).isNotNull();
    }
}
