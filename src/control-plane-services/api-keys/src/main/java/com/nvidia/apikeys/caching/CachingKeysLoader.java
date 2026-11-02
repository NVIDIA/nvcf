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

import com.google.common.cache.Cache;
import com.google.common.cache.CacheBuilder;
import com.nvidia.apikeys.services.IntrospectionKeyLoader;
import com.nvidia.apikeys.vo.IntrospectionKeyVo;
import com.nvidia.boot.exceptions.NotFoundException;
import java.time.Clock;
import java.time.Duration;
import java.util.Optional;
import java.util.concurrent.ExecutionException;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class CachingKeysLoader {

    private final IntrospectionKeyLoader keyLoader;
    private final PrefetchingCache<String, IntrospectionKeyVo> prefetchingCache;
    private final Cache<String, Boolean> invalidKeysCache;

    public CachingKeysLoader(IntrospectionKeyLoader keyLoader, Clock clock) {
        this.keyLoader = keyLoader;

        prefetchingCache = new PrefetchingCache<>(
                keyLoader::getKeyByHash, clock, Duration.ofSeconds(60));

        invalidKeysCache = CacheBuilder.newBuilder()
                .maximumSize(1000)
                .expireAfterWrite(Duration.ofSeconds(60))
                .build();
    }

    public IntrospectionKeyVo loadKeyByHash(String apiKeyHash) {
        if (invalidKeysCache.getIfPresent(apiKeyHash) != null) {
            throw new NotFoundException("Key not found");
        }

        Optional<IntrospectionKeyVo> key;
        try {
            key = prefetchingCache.get(apiKeyHash);
        } catch (ExecutionException e) {
            log.error("failed to load key from prefetching cache, falling back", e);
            return keyLoader.getKeyByHash(apiKeyHash)
                    .orElseThrow(() -> new NotFoundException("Key not found"));
        }
        if (key.isEmpty()) {
            invalidKeysCache.put(apiKeyHash, Boolean.TRUE);
            throw new NotFoundException("Key not found");
        }
        return key.get();
    }

    public void invalidate() {
        prefetchingCache.invalidate();
    }
}
