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
import com.nvidia.apikeys.dto.introspection.IntrospectionRequest;
import com.nvidia.apikeys.vo.CachedIntrospectionResponse;
import com.nvidia.boot.exceptions.BootResponseException;
import java.time.Duration;
import java.util.Optional;
import org.springframework.stereotype.Service;

@Service
public class IntrospectionLocalCache {

    public static final int LOCAL_CACHE_TTL_SECONDS = 60;
    public static final int LOCAL_CACHE_MAXIMUM_SIZE = 10000;

    private final Cache<IntrospectionRequest, CachedIntrospectionResponse> validResponseCache =
            CacheBuilder.newBuilder()
                    .concurrencyLevel(16)
                    .expireAfterWrite(Duration.ofSeconds(LOCAL_CACHE_TTL_SECONDS))
                    .maximumSize(LOCAL_CACHE_MAXIMUM_SIZE)
                    .build();

    private final Cache<IntrospectionRequest, BootResponseException> errorCache =
            CacheBuilder.newBuilder()
                    .expireAfterWrite(Duration.ofSeconds(LOCAL_CACHE_TTL_SECONDS))
                    .maximumSize(LOCAL_CACHE_MAXIMUM_SIZE)
                    .build();

    public void cacheValidResponse(IntrospectionRequest request, CachedIntrospectionResponse response) {
        validResponseCache.put(request, response);
    }

    public Optional<CachedIntrospectionResponse> getCachedResponse(IntrospectionRequest request) {
        return Optional.ofNullable(validResponseCache.getIfPresent(request));
    }

    public void cacheError(IntrospectionRequest request, BootResponseException exception) {
        errorCache.put(request, exception);
    }

    public Optional<BootResponseException> getKnownInvalidResponse(IntrospectionRequest request) {
        return Optional.ofNullable(errorCache.getIfPresent(request));
    }

    public void invalidate() {
        errorCache.invalidateAll();
        validResponseCache.invalidateAll();
    }
}
