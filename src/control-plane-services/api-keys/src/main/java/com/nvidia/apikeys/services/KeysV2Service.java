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

import static com.nvidia.apikeys.services.ActionRecorder.Action.CREATE;
import static com.nvidia.apikeys.services.ActionRecorder.Action.DELETE;
import static com.nvidia.apikeys.services.ActionRecorder.Action.REVOKE;
import static com.nvidia.apikeys.services.ActionRecorder.Action.SUSPEND;
import static com.nvidia.apikeys.services.ActionRecorder.Action.UPDATE;
import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.API_KEY;
import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.KEY_OWNER;
import static com.nvidia.apikeys.services.ActionRecorder.VALUE_NOT_APPLICABLE;
import static com.nvidia.apikeys.services.KeysService.ERROR_SLOW_DOWN;
import static com.nvidia.apikeys.services.KeysService.UPDATE_LOCK_KEY_PREFIX;

import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.persistance.dao.OwnerStatusByAccountDao;
import com.nvidia.apikeys.persistance.dao.RowUpdateLockDao;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.models.KeyV2Model;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeysByAccountSliceVo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.apikeys.vo.KeysByScopeFilterVo;
import com.nvidia.apikeys.vo.OwnerAccountStatusVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.TreeSet;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * Key management for the v2 API. New keys always go to keys_v2 through {@link KeysV2Dao}.
 *
 * <p>The configured legacy account also has keys in the legacy tables. Its reads check keys_v2
 * first and fall back to {@link KeysDao}, with service IDs translated by
 * {@link LegacyAccountTranslator}. Updates and deletes go to the table that holds the key. Its
 * owner status stays with the legacy keys through {@link KeyOwnerService}. Every other account
 * uses {@link OwnerStatusByAccountDao} for owner status.
 *
 * <p>Callers pass validated values. Keys are always returned in v2 form.
 */
@Service
@RequiredArgsConstructor
public class KeysV2Service {

    public static final String MESG_KEY_NOT_FOUND = "Key not found";
    static final String MESG_KEY_ALREADY_EXISTS = "Key already exists";
    static final String MESG_KEY_NOT_ACTIVE = "Key is not active";
    static final String MESG_KEY_STATUS_FINAL = "Key status cannot change from %s";
    static final String MESG_OWNER_SUSPENDED = "Key owner suspended.";
    static final String MESG_OWNER_STATUS_REQUIRED = "Owner status is required";
    static final String MESG_OWNER_STATUS_FOR_ACCOUNT_AND_SERVICE_UNSUPPORTED =
            "Owner status for account and service is not supported for account '%s'";

    private static final int ACCOUNT_SCAN_PAGE_SIZE = 100;

    private static final Set<KeyStatus> FINAL_KEY_STATUSES =
            Set.of(KeyStatus.EXPIRED, KeyStatus.REVOKED);

    private final KeysDao keysDao;
    private final KeysV2Dao keysV2Dao;
    private final KeyOwnerService keyOwnerService;
    private final OwnerStatusByAccountDao ownerStatusDao;
    private final RowUpdateLockDao rowUpdateLockDao;
    private final LegacyAccountTranslator legacyAccount;
    private final MillisecondPrecisionClock clock;
    private final ActionRecorder actionRecorder;

    /**
     * Every new key goes to keys_v2, including keys for the legacy account. The hash is unique
     * across keys_v2 and the legacy keys table, so a leftover v1 key cannot be shadowed.
     * Takes the keys_v2 authorization lock before the write, matching v1 create, so an
     * authorization update cannot run until minAuthzUpdateIntervalSeconds has passed.
     */
    public KeyV2Vo createKey(KeyV2Vo key, int minAuthzUpdateIntervalSeconds) {
        return createKey(key, minAuthzUpdateIntervalSeconds, true);
    }

    /**
     * Administrators create keys for suspended owners, so they pass false for
     * requireActiveOwner. The new key stays unusable until the owner is active again.
     */
    public KeyV2Vo createKey(
            KeyV2Vo key, int minAuthzUpdateIntervalSeconds, boolean requireActiveOwner) {
        if (requireActiveOwner) {
            assertOwnerActive(key.getNcaId(), key.getOwnerType(), key.getOwnerId(),
                              key.getIssuerServiceId());
        }
        if (getKeyByHash(key.getKeyHash()).isPresent()) {
            throw new ConflictException(MESG_KEY_ALREADY_EXISTS);
        }
        lockForAuthorizationUpdates(
                KeyV2Model.TABLE_NAME, key.getKeyHash(), minAuthzUpdateIntervalSeconds);
        keysV2Dao.save(key);
        recordKeyAction(key, CREATE);
        return key;
    }

