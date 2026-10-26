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

import com.nvidia.apikeys.dto.keysV2.AccountAdminKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeysOperationType;
import com.nvidia.apikeys.dto.keysV2.PlatformAdminKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.SyncKeysOperationResponse;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ForbiddenException;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.time.Instant;
import java.util.HashSet;
import java.util.HexFormat;
import java.util.List;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
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
@ConditionalOnProperty(prefix = "local.sync-bulk-prototype", name = "enabled",
        havingValue = "true")
public class SyncKeysV2OperationsService {

    private final KeysV2Service keys;
    private final ServicesService services;
    private final LegacyAccountTranslator legacyAccount;
    private final MillisecondPrecisionClock clock;
    private final int maxSelectorValues;
    private final KeysV2BulkSelectionService selection;

    public SyncKeysV2OperationsService(
            KeysV2Service keys, ServicesService services, LegacyAccountTranslator legacyAccount,
            MillisecondPrecisionClock clock,
            @Value("${local.sync-bulk-prototype.max-selector-values:10}") int maxSelectorValues,
            KeysV2BulkSelectionService selection) {
        if (maxSelectorValues < 1) {
            throw new IllegalArgumentException("max-selector-values must be positive");
        }
        this.keys = keys;
        this.services = services;
        this.legacyAccount = legacyAccount;
        this.clock = clock;
        this.maxSelectorValues = maxSelectorValues;
        this.selection = selection;
    }

    public SyncKeysOperationResponse operateAsKeyOwner(
            String userId, KeyOwnerKeysOperationRequest request) {
        return execute(Actor.KEY_OWNER, request.getOperation(), request.getAccounts(),
                List.of(validateId(userId, "userId")), request.getIssuerServices(), request.getReason(), null);
    }

    public SyncKeysOperationResponse operateAsAccountAdmin(
            String ncaId, AccountAdminKeysOperationRequest request) {
        return execute(Actor.ACCOUNT_ADMIN, request.getOperation(),
                List.of(validateId(ncaId, "ncaId")), request.getUsers(), request.getIssuerServices(),
                request.getReason(), null);
    }

    public SyncKeysOperationResponse operateAsPlatformAdmin(PlatformAdminKeysOperationRequest request) {
        return execute(Actor.PLATFORM_ADMIN, request.getOperation(), request.getAccounts(),
                request.getUsers(), request.getIssuerServices(), request.getReason(),
                request.getConfirmPlatformWide());
    }

    private SyncKeysOperationResponse execute(
            Actor actor, KeysOperationType operation, List<String> accountIds,
            List<String> userIds, List<String> issuerIds, String reason, Boolean confirmPlatformWide) {
        var cutoff = clock.instant();
        var reasonDigest = validateAndDigestReason(reason);
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
        var correlationId = UUID.randomUUID().toString();
        log.atInfo().addKeyValue("requestId", correlationId).addKeyValue("actor", actor.name())
                .addKeyValue("actorId", actorId).addKeyValue("operation", operation.name())
                .addKeyValue("accounts", accounts).addKeyValue("users", users)
                .addKeyValue("issuerServices", issuers).addKeyValue("reasonDigest", reasonDigest)
                .log("Synchronous bulk operation started");
        return process(actor, operation, accounts, users, issuers, cutoff, correlationId);
    }

    private SyncKeysOperationResponse process(
            Actor actor, KeysOperationType operation, List<String> accounts,
            List<String> users, List<String> issuers, Instant cutoff, String correlationId) {
        long completed = 0;
        boolean mutationAttempted = false;
        Set<KeyIdentity> seen = new HashSet<>();
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
                                if (key.getCreatedAt().isAfter(cutoff)
                                        || !seen.add(new KeyIdentity(key.getNcaId(), key.getOwnerId(),
                                                key.getIssuerServiceId(), key.getKeyId()))) {
                                    continue;
                                }
                                if (actor == Actor.KEY_OWNER) {
                                    assertOwnerActive(key.getNcaId(), key.getOwnerId(), key.getIssuerServiceId());
                                }
                                mutationAttempted = true;
                                switch (operation) {
                                    case REVOKED -> keys.updateKeyStatus(
                                            key.getNcaId(), USER, key.getOwnerId(), key.getIssuerServiceId(),
                                            key.getKeyId(), KeyStatus.REVOKED);
                                    case DELETE -> keys.deleteKeyById(
                                            key.getNcaId(), USER, key.getOwnerId(), key.getIssuerServiceId(),
                                            key.getKeyId());
                                }
                                completed++;
                            }
                        }
                    }
                }
            }
        } catch (RuntimeException failure) {
            throw new BulkOperationException(correlationId, mutationAttempted, failure);
        }
        log.atInfo().addKeyValue("requestId", correlationId)
                .addKeyValue("actor", actor.name()).addKeyValue("operation", operation.name())
                .addKeyValue("completedCount", completed).log("Synchronous bulk operation completed");
        return new SyncKeysOperationResponse(correlationId, operation, completed, cutoff);
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
                            if (!key.getCreatedAt().isAfter(cutoff)) {
                                assertOwnerActive(key.getNcaId(), user, key.getIssuerServiceId());
                            }
                        });
                    }
                }
            }
        }
    }

    private static String validateAndDigestReason(String reason) {
        if (reason == null) {
            return "absent";
        }
        if (reason.isBlank() || reason.length() > 1024
                || reason.chars().anyMatch(Character::isISOControl)) {
            throw new BadRequestException("reason must contain 1 to 1024 non-control characters");
        }
        try {
            return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256")
                    .digest(reason.getBytes(StandardCharsets.UTF_8)));
        } catch (NoSuchAlgorithmException impossible) {
            throw new IllegalStateException("SHA-256 unavailable", impossible);
        }
    }

    private static String auditActorId() {
        if (RequestContextHolder.getRequestAttributes() instanceof ServletRequestAttributes attributes) {
            var request = attributes.getRequest();
            var actorId = request.getHeader("NVCF-ACTOR-ID");
            if (actorId == null) {
                actorId = request.getHeader(ActorResolver.HEADER_KEY_OWNER_ID);
            }
            if (actorId != null) {
                if (actorId.isBlank() || actorId.length() > 256
                        || actorId.chars().anyMatch(Character::isISOControl)) {
                    throw new BadRequestException("Invalid internal actor ID");
                }
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
        if (value == null || value.isBlank() || "*".equals(value) || !value.equals(value.strip())
                || value.chars().anyMatch(Character::isISOControl)) {
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

    private record KeyIdentity(String account, String user, String issuer, String keyId) {
    }

    public static final class BulkOperationException extends BootResponseException {

        public BulkOperationException(String correlationId, boolean mutationAttempted, RuntimeException cause) {
            super(statusFor(cause), mutationAttempted
                    ? "Bulk operation stopped. Some keys may have changed; reconcile state before retrying."
                    : "Bulk operation stopped before any key mutation was attempted.",
                    cause, BulkOperationException.class);
            getBody().setProperty("partialApplication", mutationAttempted);
            getBody().setProperty("correlationId", correlationId);
        }

        private static HttpStatus statusFor(RuntimeException cause) {
            if (cause instanceof BootResponseException response) {
                return HttpStatus.valueOf(response.getStatusCode().value());
            }
            return HttpStatus.INTERNAL_SERVER_ERROR;
        }
    }
}
