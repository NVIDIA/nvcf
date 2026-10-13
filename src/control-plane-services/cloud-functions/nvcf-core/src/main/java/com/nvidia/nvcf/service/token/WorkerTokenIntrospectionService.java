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
package com.nvidia.nvcf.service.token;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Expiry;
import com.nimbusds.jwt.JWTParser;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.text.ParseException;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

/**
 * Calls the ICMS worker token introspection endpoint (RFC 7662) and caches
 * active results keyed on a SHA-256 digest of the raw token. Active results are
 * evicted after min(token exp, 15 minutes). Inactive results, and active results
 * that lack the token expiry or the workload binding, are never cached.
 */
@Slf4j
@Service
public class WorkerTokenIntrospectionService {

    static final Duration CACHE_TTL = Duration.ofMinutes(15);
    static final String ERROR_MISSING_BINDING = "introspection response missing binding";
    private static final String DELEGATED_AUDIENCE_PREFIX = "nvcf-icms:";

    private final IcmsClient icmsClient;
    private final boolean enabled;
    private final Cache<String, IcmsStubService.WorkerTokenIntrospectResult> cache;

    public WorkerTokenIntrospectionService(
            IcmsClient icmsClient,
            @Value("${nvcf.worker.delegated-token.enabled:false}") boolean enabled) {
        this.icmsClient = icmsClient;
        this.enabled = enabled;
        this.cache = Caffeine.newBuilder()
                .maximumSize(10_000)
                .expireAfter(new Expiry<String, IcmsStubService.WorkerTokenIntrospectResult>() {
                    @Override
                    public long expireAfterCreate(
                            String key, IcmsStubService.WorkerTokenIntrospectResult value,
                            long currentTime) {
                        if (value.getExp() == null) {
                            return 0;
                        }
                        long remainingMs = value.getExp() * 1000L - Instant.now().toEpochMilli();
                        long cappedMs = Math.max(0, Math.min(CACHE_TTL.toMillis(), remainingMs));
                        return TimeUnit.MILLISECONDS.toNanos(cappedMs);
                    }

                    @Override
                    public long expireAfterUpdate(
                            String key, IcmsStubService.WorkerTokenIntrospectResult value,
                            long currentTime, long currentDuration) {
                        return currentDuration;
                    }

                    @Override
                    public long expireAfterRead(
                            String key, IcmsStubService.WorkerTokenIntrospectResult value,
                            long currentTime, long currentDuration) {
                        return currentDuration;
                    }
                })
                .build();
    }

    public boolean isEnabled() {
        return enabled;
    }

    /**
     * Returns true when the bearer token has the shape of a delegated worker token: a
     * compact JWS whose audience carries the cluster-scoped ICMS prefix. Legacy NVCF
     * worker tokens are JWE blobs and never match. The signature is not verified here;
     * ICMS verifies it during introspection.
     */
    public static boolean isDelegatedToken(String token) {
        if (StringUtils.isBlank(token)) {
            return false;
        }
        try {
            var jwt = JWTParser.parse(token);
            if (!(jwt instanceof SignedJWT)) {
                return false;
            }
            List<String> audience = jwt.getJWTClaimsSet().getAudience();
            return audience != null
                    && audience.stream().anyMatch(a -> a.startsWith(DELEGATED_AUDIENCE_PREFIX));
        } catch (ParseException e) {
            return false;
        }
    }

    /**
     * Introspects a worker token against ICMS. Returns the result with
     * {@code active=true} only when ICMS accepted the token and returned its expiry
     * and function binding; otherwise {@code active=false}. Only active results are
     * cached.
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

        if (!result.isActive()) {
            return result;
        }
        if (result.getExp() == null
                || StringUtils.isBlank(result.getFunctionId())
                || StringUtils.isBlank(result.getFunctionVersionId())) {
            log.warn("worker token introspection active but missing exp or function binding, "
                             + "instance_id={}", result.getInstanceId());
            return IcmsStubService.WorkerTokenIntrospectResult.builder()
                    .active(false)
                    .error(ERROR_MISSING_BINDING)
                    .build();
        }
        cache.put(cacheKey, result);
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
