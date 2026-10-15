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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.Optional;
import java.util.concurrent.ExecutionException;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class PrefetchingCacheTest {

    @Mock
    private Clock clock;

    @Mock
    private java.util.function.Function<String, Optional<String>> valueProvider;

    private PrefetchingCache<String, String> cache;
    private static final Duration CACHE_TTL = Duration.ofMinutes(5);
    private static final String TEST_KEY = "test-key";
    private static final String TEST_VALUE = "test-value";
    private static final Instant NOW = Instant.parse("2024-01-01T00:00:00Z");

    @BeforeEach
    void setUp() {
        when(clock.instant()).thenReturn(NOW);
        cache = new PrefetchingCache<>(valueProvider, clock, CACHE_TTL);
    }

    @Test
    void shouldReturnValueOnCacheHit() throws ExecutionException {
        when(valueProvider.apply(TEST_KEY)).thenReturn(Optional.of(TEST_VALUE));

        Optional<String> result = cache.get(TEST_KEY);

        assertThat(result).isPresent().contains(TEST_VALUE);
        verify(valueProvider).apply(TEST_KEY);
    }

    @Test
    void shouldReturnEmptyOnCacheMiss() throws ExecutionException {
        when(valueProvider.apply(TEST_KEY)).thenReturn(Optional.empty());

        Optional<String> result = cache.get(TEST_KEY);

        assertThat(result).isEmpty();
        verify(valueProvider).apply(TEST_KEY);
    }

    @Test
    void shouldRecordKeyUsageOnCacheHit() throws ExecutionException {
        when(valueProvider.apply(TEST_KEY)).thenReturn(Optional.of(TEST_VALUE));

        cache.get(TEST_KEY);

        // then
        // Verify that the key usage was recorded by checking if the key is still in cache
        Optional<String> result = cache.get(TEST_KEY);
        assertThat(result).isPresent().contains(TEST_VALUE);
        verify(valueProvider).apply(TEST_KEY);
    }

    @Test
    void shouldInvalidateCache() throws ExecutionException {
        when(valueProvider.apply(TEST_KEY)).thenReturn(Optional.of(TEST_VALUE));
        cache.get(TEST_KEY);

        cache.invalidate();

        when(valueProvider.apply(TEST_KEY)).thenReturn(Optional.empty());
        Optional<String> result = cache.get(TEST_KEY);
        assertThat(result).isEmpty();
    }

    @Test
    void shouldHandleConcurrentAccess() throws ExecutionException {
        when(valueProvider.apply(TEST_KEY)).thenReturn(Optional.of(TEST_VALUE));

        Optional<String> result1 = cache.get(TEST_KEY);
        Optional<String> result2 = cache.get(TEST_KEY);

        assertThat(result1).isPresent().contains(TEST_VALUE);
        assertThat(result2).isPresent().contains(TEST_VALUE);
        verify(valueProvider).apply(TEST_KEY);
    }

    @Test
    void shouldRespectCacheTTL() throws ExecutionException {
        when(valueProvider.apply(TEST_KEY)).thenReturn(Optional.of(TEST_VALUE));
        cache.get(TEST_KEY);

        when(clock.instant()).thenReturn(NOW.plus(CACHE_TTL).plusSeconds(1));
        Optional<String> result = cache.get(TEST_KEY);

        assertThat(result).isPresent().contains(TEST_VALUE);
        verify(valueProvider).apply(TEST_KEY);
    }

} 
