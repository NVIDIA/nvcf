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

package com.nvidia.boot.core.health;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import java.util.Optional;
import java.util.Properties;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.info.BuildProperties;
import org.springframework.core.env.Environment;

class ApplicationHealthIndicatorTest {

    private Environment environment;
    private ApplicationHealthIndicator indicator;

    @BeforeEach
    void setUp() {
        environment = mock(Environment.class);
    }

    @Test
    void healthUsesBuildPropertiesVersionWhenPresent() {
        var props = new Properties();
        props.setProperty("version", "2.0.0");
        var buildProperties = new BuildProperties(props);
        when(environment.getProperty("spring.application.name", "unknown")).thenReturn("my-app");
        when(environment.getActiveProfiles()).thenReturn(new String[]{"test"});
        indicator = new ApplicationHealthIndicator(environment, Optional.of(buildProperties));

        Health health = indicator.health();

        assertThat(health.getStatus().getCode()).isEqualTo("UP");
        assertThat(health.getDetails())
                .containsEntry("application", "my-app")
                .containsEntry("application.profile", "test")
                .containsEntry("version", "2.0.0");
    }

    @Test
    void healthUsesEnvironmentVersionWhenBuildPropertiesAbsent() {
        when(environment.getProperty("spring.application.name", "unknown")).thenReturn("my-app");
        when(environment.getActiveProfiles()).thenReturn(new String[]{"prod"});
        when(environment.getProperty("spring.application.version", "unknown")).thenReturn("3.0.0");
        indicator = new ApplicationHealthIndicator(environment, Optional.empty());

        Health health = indicator.health();

        assertThat(health.getStatus().getCode()).isEqualTo("UP");
        assertThat(health.getDetails())
                .containsEntry("application", "my-app")
                .containsEntry("application.profile", "prod")
                .containsEntry("version", "3.0.0");
    }
}
