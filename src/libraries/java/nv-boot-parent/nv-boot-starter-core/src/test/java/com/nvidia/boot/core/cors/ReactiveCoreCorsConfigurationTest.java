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

package com.nvidia.boot.core.cors;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.runner.ReactiveWebApplicationContextRunner;
import org.springframework.web.cors.CorsConfiguration;
import org.springframework.web.cors.reactive.CorsWebFilter;

class ReactiveCoreCorsConfigurationTest {

    private final ReactiveWebApplicationContextRunner contextRunner =
            new ReactiveWebApplicationContextRunner()
                    .withUserConfiguration(
                            ReactiveCoreCorsConfiguration.class,
                            ServletCoreCorsConfiguration.class);

    @Test
    void servletCorsBeanIsNotLoaded() {
        contextRunner.run(context -> {
            assertThat(context).doesNotHaveBean(ServletCoreCorsConfiguration.class);
        });
    }

    @Test
    void corsWebFilterBeanIsRegistered() {
        contextRunner.run(context -> {
            assertThat(context).hasSingleBean(CorsWebFilter.class);
        });
    }

    @Test
    void corsConfigHasExpectedSettings() {
        var config = ReactiveCoreCorsConfiguration.corsConfig();

        assertThat(config.getAllowCredentials()).isTrue();
        assertThat(config.getAllowedHeaders()).containsExactly(CorsConfiguration.ALL);
        assertThat(config.getAllowedMethods()).containsExactly(CorsConfiguration.ALL);
        assertThat(config.getAllowedOriginPatterns()).containsExactly(CorsConfiguration.ALL);
        assertThat(config.getExposedHeaders()).containsExactly(CorsConfiguration.ALL);
        assertThat(config.getMaxAge()).isEqualTo(86400L);
    }
}
