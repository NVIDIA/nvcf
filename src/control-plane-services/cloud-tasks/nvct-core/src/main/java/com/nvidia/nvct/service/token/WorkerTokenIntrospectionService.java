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
package com.nvidia.nvct.service.token;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.nvidia.nvct.service.icms.IcmsClient;
import com.nvidia.nvct.service.icms.IcmsStubService;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.time.Duration;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

/**
 * Calls the ICMS worker token introspection endpoint (RFC 7662) and caches
 * active results keyed on a SHA-256 digest of the raw token.  Results are
 * evicted after at most the PSAT TTL (15 minutes).  Inactive results are never
 * cached so clock-skew and nbf windows are handled on the next retry.
 */
@Slf4j
@Service
public class WorkerTokenIntrospectionService {

    private static final Duration CACHE_TTL = Duration.ofMinutes(14);

    private final IcmsClient icmsClient;
    private final boolean enabled;
    private final Cache<String, IcmsStubService.WorkerTokenIntrospectResult> cache;

    public WorkerTokenIntrospectionService(
            IcmsClient icmsClient,
            @Value("${nvct.worker.delegated-token-enabled:false}") boolean enabled) {
        this.icmsClient = icmsClient;
        this.enabled = enabled;
        this.cache = Caffeine.newBuilder()
                .maximumSize(10_000)
                .expireAfterWrite(CACHE_TTL)
                .<String, IcmsStubService.WorkerTokenIntrospectResult>build();
    }

    public boolean isEnabled() {
        return enabled;
    }

    /**
     * Introspects a worker token against ICMS.  Returns the result with
     * {@code active=true} when the token is valid; {@code active=false} otherwise.
     * Active results are cached; inactive results are not.
     */
    public IcmsStubService.WorkerTokenIntrospectResult introspect(String rawToken) {
        var cacheKey = sha256Hex(rawToken);
        var cached = cache.getIfPresent(cacheKey);
        if (cached != null) {
            log.debug("worker token introspection cache hit");
            return cached;
        }

        log.debug("calling ICMS to introspect worker token");
        var result = icmsClient.introspectWorkerToken(
                IcmsStubService.WorkerTokenIntrospectRequest.builder().token(rawToken).build());

        if (result.isActive()) {
            cache.put(cacheKey, result);
        }
        return result;
    }

    private static String sha256Hex(String input) {
        try {
            var digest = MessageDigest.getInstance("SHA-256");
            var hash = digest.digest(input.getBytes(StandardCharsets.UTF_8));
            var sb = new StringBuilder(hash.length * 2);
            for (byte b : hash) {
                sb.append(String.format("%02x", b));
            }
            return sb.toString();
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 not available", e);
        }
    }
}
