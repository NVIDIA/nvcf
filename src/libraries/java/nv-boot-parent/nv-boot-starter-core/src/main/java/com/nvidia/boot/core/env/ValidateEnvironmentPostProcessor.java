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

import java.util.ArrayList;
import java.util.List;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.EnvironmentPostProcessor;
import org.springframework.core.Ordered;
import org.springframework.core.env.ConfigurableEnvironment;

/**
 * Validates that required properties are configured at startup.
 * Fails fast if critical configuration is missing.
 */
public class ValidateEnvironmentPostProcessor implements EnvironmentPostProcessor, Ordered {

    private static final List<String> REQUIRED_PROPERTIES = List.of(
            "spring.application.name",
            "spring.application.version",
            "spring.profiles.active"
    );

    @Override
    public void postProcessEnvironment(
            ConfigurableEnvironment environment,
            SpringApplication application) {
        var missingRequired = new ArrayList<String>();

        for (var property : REQUIRED_PROPERTIES) {
            if (!environment.containsProperty(property)
                    || StringUtils.isBlank(environment.getProperty(property))) {
                missingRequired.add(property);
            }
        }

        if (!missingRequired.isEmpty()) {
            throw new IllegalStateException(
                    "Required properties not configured: " + missingRequired
                            + ". Please set these properties in application.yaml or " +
                            "environment variables."
            );
        }
    }

    @Override
    public int getOrder() {
        return Ordered.LOWEST_PRECEDENCE;
    }
}
