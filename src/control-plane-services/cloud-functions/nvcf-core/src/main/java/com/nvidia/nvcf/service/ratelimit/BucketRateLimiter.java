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
package com.nvidia.nvcf.service.ratelimit;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import io.github.bucket4j.Bandwidth;
import io.github.bucket4j.Bucket;
import io.github.bucket4j.ConsumptionProbe;
import jakarta.validation.constraints.NotNull;
import java.time.Duration;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class BucketRateLimiter implements RateLimiter {

    private static final String MSG_ACCOUNT_PREFIX = "Account '%s'";
    private static final String MSG_FUNCTION_PREFIX = "Function id '%s'";
    private static final String MSG_FUNCTION_VERSION_PREFIX = "Function id '%s', version '%s'";
    private static final String MSG_CONSUMED_TOKEN =
            "{}: consumed-token: {}, remaining-tokens:'{}'";
    private static final String MSG_TOO_MANY_REQUESTS = "%s: too many requests. Slow down.";
    private static final String MSG_NEW_BUCKET =
            "{}: creating a new bucket with bandwidth {} per second";

    private final LoadingCache<CacheKey, Bucket> bucketMap;

    private record CacheKey(String id, long bandwidthPerSecond, String msgPrefix) {

    }

    BucketRateLimiter() {
        bucketMap = Caffeine.newBuilder()
                .maximumSize(10_000)
                .expireAfterWrite(Duration.ofMinutes(10))
                .scheduler(Scheduler.systemScheduler())
                .recordStats()
                .build(BucketRateLimiter::newBucket);
    }

    @Override
    public void accountLimit(String ncaId, long allowedInvocations) {
        verifyCall(ncaId, allowedInvocations, String.format(MSG_ACCOUNT_PREFIX, ncaId));
    }

    @Override
    public void functionLimit(UUID functionId, long allowedInvocations) {
        verifyCall(functionId.toString(), allowedInvocations,
                   String.format(MSG_FUNCTION_PREFIX, functionId));
    }

    @Override
    public void functionVersionLimit(
            UUID functionId, UUID functionVersionId, long allowedInvocations) {
        verifyCall(functionVersionId.toString(), allowedInvocations,
                   String.format(MSG_FUNCTION_VERSION_PREFIX, functionId, functionVersionId));
    }

    private void verifyCall(String id, long bandwidthPerSecond, String msgPrefix) {
        final Bucket bucket = bucketMap.get(new CacheKey(id, bandwidthPerSecond, msgPrefix));
        final ConsumptionProbe consumptionProbe = bucket.tryConsumeAndReturnRemaining(1);
        final boolean consumed = consumptionProbe.isConsumed();
        log.debug(MSG_CONSUMED_TOKEN, msgPrefix, consumed, consumptionProbe.getRemainingTokens());
        if (!consumed) {
            var msg = String.format(MSG_TOO_MANY_REQUESTS, msgPrefix);
            log.error(msg);
            throw new TooManyRequestsException(msg);
        }
    }

    private static Bucket newBucket(@NotNull CacheKey key) {
        log.debug(MSG_NEW_BUCKET, key.msgPrefix, key.bandwidthPerSecond);

        return Bucket.builder()
                .addLimit(
                        Bandwidth
                                .builder()
                                .capacity(key.bandwidthPerSecond)
                                .refillGreedy(key.bandwidthPerSecond, Duration.ofSeconds(1))
                                .build())
                .build();
    }
}
