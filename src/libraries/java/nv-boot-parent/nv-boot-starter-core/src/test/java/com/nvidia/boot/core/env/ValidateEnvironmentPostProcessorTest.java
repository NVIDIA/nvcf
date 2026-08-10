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

package com.nvidia.boot.core.env;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import org.junit.jupiter.api.Test;
import org.springframework.boot.SpringApplication;
import org.springframework.mock.env.MockEnvironment;

class ValidateEnvironmentPostProcessorTest {

    @Test
    void throwsWhenRequiredPropertiesMissing() {
        var environment = new MockEnvironment();
        var application = new SpringApplication();
        var processor = new ValidateEnvironmentPostProcessor();

        assertThatThrownBy(() -> processor.postProcessEnvironment(environment, application))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("Required properties not configured");
    }

    @Test
    void throwsWhenRequiredPropertiesBlank() {
        var environment = new MockEnvironment();
        environment.setProperty("spring.application.name", "   ");
        environment.setProperty("spring.application.version", "");
        environment.setProperty("spring.profiles.active", "test");
        var application = new SpringApplication();
        var processor = new ValidateEnvironmentPostProcessor();

        assertThatThrownBy(() -> processor.postProcessEnvironment(environment, application))
                .isInstanceOf(IllegalStateException.class);
    }

    @Test
    void succeedsWhenAllRequiredPropertiesSet() {
        var environment = new MockEnvironment();
        environment.setProperty("spring.application.name", "my-app");
        environment.setProperty("spring.application.version", "1.0.0");
        environment.setProperty("spring.profiles.active", "test");
        var application = new SpringApplication();
        var processor = new ValidateEnvironmentPostProcessor();

        processor.postProcessEnvironment(environment, application);

        assertThat(processor.getOrder()).isEqualTo(Integer.MAX_VALUE);
    }
}
