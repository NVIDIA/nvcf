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

package com.nvidia.boot.properties.env;

import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.ENABLED;
import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.FILE;
import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.PROPERTY_SOURCE_NAME;

import com.nvidia.boot.properties.property.YamlPropertySourceFactory;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.EnvironmentPostProcessor;
import org.springframework.core.env.ConfigurableEnvironment;
import org.springframework.core.env.PropertySource;

/**
 * EnvironmentPostProcessor that adds the reloadable properties file as a PropertySource
 * during environment preparation, before PropertySourceLocators run. This allows other
 * libraries' BootstrapConfigurations to reference properties from the reloadable file.
 * <p>
 * Configure via bootstrap.yaml under {@code nv-boot.reloadable-properties}.
 */
public class ReloadablePropertiesEnvironmentPostProcessor implements EnvironmentPostProcessor {

    @Override
    public void postProcessEnvironment(
            ConfigurableEnvironment environment,
            SpringApplication application) {
        if (shouldSkip(environment)) {
            return;
        }

        var propertySourceName = PROPERTY_SOURCE_NAME;
        var sources = environment.getPropertySources();
        sources.remove(propertySourceName);
        sources.addFirst(createPropertySource(environment, propertySourceName));
    }

    private static boolean shouldSkip(ConfigurableEnvironment environment) {
        var enabled = environment.getProperty(ENABLED, "true");
        return !"true".equalsIgnoreCase(enabled);
    }

    private PropertySource<?> createPropertySource(
            ConfigurableEnvironment environment,
            String propertySourceName) {
        var file = environment.getProperty(FILE);
        if (StringUtils.isBlank(file)) {
            throw new IllegalStateException(
                    FILE + " must be set when " + ENABLED + " is true");
        }

        return new YamlPropertySourceFactory().createPropertySource(propertySourceName, file);
    }
}
