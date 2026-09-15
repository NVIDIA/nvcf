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

package com.nvidia.boot.registries.configurations;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.catchThrowable;

import java.util.Map;
import java.util.Objects;
import org.junit.jupiter.api.Nested;
import org.junit.jupiter.api.Test;
import org.springframework.boot.context.properties.bind.BindException;
import org.springframework.boot.context.properties.bind.Bindable;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.boot.context.properties.bind.validation.BindValidationException;
import org.springframework.boot.context.properties.bind.validation.ValidationBindHandler;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.StandardEnvironment;
import org.springframework.validation.beanvalidation.LocalValidatorFactoryBean;

class RegistryConfigurationPropertiesValidationTest {

    private static final String CONFIG_PREFIX = "test.registries";

    private static String getValidationMessage(Throwable throwable) {
        var cause = throwable.getCause();
        if (cause instanceof BindValidationException bve) {
            return bve.getValidationErrors().getAllErrors().stream()
                    .map(org.springframework.validation.ObjectError::getDefaultMessage)
                    .filter(Objects::nonNull)
                    .reduce("", (a, b) -> a + " " + b);
        }
        return throwable.getMessage() != null ? throwable.getMessage() : "";
    }

    @Nested
    class BindingValidation {

        @Test
        void bindingFailsWhenNameIsMissing() {
            var env = new StandardEnvironment();
            env.getPropertySources().addFirst(new MapPropertySource("test", Map.of(
                    CONFIG_PREFIX + ".recognized.container.ngc.hostname", "nvcr.io")));

            var validator = new LocalValidatorFactoryBean();
            validator.afterPropertiesSet();
            var bindHandler = new ValidationBindHandler(validator);
            var binder = Binder.get(env);

            var throwable = catchThrowable(() -> binder
                    .bind(CONFIG_PREFIX,
                          Bindable.of(RegistryConfigurationProperties.class),
                          bindHandler)
                    .orElseThrow(() -> new IllegalStateException("Expected binding to fail")));
            assertThat(throwable).isInstanceOf(BindException.class);
            assertThat(getValidationMessage(throwable)).contains("Registry name is required");
        }

        @Test
        void bindingFailsWhenHostnameIsMissing() {
            var env = new StandardEnvironment();
            env.getPropertySources().addFirst(new MapPropertySource("test", Map.of(
                    CONFIG_PREFIX + ".recognized.container.ngc.name", "NGC")));

            var validator = new LocalValidatorFactoryBean();
            validator.afterPropertiesSet();
            var bindHandler = new ValidationBindHandler(validator);
            var binder = Binder.get(env);

            var throwable = catchThrowable(() -> binder
                    .bind(CONFIG_PREFIX,
                          Bindable.of(RegistryConfigurationProperties.class),
                          bindHandler)
                    .orElseThrow(() -> new IllegalStateException("Expected binding to fail")));
            assertThat(throwable).isInstanceOf(BindException.class);
            assertThat(getValidationMessage(throwable)).contains("Registry hostname is required");
        }

        @Test
        void bindingFailsWhenNameIsBlank() {
            var env = new StandardEnvironment();
            env.getPropertySources().addFirst(new MapPropertySource("test", Map.of(
                    CONFIG_PREFIX + ".recognized.container.ngc.name", "   ",
                    CONFIG_PREFIX + ".recognized.container.ngc.hostname", "nvcr.io")));

            var validator = new LocalValidatorFactoryBean();
            validator.afterPropertiesSet();
            var bindHandler = new ValidationBindHandler(validator);
            var binder = Binder.get(env);

            var throwable = catchThrowable(() -> binder
                    .bind(CONFIG_PREFIX,
                          Bindable.of(RegistryConfigurationProperties.class),
                          bindHandler)
                    .orElseThrow(() -> new IllegalStateException("Expected binding to fail")));
            assertThat(throwable).isInstanceOf(BindException.class);
            assertThat(getValidationMessage(throwable)).contains("Registry name is required");
        }

        @Test
        void bindingFailsWhenHostnameIsBlank() {
            var env = new StandardEnvironment();
            env.getPropertySources().addFirst(new MapPropertySource("test", Map.of(
                    CONFIG_PREFIX + ".recognized.container.ngc.name", "NGC",
                    CONFIG_PREFIX + ".recognized.container.ngc.hostname", "")));

            var validator = new LocalValidatorFactoryBean();
            validator.afterPropertiesSet();
            var bindHandler = new ValidationBindHandler(validator);
            var binder = Binder.get(env);

            var throwable = catchThrowable(() -> binder
                    .bind(CONFIG_PREFIX,
                          Bindable.of(RegistryConfigurationProperties.class),
                          bindHandler)
                    .orElseThrow(() -> new IllegalStateException("Expected binding to fail")));
            assertThat(throwable).isInstanceOf(BindException.class);
            assertThat(getValidationMessage(throwable)).contains("Registry hostname is required");
        }

        @Test
        void bindingSucceedsWhenNameAndHostnameArePresent() {
            var env = new StandardEnvironment();
            env.getPropertySources().addFirst(new MapPropertySource("test", Map.of(
                    CONFIG_PREFIX + ".recognized.container.ngc.name", "NGC",
                    CONFIG_PREFIX + ".recognized.container.ngc.hostname", "nvcr.io")));

            var validator = new LocalValidatorFactoryBean();
            validator.afterPropertiesSet();
            var bindHandler = new ValidationBindHandler(validator);
            var binder = Binder.get(env);

            var result = binder.bind(CONFIG_PREFIX,
                                     Bindable.of(RegistryConfigurationProperties.class),
                                     bindHandler);

            assertThat(result.isBound()).isTrue();
            var config = result.get();
            var ngcConfig = config.getRecognized().getContainer().get("ngc");
            assertThat(ngcConfig.getName()).isEqualTo("NGC");
            assertThat(ngcConfig.getHostname()).isEqualTo("nvcr.io");
        }
    }
}
