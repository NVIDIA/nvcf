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

package com.nvidia.apikeys.services;

import static com.nvidia.apikeys.vo.KeyOwnerType.USER;

import com.nvidia.apikeys.caching.CachingKeysLoader;
import com.nvidia.apikeys.caching.IntrospectionLocalCache;
import com.nvidia.apikeys.dto.keysV2.AccountAdminKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOperationResponse;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeysOperationType;
import com.nvidia.apikeys.dto.keysV2.PlatformAdminKeysOperationRequest;
import com.nvidia.apikeys.utils.JsonUtils;
import com.nvidia.apikeys.vo.KeyOperationStatus;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import java.time.Instant;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Service;
import org.springframework.web.context.request.RequestContextHolder;
import org.springframework.web.context.request.ServletRequestAttributes;

/**
 * Processes explicit or wildcard selections synchronously using paged, legacy-aware reads
 * and the existing per-key operations.
 */
@Slf4j
@Service
public class SyncKeysV2OperationsService {

    private final KeysV2Service keys;
    private final ServicesService services;
    private final LegacyAccountTranslator legacyAccount;
    private final MillisecondPrecisionClock clock;
    private final int maxSelectorValues;
    private final KeysV2BulkSelectionService selection;
    private final CachingKeysLoader keyCache;
    private final IntrospectionLocalCache introspectionCache;

    public SyncKeysV2OperationsService(
            KeysV2Service keys, ServicesService services, LegacyAccountTranslator legacyAccount,
            MillisecondPrecisionClock clock,
            @Value("${apikeys.bulk-max-selector-values}") int maxSelectorValues,
            KeysV2BulkSelectionService selection, CachingKeysLoader keyCache,
            IntrospectionLocalCache introspectionCache) {
        if (maxSelectorValues < 1) {
            throw new IllegalArgumentException("max-selector-values must be positive");
        }
        this.keys = keys;
        this.services = services;
        this.legacyAccount = legacyAccount;
        this.clock = clock;
        this.maxSelectorValues = maxSelectorValues;
        this.selection = selection;
        this.keyCache = keyCache;
        this.introspectionCache = introspectionCache;
    }

    public KeyOperationResponse operateAsKeyOwner(
            String userId, KeyOwnerKeysOperationRequest request) {
        return execute(Actor.KEY_OWNER, request.getOperation(), request.getAccounts(),
                List.of(validateId(userId, "userId")), request.getIssuerServices(), null);
    }

    public KeyOperationResponse operateAsAccountAdmin(
            String ncaId, AccountAdminKeysOperationRequest request) {
        return execute(Actor.ACCOUNT_ADMIN, request.getOperation(),
                List.of(validateId(ncaId, "ncaId")), request.getUsers(), request.getIssuerServices(), null);
    }

    public KeyOperationResponse operateAsPlatformAdmin(PlatformAdminKeysOperationRequest request) {
        return execute(Actor.PLATFORM_ADMIN, request.getOperation(), request.getAccounts(),
                request.getUsers(), request.getIssuerServices(),
                request.getConfirmPlatformWide());
    }

    private KeyOperationResponse execute(
            Actor actor, KeysOperationType operation, List<String> accountIds,
            List<String> userIds, List<String> issuerIds, Boolean confirmPlatformWide) {
        var cutoff = clock.instant();
        var actorId = auditActorId();
        if (operation == null) {
            throw new BadRequestException("operation is required");
        }
        var accounts = validateSelection(accountIds, "accounts");
        var users = validateSelection(userIds, "users");
        var issuers = validateSelection(issuerIds, "issuerServices");
        if (actor == Actor.PLATFORM_ADMIN && isWildcard(accounts) && isWildcard(users)
                && isWildcard(issuers) && !Boolean.TRUE.equals(confirmPlatformWide)) {
            throw new BadRequestException("confirm_platform_wide must be true for a platform-wide operation");
        }
        if (!isWildcard(issuers)) {
            for (var issuer : issuers) {
                assertRegisteredIssuer(issuer);
            }
        }
        // Check every selected owner boundary before allowing any changes.
        if (actor == Actor.KEY_OWNER) {
            assertSelectedOwnerScopesActive(accounts, users.getFirst(), issuers, cutoff);
        }
        var operationId = UUID.randomUUID();
        return process(actor, actorId, operation, accounts, users, issuers, cutoff, operationId);
    }

