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
import java.util.concurrent.atomic.AtomicReference;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.actuate.endpoint.HealthEndpoint;
import org.springframework.boot.health.contributor.Status;
import org.springframework.http.ResponseEntity;

/**
 * Caches minimal {@code /health} responses per {@link HealthResponseCacheProperties#timeToLive()}
 * to reduce load from frequent probes.
 */
public class CachedHealthResponseService {

    private final HealthEndpoint healthEndpoint;
    private final HealthResponseCacheProperties healthResponseCacheProperties;

    private final AtomicReference<CachedEntry> cache = new AtomicReference<>();

    public CachedHealthResponseService(
            HealthEndpoint healthEndpoint, HealthResponseCacheProperties healthResponseCacheProperties) {
        this.healthEndpoint = healthEndpoint;
        this.healthResponseCacheProperties = healthResponseCacheProperties;
    }

    // Returns a cached ResponseEntity when still fresh; otherwise refreshes from HealthEndpoint#health().
    public ResponseEntity<Health> getHealth() {
        long accessTime = System.currentTimeMillis();
        var cached = cache.get();
        if (cached == null
                || cached.isStale(accessTime, healthResponseCacheProperties.timeToLive())) {
            var health = healthEndpoint.health();
            var status = health.getStatus();
            var healthComponent = Health.status(status).build();
            ResponseEntity<Health> response;
            if (Status.UP.equals(status)) {
                response = ResponseEntity.ok(healthComponent);
            } else if (Status.DOWN.equals(status)) {
                response = ResponseEntity.status(503).body(healthComponent);
            } else {
                response = ResponseEntity.status(500).body(healthComponent);
            }
            cache.set(new CachedEntry(response, accessTime));
            return response;
        }
        return cached.response();
    }

    private static final class CachedEntry {
        private final ResponseEntity<Health> response;
        private final long creationTime;

        private CachedEntry(ResponseEntity<Health> response, long creationTime) {
            this.response = response;
            this.creationTime = creationTime;
        }

        private boolean isStale(long accessTime, Duration timeToLive) {
            return (accessTime - creationTime) >= timeToLive.toMillis();
        }

        private ResponseEntity<Health> response() {
            return response;
        }
    }
}
