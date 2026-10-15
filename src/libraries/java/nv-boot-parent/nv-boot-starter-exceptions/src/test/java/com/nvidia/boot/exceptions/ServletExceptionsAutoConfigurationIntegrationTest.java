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

package com.nvidia.boot.exceptions;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.exceptions.handlers.DefaultMvcExceptionHandler;
import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.test.context.runner.WebApplicationContextRunner;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.servlet.mvc.method.annotation.ResponseEntityExceptionHandler;

class ServletExceptionsAutoConfigurationIntegrationTest {

    private final WebApplicationContextRunner contextRunner = new WebApplicationContextRunner()
            .withPropertyValues("spring.main.web-application-type=servlet")
            .withConfiguration(AutoConfigurations.of(ServletExceptionsAutoConfiguration.class));

    @Test
    void registersDefaultMvcExceptionHandler() {
        contextRunner.run((var context) -> assertThat(context).hasSingleBean(DefaultMvcExceptionHandler.class));
    }

    @Test
    void customServletResponseEntityExceptionHandlerPreventsDefaultBean() {
        new WebApplicationContextRunner()
                .withPropertyValues("spring.main.web-application-type=servlet")
                .withUserConfiguration(CustomMvcHandlerConfig.class)
                .withConfiguration(AutoConfigurations.of(ServletExceptionsAutoConfiguration.class))
                .run((var context) -> {
                    assertThat(context).doesNotHaveBean(DefaultMvcExceptionHandler.class);
                    assertThat(context).hasBean("customMvcHandler");
                });
    }

    @Configuration
    static class CustomMvcHandlerConfig {

        @Bean
        ResponseEntityExceptionHandler customMvcHandler() {
            return new ResponseEntityExceptionHandler() {};
        }
    }
}