    private KeyOperationResponse process(
            Actor actor, String actorId, KeysOperationType operation, List<String> accounts,
            List<String> users, List<String> issuers, Instant cutoff, UUID operationId) {
        var result = KeyOperationResponse.builder()
                .operationId(operationId).operation(operation).status(KeyOperationStatus.COMPLETED)
                .matchedCount(0L).completedCount(0L).failedCount(0L).submittedAt(cutoff).build();
        boolean mutationAttempted = false;
        try {
            for (var account : accounts) {
                for (var user : users) {
                    for (var issuer : issuers) {
                        try (var selected = select(account, user, issuer, cutoff)) {
                            var iterator = selected.iterator();
                            while (iterator.hasNext()) {
                                var key = iterator.next();
                                assertMatchesScope(key, account, user, issuer);
                                if (key.getCreatedAt() == null) {
                                    throw new IllegalStateException("Selected key has no creation time");
                                }
                                result.setMatchedCount(result.getMatchedCount() + 1);
                                try {
                                    if (actor == Actor.KEY_OWNER) {
                                        assertOwnerActive(key.getNcaId(), key.getOwnerId(), key.getIssuerServiceId());
                                    }
                                    mutationAttempted = true;
                                    switch (operation) {
                                        case REVOKED -> keys.updateKeyStatus(
                                                key.getNcaId(), USER, key.getOwnerId(), key.getIssuerServiceId(),
                                                key.getKeyId(), KeyStatus.REVOKED);
                                        case DELETE -> deleteSelectedKey(key);
                                    }
                                    result.setCompletedCount(result.getCompletedCount() + 1);
                                } catch (RuntimeException failure) {
                                    result.setFailedCount(result.getFailedCount() + 1);
                                    throw failure;
                                }
                            }
                        }
                    }
                }
            }
        } catch (RuntimeException failure) {
            result.setStatus(KeyOperationStatus.FAILED);
            throw new BulkOperationException(result, mutationAttempted, failure);
        } finally {
            if (mutationAttempted) {
                keyCache.invalidate();
                introspectionCache.invalidate();
            }
            result.setUpdatedAt(clock.instant());
            log.info("Synchronous bulk operation finished: {}", JsonUtils.getRequestResponseJsonMapper()
                    .writeValueAsString(Map.of("actor", actor.name(), "actorId", actorId,
                            "accounts", accounts, "users", users, "issuerServices", issuers,
                            "bulkOperation", result)));
        }
        return result;
    }

    private void deleteSelectedKey(KeysByOwnerAndAccountAndServiceVo key) {
        try {
            keys.deleteKeyById(key.getNcaId(), USER, key.getOwnerId(), key.getIssuerServiceId(), key.getKeyId());
        } catch (NotFoundException ignored) {
            // A key deleted after selection already satisfies this operation.
        }
    }

    private Stream<KeysByOwnerAndAccountAndServiceVo> select(
            String account, String user, String issuer, Instant cutoff) {
        return selection.select("*".equals(account) ? null : account,
                "*".equals(user) ? null : user, "*".equals(issuer) ? null : issuer, cutoff);
    }

    private void assertSelectedOwnerScopesActive(
            List<String> accounts, String user, List<String> issuers, Instant cutoff) {
        for (var account : accounts) {
            if (!"*".equals(account) && isWildcard(issuers)) {
                assertOwnerAccountActive(account, user);
            }
            for (var issuer : issuers) {
                if (!"*".equals(account) && !"*".equals(issuer)) {
                    assertOwnerActive(account, user, issuer);
                } else {
                    try (var selected = select(account, user, issuer, cutoff)) {
                        selected.forEach(key -> {
                            assertMatchesScope(key, account, user, issuer);
                            if (key.getCreatedAt() == null) {
                                throw new IllegalStateException("Selected key has no creation time");
                            }
                            assertOwnerActive(key.getNcaId(), user, key.getIssuerServiceId());
                        });
                    }
                }
            }
        }
    }

