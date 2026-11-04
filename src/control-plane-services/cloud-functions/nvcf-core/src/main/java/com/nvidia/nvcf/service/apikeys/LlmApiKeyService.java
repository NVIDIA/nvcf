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
package com.nvidia.nvcf.service.apikeys;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.UpstreamException;
import java.time.Duration;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClientRequestException;

// Own cache, separate from ApiKeysService's - a generic-purpose hit must never satisfy an
// LLM-invocation lookup (no rate data) and vice versa.
@Slf4j
@Service
@RequiredArgsConstructor
public class LlmApiKeyService {
    private static final String MESG_RESULT_FROM_BACKUP_CACHE =
            "Returning ApiKeyValidationResult from backup cache as NAK is not reachable - '{}'";
    private static final String MESG_RESULT_NOT_IN_BACKUP_CACHE =
            "NAK is not reachable and ApiKeyValidationResult is not in backup cache anymore - '{}'";
    private static final String MESG_VALIDATION_RESULT = "LLM ApiKey validation result: '{}'";

    private final LlmApiKeyClient llmApiKeyClient;
    private final LoadingCache<String, ApiKeyValidationResult> cache = Caffeine.newBuilder()
            .maximumSize(512).expireAfterWrite(Duration.ofMinutes(1))
            .scheduler(Scheduler.systemScheduler())
            .build(this::fetchApiKeyValidationResult);
    private final Cache<String, ApiKeyValidationResult> backupCache = Caffeine.newBuilder()
            .maximumSize(512).expireAfterWrite(Duration.ofMinutes(60))
            .scheduler(Scheduler.systemScheduler())
            .build();

    private ApiKeyValidationResult fetchApiKeyValidationResult(String apiKey) {
        try {
            var result = llmApiKeyClient.fetchApiKeyValidationResult(apiKey);
            log.debug(MESG_VALIDATION_RESULT, result);
            backupCache.put(apiKey, result);
            return result;
        } catch (WebClientRequestException | UpstreamException ex) {
            return fetchApiKeyValidationResultFromBackupCache(apiKey, ex);
        }
    }

    public ApiKeyValidationResult resolveForLlmInvocation(String apiKey) {
        return cache.get(apiKey);
    }

    @VisibleForTesting
    public void invalidateCache() {
        cache.invalidateAll();
        backupCache.invalidateAll();
    }

    @VisibleForTesting
    public void invalidatePrimaryCache() {
        cache.invalidateAll();
    }

    private ApiKeyValidationResult fetchApiKeyValidationResultFromBackupCache(
            String apiKey,
            RuntimeException ex) {
        var result = backupCache.getIfPresent(apiKey);
        if (result == null) {
            log.error(MESG_RESULT_NOT_IN_BACKUP_CACHE, ex.getMessage());
            throw ex;
        }
        log.info(MESG_RESULT_FROM_BACKUP_CACHE, ex.getMessage());
        return result;
    }
}
