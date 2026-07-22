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

package com.nvidia.boot.properties;

import static com.nvidia.boot.properties.util.ReloadablePropertiesConstants.ENABLED;

import com.nvidia.boot.properties.property.ReloadablePropertiesConfigurationProvider;
import com.nvidia.boot.properties.refresher.FileBasedPropertiesRefresher;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.refresh.ContextRefresher;
import org.springframework.context.annotation.Bean;
import org.springframework.core.env.Environment;

/**
 * Auto-configuration for reloadable properties.
 * Enabled when {@code nv-boot.reloadable-properties.enabled} is true.
 */
@AutoConfiguration
@ConditionalOnProperty(name = ENABLED, havingValue = "true", matchIfMissing = true)
public class ReloadablePropertiesAutoConfiguration {

    @Bean
    public ReloadablePropertiesConfigurationProvider reloadablePropertiesConfigurationProvider(
            Environment environment) {
        return new ReloadablePropertiesConfigurationProvider(environment);
    }

    @Bean
    public FileBasedPropertiesRefresher fileBasedPropertiesRefresher(
            ContextRefresher contextRefresher,
            ReloadablePropertiesConfigurationProvider configurationProvider) {
        return new FileBasedPropertiesRefresher(contextRefresher, configurationProvider);
    }
}