    private static String auditActorId() {
        if (RequestContextHolder.getRequestAttributes() instanceof ServletRequestAttributes attributes) {
            var request = attributes.getRequest();
            var actorId = request.getHeader("NVCF-ACTOR-ID");
            if (actorId != null) {
                return actorId;
            }
        }
        return ActionRecorder.VALUE_NOT_APPLICABLE;
    }

    private List<String> validateSelection(List<String> selection, String dimension) {
        if (selection == null || selection.isEmpty()) {
            throw new BadRequestException(dimension + " must be nonempty");
        }
        if (selection.size() > maxSelectorValues) {
            throw new BadRequestException(dimension + " exceeds the configured selector limit");
        }
        if (isWildcard(selection)) {
            return List.of("*");
        }
        if (selection.contains("*")) {
            throw new BadRequestException(dimension + " wildcard must be the only value");
        }
        var distinct = new HashSet<String>();
        for (var value : selection) {
            validateId(value, dimension);
            if (!distinct.add(value)) {
                throw new BadRequestException(dimension + " must not contain duplicate IDs");
            }
        }
        return List.copyOf(selection);
    }

    private static boolean isWildcard(List<String> selection) {
        return selection.size() == 1 && "*".equals(selection.getFirst());
    }

    private static String validateId(String value, String dimension) {
        if (value == null || value.isBlank() || "*".equals(value)) {
            throw new BadRequestException(dimension + " contains an invalid ID");
        }
        return value;
    }

    private void assertRegisteredIssuer(String issuer) {
        if (!issuer.equals(legacyAccount.toServiceId(issuer))) {
            throw new BadRequestException("Use canonical issuer service IDs");
        }
        var registered = services.getServiceById(issuer).isPresent()
                || legacyAccount.findLegacyIssuerServiceId(issuer)
                        .flatMap(services::getServiceById).isPresent();
        if (!registered) {
            throw new BadRequestException("Unknown issuer service");
        }
    }

    private void assertOwnerActive(String account, String user, String issuer) {
        assertOwnerAccountActive(account, user);
        var issuerStatus = keys.getOwnerStatusByAccountAndService(account, USER, user, issuer);
        if (issuerStatus.map(status -> status.getOwnerStatus() != KeyOwnerStatus.ACTIVE)
                        .orElse(false)) {
            throw new ForbiddenException("Key owner suspended.");
        }
    }

    private void assertOwnerAccountActive(String account, String user) {
        if (keys.getOwnerStatusByAccount(account, USER, user)
                .map(status -> status.getOwnerStatus() != KeyOwnerStatus.ACTIVE).orElse(false)) {
            throw new ForbiddenException("Key owner suspended.");
        }
    }

    private static void assertMatchesScope(
            KeysByOwnerAndAccountAndServiceVo key, String account, String user, String issuer) {
        if (!matches(account, key.getNcaId()) || key.getOwnerType() != USER
                || !matches(user, key.getOwnerId())
                || !matches(issuer, key.getIssuerServiceId())
                || key.getKeyId() == null || key.getKeyId().isBlank()) {
            throw new IllegalStateException("Selected key does not match the requested scope");
        }
    }

    private static boolean matches(String selector, String value) {
        return value != null && !value.isBlank() && !"*".equals(value)
                && ("*".equals(selector) || Objects.equals(selector, value));
    }

    private enum Actor {
        KEY_OWNER, ACCOUNT_ADMIN, PLATFORM_ADMIN
    }

    public static final class BulkOperationException extends BootResponseException {

        public BulkOperationException(KeyOperationResponse result, boolean mutationAttempted, RuntimeException cause) {
            super(statusFor(cause), mutationAttempted
                    ? "Bulk operation stopped. Some keys may have changed; reconcile state before retrying."
                    : "Bulk operation stopped before any key mutation was attempted.",
                    cause, BulkOperationException.class);
            getBody().setProperty("partialApplication", mutationAttempted);
            getBody().setProperty("bulkOperation", result);
        }

        private static HttpStatus statusFor(RuntimeException cause) {
            if (cause instanceof BootResponseException response) {
                return HttpStatus.valueOf(response.getStatusCode().value());
            }
            return HttpStatus.INTERNAL_SERVER_ERROR;
        }
    }
}