    /**
     * Introspection lookup. Checks keys_v2 first and falls back to the legacy keys table.
     */
    public Optional<KeyV2Vo> getKeyByHash(String keyHash) {
        return keysV2Dao.getKeyByHash(keyHash)
                .or(() -> keysDao.getKeyByHash(keyHash).map(legacyAccount::fromLegacy));
    }

    /**
     * Checks keys_v2 first. For the legacy account, falls back to the legacy tables.
     */
    public Optional<KeyV2Vo> getKeyById(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId) {
        return find(ncaId, ownerType, ownerId, issuerServiceId, keyId).map(StoredKey::key);
    }

    /**
     * Keys in keys_v2, followed by legacy keys when the account is the legacy account. A key
     * already migrated to keys_v2 is listed once, from keys_v2.
     */
    public List<KeysByOwnerAndAccountAndServiceVo> listKeys(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        List<KeysByOwnerAndAccountAndServiceVo> keys;
        try (var v2Keys = keysV2Dao.list(ncaId, ownerType, ownerId, issuerServiceId)) {
            keys = v2Keys.toList();
        }
        if (!legacyAccount.isLegacy(ncaId)) {
            return keys;
        }
        var v2KeyIds = keys.stream()
                .map(KeysByOwnerAndAccountAndServiceVo::getKeyId)
                .collect(Collectors.toSet());
        var legacyKeys = legacyAccount.findLegacyIssuerServiceId(issuerServiceId)
                .map(legacyServiceId -> keysDao.list(ownerType, ownerId, legacyServiceId))
                .orElse(List.of())
                .stream()
                .filter(key -> !v2KeyIds.contains(key.getKeyId()))
                .map(legacyAccount::fromLegacy);
        return Stream.concat(keys.stream(), legacyKeys).toList();
    }

    /**
     * Pages keys_v2 keys matching every non-null filter field. Legacy keys are not included,
     * use {@link #listKeys(String, KeyOwnerType, String, String)} for a fully scoped list.
     */
    public KeysByAccountSliceVo listKeys(KeysByScopeFilterVo filter, int limit, String cursor) {
        return keysV2Dao.list(filter, limit, cursor);
    }

