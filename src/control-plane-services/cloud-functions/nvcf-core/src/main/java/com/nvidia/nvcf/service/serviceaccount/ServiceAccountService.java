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
package com.nvidia.nvcf.service.serviceaccount;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.RateLimitAttributes;
import java.time.Duration;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClientRequestException;

@Slf4j
@Service
public class ServiceAccountService {
    private static final String MESG_RATE_LIMIT_FROM_BACKUP_CACHE =
            "Returning tiered rate limit from backup cache as NAK is not reachable - '{}'";
    private static final String MESG_RATE_LIMIT_NOT_IN_BACKUP_CACHE =
            "NAK is not reachable and tiered rate limit is not in backup cache anymore - '{}'";
    private static final String MESG_TIERED_RATE_LIMIT = "Tiered rate limit for ncaId '{}': '{}'";
    private static final RateLimitAttributes NO_RATE_LIMIT = new RateLimitAttributes(null, null);

    private final ServiceAccountClient serviceAccountClient;
    private final boolean tieredRateLimitEnabled;
    private final LoadingCache<String, RateLimitAttributes> tieredRateLimitCache =
            Caffeine.newBuilder()
                    .maximumSize(512).expireAfterWrite(Duration.ofMinutes(1))
                    .scheduler(Scheduler.systemScheduler())
                    .build(this::fetchTieredRateLimit);
    private final Cache<String, RateLimitAttributes> tieredRateLimitBackupCache =
            Caffeine.newBuilder()
                    .maximumSize(512).expireAfterWrite(Duration.ofMinutes(60))
                    .scheduler(Scheduler.systemScheduler())
                    .build();

    // The tiered-rate policy's rate-lookup data source isn't registered yet, and the real
    // provider is still unknown. Default off so deploying this code doesn't fail-close every
    // LLM invocation; flip once that dependency lands.
    public ServiceAccountService(
            ServiceAccountClient serviceAccountClient,
            @Value("${nvcf.account-token-rate-limit-enabled:false}") boolean tieredRateLimitEnabled) {
        this.serviceAccountClient = serviceAccountClient;
        this.tieredRateLimitEnabled = tieredRateLimitEnabled;
    }

    private RateLimitAttributes fetchTieredRateLimit(String ncaId) {
        try {
            var result = serviceAccountClient.fetchTieredRateLimit(ncaId);
            log.debug(MESG_TIERED_RATE_LIMIT, ncaId, result);
            tieredRateLimitBackupCache.put(ncaId, result);
            return result;
        } catch (WebClientRequestException | UpstreamException ex) {
            // WebClientRequestException is thrown when the external service (NAK) is not
            // reachable. NVCF should use the backup cache only when NAK is not reachable. For
            // other exceptions, backup cache should not be used.
            return fetchTieredRateLimitFromBackupCache(ncaId, ex);
        }
    }

    // Throws if NAK is unreachable and the value isn't backup-cached either; matches the
    // API-key path's fail-closed behavior on lookup failure rather than silently omitting
    // the rate limit.
    public RateLimitAttributes getTieredRateLimit(String ncaId) {
        if (!tieredRateLimitEnabled) {
            return NO_RATE_LIMIT;
        }
        return tieredRateLimitCache.get(ncaId);
    }

    @VisibleForTesting
    public void invalidateCache() {
        tieredRateLimitCache.invalidateAll();
        tieredRateLimitBackupCache.invalidateAll();
    }

    @VisibleForTesting
    public void invalidatePrimaryCache() {
        tieredRateLimitCache.invalidateAll();
    }

    private RateLimitAttributes fetchTieredRateLimitFromBackupCache(
            String ncaId,
            RuntimeException ex) {
        var rateLimit = tieredRateLimitBackupCache.getIfPresent(ncaId);
        if (rateLimit == null) {
            log.error(MESG_RATE_LIMIT_NOT_IN_BACKUP_CACHE, ex.getMessage());
            throw ex;
        }
        log.info(MESG_RATE_LIMIT_FROM_BACKUP_CACHE, ex.getMessage());
        return rateLimit;
    }
}
