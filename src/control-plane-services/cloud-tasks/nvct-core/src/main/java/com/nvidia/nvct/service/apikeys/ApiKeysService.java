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
package com.nvidia.nvct.service.apikeys;

import static com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.POLICY_RESULT_ATTRIBUTE;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvct.service.apikeys.ApiKeyValidationResult.ApiKeyTaskAccess;
import java.time.Duration;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.security.oauth2.core.OAuth2AuthenticatedPrincipal;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClientRequestException;

@Slf4j
@Service
@RequiredArgsConstructor
public class ApiKeysService {
    private static final String MESG_POLICY_RESULT_FROM_BACKUP_CACHE =
            "Returning ApiKeyValidationResult from backup cache as ApiKeys is not reachable - '{}'";
    private static final String MESG_POLICY_RESULT_NOT_IN_BACKUP_CACHE =
            "ApiKeys is not reachable and ApiKeyValidationResult is not in backup cache - '{}'";
    private static final String MESG_API_KEY_VALIDATION_RESULT =
            "Api Key Validation Result: '{}'";

    private final ApiKeysClient apiKeysClient;
    private final LoadingCache<String, ApiKeyValidationResult> apiKeysCache = Caffeine.newBuilder()
            .maximumSize(512).expireAfterWrite(Duration.ofMinutes(1))
            .scheduler(Scheduler.systemScheduler())
            .build(this::fetchApiKeyValidationResult);
    private final Cache<String, ApiKeyValidationResult> apiKeysBackupCache = Caffeine.newBuilder()
            .maximumSize(512).expireAfterWrite(Duration.ofMinutes(60))
            .scheduler(Scheduler.systemScheduler())
            .build();

    private ApiKeyValidationResult fetchApiKeyValidationResult(String apiKey) {
        try {
            var result = apiKeysClient.fetchApiKeyValidationResult(apiKey);
            log.debug(MESG_API_KEY_VALIDATION_RESULT, result);
            apiKeysBackupCache.put(apiKey, result);
            return result;
        } catch (WebClientRequestException | UpstreamException ex) {
            // WebClientRequestException is thrown when external service(such as Api Keys) is not
            // reachable. NVCT should use the backup cache only when Api Keys is not reachable. For
            // other exceptions, backup cache should not be used.
            return fetchApiKeyValidationResultFromBackupCache(apiKey, ex);
        }
    }

    public ApiKeyValidationResult resolveNCAIdFromApiKey(String apiKey) {
        return apiKeysCache.get(apiKey);
    }

    @VisibleForTesting
    public void invalidateCache() {
        apiKeysCache.invalidateAll();
        apiKeysBackupCache.invalidateAll();
    }

    @VisibleForTesting
    public void invalidatePrimaryCache() {
        apiKeysCache.invalidateAll();
    }

    public static Optional<ApiKeyTaskAccess> isApiKeyAuth(Authentication authentication) {
        if (!(authentication.getPrincipal() instanceof OAuth2AuthenticatedPrincipal principal)) {
            return Optional.empty();
        }
        if (principal.getAttribute(
                ApiKeyValidationResult.TASK_ACCESS_ATTRIBUTE) instanceof ApiKeyTaskAccess access) {
            return Optional.of(access);
        }
        return Optional.empty();
    }

    public Optional<ApiKeyValidationResult> getApiKeyValidationResult(
            Authentication authentication) {
        if (!(authentication.getPrincipal() instanceof OAuth2AuthenticatedPrincipal principal)) {
            return Optional.empty();
        }
        if (principal.getAttribute(
                POLICY_RESULT_ATTRIBUTE) instanceof ApiKeyValidationResult policyResult) {
            return Optional.of(policyResult);
        }
        return Optional.empty();
    }

    private ApiKeyValidationResult fetchApiKeyValidationResultFromBackupCache(
            String apiKey,
            RuntimeException ex) {
        var policyResult = apiKeysBackupCache.getIfPresent(apiKey);
        if (policyResult == null) {
            log.error(MESG_POLICY_RESULT_NOT_IN_BACKUP_CACHE, ex.getMessage());
            throw ex;
        }
        log.info(MESG_POLICY_RESULT_FROM_BACKUP_CACHE, ex.getMessage());
        return policyResult;
    }
}