    /**
     * Sets ACTIVE, SUSPENDED, or REVOKED. EXPIRED and REVOKED keys keep their status.
     */
    public KeyV2Vo updateKeyStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId, KeyStatus status) {
        var stored = load(ncaId, ownerType, ownerId, issuerServiceId, keyId);
        var key = stored.key();
        if (key.getKeyStatus() == status) {
            return key;
        }
        if (FINAL_KEY_STATUSES.contains(key.getKeyStatus())) {
            throw new ConflictException(MESG_KEY_STATUS_FINAL.formatted(key.getKeyStatus()));
        }

        var updated = key.toBuilder().keyStatus(status).build();
        save(stored, updated);
        recordKeyAction(updated, switch (status) {
            case SUSPENDED -> SUSPEND;
            case REVOKED -> REVOKE;
            default -> UPDATE;
        });
        return updated;
    }

    public KeyV2Vo updateKeyAuthorizations(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId, String authorizations, int minAuthzUpdateIntervalSeconds) {
        var stored = load(ncaId, ownerType, ownerId, issuerServiceId, keyId);
        var key = stored.key();
        if (key.getKeyStatus() != KeyStatus.ACTIVE) {
            throw new BadRequestException(MESG_KEY_NOT_ACTIVE);
        }

        var tableName = stored.legacy() ? KeyModel.TABLE_NAME : KeyV2Model.TABLE_NAME;
        lockForAuthorizationUpdates(tableName, key.getKeyHash(), minAuthzUpdateIntervalSeconds);

        var updated = key.toBuilder().authorizations(authorizations).build();
        save(stored, updated);
        recordKeyAction(updated, UPDATE);
        return updated;
    }

    /**
     * Removes the key from keys_v2 and, for the legacy account, from the legacy tables, so a key
     * that exists in both while migration runs cannot reappear through the legacy fallback.
     */
    public void deleteKeyById(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId) {
        var v2Key = keysV2Dao.get(ncaId, ownerType, ownerId, issuerServiceId, keyId);
        var legacyKey = findLegacyKeyRow(ncaId, ownerType, ownerId, issuerServiceId, keyId);
        if (v2Key.isEmpty() && legacyKey.isEmpty()) {
            throw new NotFoundException(MESG_KEY_NOT_FOUND);
        }

        v2Key.ifPresent(keysV2Dao::delete);
        legacyKey.ifPresent(keysDao::deleteKey);
        v2Key.ifPresentOrElse(
                key -> recordKeyAction(key, DELETE),
                () -> recordKeyAction(legacyAccount.fromLegacy(legacyKey.get()), DELETE));
    }

    /**
     * Owner status for the account. For the legacy account this is the owner status stored with
     * the owner's legacy keys.
     */
    public Optional<OwnerStatusByAccountModel> getOwnerStatusByAccount(
            String ncaId, KeyOwnerType ownerType, String ownerId) {
        if (legacyAccount.isLegacy(ncaId)) {
            return keysDao.getKeyOwner(ownerType, ownerId)
                    .filter(owner -> owner.getOwnerStatus() != null)
                    .map(owner -> toOwnerStatusByAccount(ncaId, owner));
        }
        return ownerStatusDao.getOwnerStatusByAccount(ncaId, ownerType, ownerId);
    }

    /**
     * The owner's status in every account known to API Keys, ordered by account ID. An account
     * is known when the owner has a key or a stored status in it. An account without a stored
     * account-scoped status is ACTIVE.
     */
    public List<OwnerAccountStatusVo> listOwnerAccountStatuses(
            KeyOwnerType ownerType, String ownerId) {
        var accountStatuses = ownerStatusDao.listOwnerStatusByAccount(ownerType, ownerId).stream()
                .collect(Collectors.toMap(OwnerStatusByAccountModel::getNcaId, s -> s));
        var issuerStatuses = ownerStatusDao.listOwnerStatusByAccountAndService(ownerType, ownerId)
                .stream()
                .collect(Collectors.groupingBy(OwnerStatusByAccountAndServiceModel::getNcaId));

        var ncaIds = new TreeSet<String>();
        ncaIds.addAll(accountStatuses.keySet());
        ncaIds.addAll(issuerStatuses.keySet());
        ncaIds.addAll(listKeyAccountIds(ownerType, ownerId));
        if (keysDao.getKeyOwner(ownerType, ownerId).isPresent()) {
            ncaIds.add(legacyAccount.legacyNcaId());
        }

        return ncaIds.stream()
                .map(ncaId -> OwnerAccountStatusVo.builder()
                        .ncaId(ncaId)
                        .status(accountStatus(ncaId, ownerType, ownerId, accountStatuses))
                        .issuerServices(issuerStatuses.getOrDefault(ncaId, List.of()).stream()
                                                .map(KeysV2Service::toIssuerStatus)
                                                .sorted(Comparator.comparing(
                                                        OwnerAccountStatusVo.IssuerStatus::issuerServiceId))
                                                .toList())
                        .build())
                .toList();
    }

    /**
     * The owner's status in one account, ACTIVE when nothing is stored. Reads only the owner's
     * status records, never keys or other accounts' keys. The legacy account has no
     * account-and-service records.
     */
    public OwnerAccountStatusVo getOwnerAccountStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId) {
        var issuerStatuses = legacyAccount.isLegacy(ncaId)
                ? List.<OwnerStatusByAccountAndServiceModel>of()
                : ownerStatusDao.listOwnerStatusByAccountAndService(ncaId, ownerType, ownerId);
        return OwnerAccountStatusVo.builder()
                .ncaId(ncaId)
                .status(getOwnerStatusByAccount(ncaId, ownerType, ownerId)
                                .map(OwnerStatusByAccountModel::getOwnerStatus)
                                .orElse(KeyOwnerStatus.ACTIVE))
                .issuerServices(issuerStatuses.stream()
                                        .map(KeysV2Service::toIssuerStatus)
                                        .sorted(Comparator.comparing(
                                                OwnerAccountStatusVo.IssuerStatus::issuerServiceId))
                                        .toList())
                .build();
    }

    /**
     * Owner status for the account and issuer service. The legacy account has no
     * account-and-service records.
     */
    public Optional<OwnerStatusByAccountAndServiceModel> getOwnerStatusByAccountAndService(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        if (legacyAccount.isLegacy(ncaId)) {
            return Optional.empty();
        }
        return ownerStatusDao.getOwnerStatusByAccountAndService(
                ncaId, ownerType, ownerId, issuerServiceId);
    }

    public OwnerStatusByAccountModel updateOwnerStatusByAccount(
            String ncaId, KeyOwnerType ownerType, String ownerId, KeyOwnerStatus status) {
        requireOwnerStatus(status);
        if (legacyAccount.isLegacy(ncaId)) {
            var owner = keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(ownerType, ownerId);
            var saved = keyOwnerService.updateKeyOwner(owner, status, clock.instant());
            return toOwnerStatusByAccount(ncaId, saved);
        }
        var saved = ownerStatusDao.saveOwnerStatusByAccount(ncaId, ownerType, ownerId, status);
        recordOwnerAction(ownerType, ownerId, status, VALUE_NOT_APPLICABLE);
        return saved;
    }

    public OwnerStatusByAccountAndServiceModel updateOwnerStatusByAccountAndService(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            KeyOwnerStatus status) {
        requireOwnerStatus(status);
        if (legacyAccount.isLegacy(ncaId)) {
            throw new BadRequestException(
                    MESG_OWNER_STATUS_FOR_ACCOUNT_AND_SERVICE_UNSUPPORTED.formatted(ncaId));
        }
        var saved = ownerStatusDao.saveOwnerStatusByAccountAndService(
                ncaId, ownerType, ownerId, issuerServiceId, status);
        recordOwnerAction(ownerType, ownerId, status, issuerServiceId);
        return saved;
    }

    private StoredKey load(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId) {
        return find(ncaId, ownerType, ownerId, issuerServiceId, keyId)
                .orElseThrow(() -> new NotFoundException(MESG_KEY_NOT_FOUND));
    }

    private Optional<StoredKey> find(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId) {
        var v2Key = keysV2Dao.get(ncaId, ownerType, ownerId, issuerServiceId, keyId);
        if (v2Key.isPresent()) {
            return v2Key.flatMap(row -> keysV2Dao.getKeyByHash(row.getKeyHash()))
                    .map(key -> new StoredKey(key, false));
        }
        return findLegacyKeyRow(ncaId, ownerType, ownerId, issuerServiceId, keyId)
                .flatMap(row -> keysDao.getKeyByHash(row.getKeyHash()))
                .map(key -> new StoredKey(legacyAccount.fromLegacy(key), true));
    }

    private Optional<KeyByOwnerAndServiceVo> findLegacyKeyRow(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId,
            String keyId) {
        if (!legacyAccount.isLegacy(ncaId)) {
            return Optional.empty();
        }
        return legacyAccount.findLegacyIssuerServiceId(issuerServiceId)
                .flatMap(legacyServiceId -> keysDao.getKeyByOwnerAndServiceAndId(
                        ownerType, ownerId, legacyServiceId, keyId));
    }

    private void save(StoredKey stored, KeyV2Vo updated) {
        if (stored.legacy()) {
            var owner = keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(
                    updated.getOwnerType(), updated.getOwnerId());
            keysDao.save(legacyAccount.toLegacy(updated), owner);
        } else {
            keysV2Dao.save(updated);
        }
    }

    private Set<String> listKeyAccountIds(KeyOwnerType ownerType, String ownerId) {
        var filter = KeysByScopeFilterVo.builder().ownerType(ownerType).ownerId(ownerId).build();
        var ncaIds = new TreeSet<String>();
        String cursor = null;
        do {
            var slice = keysV2Dao.list(filter, ACCOUNT_SCAN_PAGE_SIZE, cursor);
            slice.keys().forEach(key -> ncaIds.add(key.getNcaId()));
            cursor = slice.cursor();
        } while (cursor != null);
        return ncaIds;
    }

    private KeyOwnerStatus accountStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId,
            Map<String, OwnerStatusByAccountModel> storedStatuses) {
        if (legacyAccount.isLegacy(ncaId)) {
            return getOwnerStatusByAccount(ncaId, ownerType, ownerId)
                    .map(OwnerStatusByAccountModel::getOwnerStatus)
                    .orElse(KeyOwnerStatus.ACTIVE);
        }
        return Optional.ofNullable(storedStatuses.get(ncaId))
                .map(OwnerStatusByAccountModel::getOwnerStatus)
                .orElse(KeyOwnerStatus.ACTIVE);
    }

    private static OwnerAccountStatusVo.IssuerStatus toIssuerStatus(
            OwnerStatusByAccountAndServiceModel model) {
        return new OwnerAccountStatusVo.IssuerStatus(
                model.getIssuerServiceId(), model.getOwnerStatus(), model.getUpdatedAt());
    }

    /**
     * Denies the operation when the owner is suspended for the account or the issuer service.
     */
    public void assertOwnerActive(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        assertOwnerActive(getEffectiveOwnerStatus(ncaId, ownerType, ownerId, issuerServiceId));
    }

    /**
     * The legacy account keeps owner status with the owner's legacy keys.
     */
    private KeyOwnerStatus getEffectiveOwnerStatus(
            String ncaId, KeyOwnerType ownerType, String ownerId, String issuerServiceId) {
        if (legacyAccount.isLegacy(ncaId)) {
            return keysDao.getKeyOwner(ownerType, ownerId)
                    .map(KeyOwnerVo::getOwnerStatus)
                    .orElse(KeyOwnerStatus.ACTIVE);
        }
        return ownerStatusDao.getEffectiveStatus(ncaId, ownerType, ownerId, issuerServiceId);
    }

    private void lockForAuthorizationUpdates(
            String tableName, String keyHash, int minAuthzUpdateIntervalSeconds) {
        var lockAcquired = rowUpdateLockDao.lock(
                tableName, UPDATE_LOCK_KEY_PREFIX + keyHash, clock.instant(),
                minAuthzUpdateIntervalSeconds);
        if (!lockAcquired) {
            throw new TooManyRequestsException(ERROR_SLOW_DOWN);
        }
    }

    private static void requireOwnerStatus(KeyOwnerStatus status) {
        if (status == null) {
            throw new BadRequestException(MESG_OWNER_STATUS_REQUIRED);
        }
    }

    private static void assertOwnerActive(KeyOwnerStatus status) {
        if (status != KeyOwnerStatus.ACTIVE) {
            throw new ForbiddenException(MESG_OWNER_SUSPENDED);
        }
    }

    private static OwnerStatusByAccountModel toOwnerStatusByAccount(
            String ncaId, KeyOwnerVo owner) {
        return OwnerStatusByAccountModel.builder()
                .ncaId(ncaId)
                .ownerType(owner.getOwnerType())
                .ownerId(owner.getOwnerId())
                .ownerStatus(owner.getOwnerStatus())
                .createdAt(null)
                .updatedAt(owner.getOwnerStatusUpdatedAt())
                .build();
    }

    private void recordKeyAction(KeyV2Vo key, ActionRecorder.Action action) {
        actionRecorder.record(API_KEY, key.getKeyId(), key.getOwnerType().name(), key.getOwnerId(),
                              action, key.getIssuerServiceId(), key.getAudienceServiceIds());
    }

    private void recordKeyAction(KeysByOwnerAndAccountAndServiceVo key, ActionRecorder.Action action) {
        actionRecorder.record(API_KEY, key.getKeyId(), key.getOwnerType().name(), key.getOwnerId(),
                              action, key.getIssuerServiceId(), key.getAudienceServiceIds());
    }

    private void recordOwnerAction(
            KeyOwnerType ownerType, String ownerId, KeyOwnerStatus status, String issuerServiceId) {
        var action = status == KeyOwnerStatus.SUSPENDED
                ? ActionRecorder.Action.SUSPENDED
                : ActionRecorder.Action.ACTIVE;
        actionRecorder.record(KEY_OWNER, VALUE_NOT_APPLICABLE, ownerType.name(), ownerId, action,
                              issuerServiceId, Set.of());
    }

    /**
     * A key in v2 form and whether it is stored in the legacy tables.
     */
    private record StoredKey(KeyV2Vo key, boolean legacy) {
    }
}
