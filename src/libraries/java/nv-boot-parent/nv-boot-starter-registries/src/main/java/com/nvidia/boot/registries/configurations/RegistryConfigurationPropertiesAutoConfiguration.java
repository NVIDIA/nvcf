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

import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnBean;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.boot.context.properties.bind.Bindable;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.boot.context.properties.bind.validation.ValidationBindHandler;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.core.env.Environment;
import org.springframework.validation.Validator;

@AutoConfiguration
@Slf4j
@RequiredArgsConstructor
@ConditionalOnBean(RegistryConfigPathProvider.class)
@EnableConfigurationProperties
public class RegistryConfigurationPropertiesAutoConfiguration {
    private final RegistryConfigPathProvider configPathProvider;

    @Bean
    @RefreshScope
    public RegistryConfigurationProperties registryConfigurationProperties(
            Environment env,
            Validator validator) {
        var configPath = configPathProvider.getConfigPath();
        var bindHandler = new ValidationBindHandler(validator);
        return Binder.get(env)
                .bind(configPath, Bindable.of(RegistryConfigurationProperties.class), bindHandler)
                .orElseThrow(() -> new IllegalStateException("Wrong registries configuration"));
    }
}
