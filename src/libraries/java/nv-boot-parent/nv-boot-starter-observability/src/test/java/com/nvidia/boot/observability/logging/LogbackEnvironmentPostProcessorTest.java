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

package com.nvidia.boot.observability.logging;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.springframework.boot.SpringApplication;
import org.springframework.core.Ordered;
import org.springframework.core.env.StandardEnvironment;

class LogbackEnvironmentPostProcessorTest {

    @Test
    void loadsLogbackPropertiesWhenPresent() {
        var environment = new StandardEnvironment();
        var application = new SpringApplication();
        var processor = new LogbackEnvironmentPostProcessor();

        processor.postProcessEnvironment(environment, application);

        assertThat(environment.getProperty("logging.config"))
                .isEqualTo("classpath:com/nvidia/boot/observability/logging/logback-boot-observability-defaults.xml");
        assertThat(environment.getProperty("logging.exception-conversion-word")).isEqualTo("%bootEx");
    }

    @Test
    void hasCorrectOrder() {
        var processor = new LogbackEnvironmentPostProcessor();
        assertThat(processor.getOrder()).isEqualTo(Ordered.LOWEST_PRECEDENCE - 10);
    }
}
