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

import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;
import org.springframework.boot.autoconfigure.condition.ConditionalOnBean;
import org.springframework.context.annotation.Conditional;

/**
 * Conditional annotation that checks if a specific registry key exists in the configuration map,
 * to allows dynamic registry config location.
 */
@Target({ElementType.TYPE, ElementType.METHOD})
@Retention(RetentionPolicy.RUNTIME)
@Conditional(RegistryKeyCondition.class)
@ConditionalOnBean(RegistryConfigurationProperties.class)
public @interface ConditionalOnRegistryKey {

    /**
     * The registry type to check (e.g., "container", "model", "resource", "helm")
     */
    String registryType();

    /**
     * The registry key to check for (e.g., "ngc", "custom-registry")
     */
    String key();

    /**
     * Whether to require that the registry has a hostname configured.
     * Defaults to true since a registry without a hostname is typically not usable.
     */
    boolean requireHostname() default true;
}
