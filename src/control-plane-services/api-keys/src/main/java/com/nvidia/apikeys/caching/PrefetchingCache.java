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

package com.nvidia.apikeys.caching;

import com.google.common.cache.CacheBuilder;
import com.google.common.cache.CacheLoader;
import com.google.common.cache.LoadingCache;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutionException;
import java.util.function.Function;
import lombok.Builder;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class PrefetchingCache<K, V> {

    public static final int KEY_USAGE_TO_TRACK_MAX_COUNT = 2000;
    public static final int PREFETCH_TASK_INITIAL_DELAY_SECONDS = 60;
    public static final Duration KEY_USAGE_MAX_IDLE_TIME = Duration.ofMinutes(
            PREFETCH_TASK_INITIAL_DELAY_SECONDS);
    public static final int MAX_PREFETCHED_KEYS = 1000;
    private final LoadingCache<K, Optional<V>> keysCache;
    private final Clock clock;
    private final Map<K, Record<K>> records = new ConcurrentHashMap<>();

    /**
     * The purpose of this cache is to keep track of key fetch time. Any time a key is refreshed it
     * gets a value that points to "current" time. While reading from cache returns this time if
     * known without resetting the time.
     */
    private final LoadingCache<K, Instant> keyLastFetchTime;

    /**
     * Methods invalidate all cache records. Not intended to be used in normal flows, only during
     * testing.
     */
    public void invalidate() {
        records.clear();
        keysCache.invalidateAll();
    }

    @Builder(toBuilder = true)
    private static class Record<T> {

        @Getter
        private T key;
        private Instant lastFetchTime;
        private Instant lastUseTime;
        private Duration averageTimeBetweenRequests;
        private Instant projectedLoadTime;
    }

    public PrefetchingCache(Function<K, Optional<V>> valueProvider, Clock clock, Duration keyCacheTtl) {
        this.clock = clock;

        keyLastFetchTime = CacheBuilder.newBuilder()
                .maximumSize(KEY_USAGE_TO_TRACK_MAX_COUNT)
                .expireAfterWrite(KEY_USAGE_MAX_IDLE_TIME)
                .build(CacheLoader.from(k -> clock.instant()));

        keysCache = CacheBuilder.newBuilder()
                .expireAfterWrite(keyCacheTtl)
                .concurrencyLevel(16)
                .maximumSize(MAX_PREFETCHED_KEYS)
                .build(CacheLoader.from(k -> {
                    Optional<V> value = valueProvider.apply(k);
                    keyLastFetchTime.refresh(k);
                    log.debug("value refresh {} complete", k);
                    return value;
                }));

    }

    public Optional<V> get(K key)
            throws ExecutionException {
        Optional<V> value = keysCache.get(key);
        if (value.isPresent()) {
            recordKeyUse(key);
        }
        return value;
    }

    private void recordKeyUse(K key) {
        Instant now = clock.instant();
        Record<K> keyRecord = records.computeIfAbsent(
                key, k -> Record.<K>builder()
                        .key(k)
                        .lastFetchTime(now)
                        .averageTimeBetweenRequests(Duration.ZERO)
                        .projectedLoadTime(now)
                        .lastUseTime(now)
                        .build());

        log.debug("noticed key {} use", key);

        if (now.isAfter(keyRecord.lastUseTime)) {
            Duration timeSinceLastUpdate = Duration.between(keyRecord.lastUseTime, now);
            var averageTimeBetweenRequests = timeSinceLastUpdate
                    .plus(keyRecord.averageTimeBetweenRequests)
                    .dividedBy(2);

            var projectedLoadTime = now.plus(averageTimeBetweenRequests);

            log.debug(
                    "key {} used {} seconds ago, avg update time {} seconds, next update projected at {}",
                    key, timeSinceLastUpdate.getSeconds(),
                    averageTimeBetweenRequests.getSeconds(), projectedLoadTime);

            records.put(key, keyRecord.toBuilder()
                    .lastUseTime(now)
                    .averageTimeBetweenRequests(averageTimeBetweenRequests)
                    .projectedLoadTime(projectedLoadTime)
                    .build());
        }
    }

}
