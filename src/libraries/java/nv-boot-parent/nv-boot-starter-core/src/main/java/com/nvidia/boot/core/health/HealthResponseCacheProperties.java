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

import java.time.Duration;

/**
 * Configuration for caching {@code GET /health} responses in {@link CachedHealthResponseService}.
 *
 * <p>Applications may register a {@link org.springframework.context.annotation.Bean} of this type
 * to set the cache time-to-live. If no bean is present, the starter registers a default with {@link
 * #DEFAULT_TIME_TO_LIVE}.
 *
 * <pre>{@code
 * @Configuration
 * class MyAppHealthConfiguration {
 *     @Bean
 *     HealthResponseCacheProperties healthResponseCacheProperties() {
 *         return HealthResponseCacheProperties.of(Duration.ofSeconds(5));
 *     }
 * }
 * }</pre>
 */
public record HealthResponseCacheProperties(Duration timeToLive) {

    // Default TTL when the application does not define a HealthResponseCacheProperties bean.
    public static final Duration DEFAULT_TIME_TO_LIVE = Duration.ofSeconds(3);

    public HealthResponseCacheProperties {
        if (timeToLive.isNegative()) {
            throw new IllegalArgumentException("timeToLive must not be negative");
        }
    }

    // Default TTL; used by the starter when no custom bean is registered.
    public static HealthResponseCacheProperties ofDefaults() {
        return new HealthResponseCacheProperties(DEFAULT_TIME_TO_LIVE);
    }

    // Custom TTL. Zero means entries are always considered stale (refresh on every request).
    public static HealthResponseCacheProperties of(Duration timeToLive) {
        return new HealthResponseCacheProperties(timeToLive);
    }
}
