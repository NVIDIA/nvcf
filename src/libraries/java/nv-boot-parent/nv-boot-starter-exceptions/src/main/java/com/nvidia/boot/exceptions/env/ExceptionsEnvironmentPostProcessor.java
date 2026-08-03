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

package com.nvidia.boot.exceptions.env;

import java.io.IOException;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.EnvironmentPostProcessor;
import org.springframework.core.Ordered;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.core.env.PropertiesPropertySource;
import org.springframework.core.io.ClassPathResource;
import org.springframework.core.io.support.PropertiesLoaderUtils;

/**
 * Environment post processor that loads logback configuration properties
 * for filtered stack traces (StackFilteringThrowableConverter).
 */
@Slf4j
public class ExceptionsEnvironmentPostProcessor implements EnvironmentPostProcessor, Ordered {

    private static final String EXCEPTIONS_DEFAULTS_FILE =
            "nv-boot-exceptions-defaults.properties";

    @Override
    public void postProcessEnvironment(
            ConfigurableEnvironment environment,
            SpringApplication application) {
        loadExceptionDefaults(environment);
    }

    private void loadExceptionDefaults(ConfigurableEnvironment environment) {
        loadPropertiesFile(environment, EXCEPTIONS_DEFAULTS_FILE, "nv-boot-exceptions-defaults");
    }

    private void loadPropertiesFile(
            ConfigurableEnvironment environment,
            String filename,
            String sourceName) {
        try {
            var resource = new ClassPathResource(filename);
            if (resource.exists()) {
                var properties = PropertiesLoaderUtils.loadProperties(resource);
                environment.getPropertySources().addLast(
                        new PropertiesPropertySource(sourceName, properties)
                );
            }
        } catch (IOException e) {
            log.warn("Failed to load properties file '{}'", filename);
        }
    }

    @Override
    public int getOrder() {
        return Ordered.LOWEST_PRECEDENCE - 10;
    }
}
