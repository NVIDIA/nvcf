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
package com.nvidia.nvct.configuration;

import com.nvidia.boot.registries.configurations.RegistryConfigPathProvider;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * Registry configuration for NVCT. Uses {@code proxyBeanMethods = false} to avoid early
 * singleton creation that triggers ConfigurationClassPostProcessor warnings. Safe here
 * because the {@code @Bean} method does not call other {@code @Bean} methods on this class.
 */
@Configuration(proxyBeanMethods = false)
public class RegistryConfiguration {
    private static final String REGISTRY_CONFIG_PATH = "nvct.registries";

    @Bean
    public RegistryConfigPathProvider registryConfigPathProvider() {
        return () -> REGISTRY_CONFIG_PATH;
    }
}
