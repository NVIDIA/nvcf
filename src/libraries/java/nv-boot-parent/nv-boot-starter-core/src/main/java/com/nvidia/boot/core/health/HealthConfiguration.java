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

package com.nvidia.boot.core.health;

import java.util.Optional;
import org.springframework.boot.actuate.autoconfigure.endpoint.condition.ConditionalOnAvailableEndpoint;
import org.springframework.boot.health.actuate.endpoint.HealthEndpoint;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.boot.info.BuildProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.env.Environment;

/** Auto-configuration for health endpoint and controller. */
@Configuration
@ConditionalOnWebApplication
@ConditionalOnClass(HealthEndpoint.class)
public class HealthConfiguration {

    // Default cache TTL for GET /health when the app does not define its own
    // HealthResponseCacheProperties bean. Applications may override with a @Bean of
    // type HealthResponseCacheProperties.
    @Bean
    @ConditionalOnMissingBean(HealthResponseCacheProperties.class)
    @ConditionalOnAvailableEndpoint(endpoint = HealthEndpoint.class)
    public HealthResponseCacheProperties healthResponseCacheProperties() {
        return HealthResponseCacheProperties.ofDefaults();
    }

    @Bean
    @ConditionalOnAvailableEndpoint(endpoint = HealthEndpoint.class)
    public CachedHealthResponseService cachedHealthResponseService(
            HealthEndpoint healthEndpoint,
            HealthResponseCacheProperties healthResponseCacheProperties) {
        return new CachedHealthResponseService(healthEndpoint, healthResponseCacheProperties);
    }

    @Bean
    @ConditionalOnAvailableEndpoint(endpoint = HealthEndpoint.class)
    public HealthController healthController(
            CachedHealthResponseService cachedHealthResponseService) {
        return new HealthController(cachedHealthResponseService);
    }

    @Bean
    public ApplicationHealthIndicator applicationHealthIndicator(
            Environment environment,
            Optional<BuildProperties> buildProperties) {
        return new ApplicationHealthIndicator(environment, buildProperties);
    }
}
