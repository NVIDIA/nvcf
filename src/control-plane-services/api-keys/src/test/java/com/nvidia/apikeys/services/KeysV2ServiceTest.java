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

import static com.nvidia.apikeys.TestData.API_KEY_HASH_1;
import static com.nvidia.apikeys.TestData.KEY_AUTHZ_2;
import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_V2_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.API_KEY;
import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.KEY_OWNER;
import static com.nvidia.apikeys.services.LegacyAccountTranslatorTest.LEGACY_KEY_V2;
import static com.nvidia.apikeys.services.LegacyAccountTranslatorTest.LEGACY_NCA_ID;
import static com.nvidia.apikeys.services.LegacyAccountTranslatorTest.SERVICE_ID_MAP;
import static com.nvidia.apikeys.services.LegacyAccountTranslatorTest.SERVICE_NAME;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.persistance.dao.OwnerStatusByAccountDao;
import com.nvidia.apikeys.persistance.dao.RowUpdateLockDao;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.persistance.models.KeyV2Model;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.services.ActionRecorder.Action;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeysV2ServiceTest {

    private static final int MIN_AUTHZ_UPDATE_INTERVAL_SECONDS = 10;
    private static final KeysByOwnerAndAccountAndServiceVo KEY_V2_ROW_1 =
            KeysByOwnerAndAccountAndServiceVo.from(KEY_V2_VO_1);

    @Mock
    private KeysDao keysDao;
    @Mock
    private KeysV2Dao keysV2Dao;
    @Mock
    private KeyOwnerService keyOwnerService;
    @Mock
    private OwnerStatusByAccountDao ownerStatusDao;
    @Mock
    private RowUpdateLockDao rowUpdateLockDao;
    @Mock
    private MillisecondPrecisionClock clock;
    @Mock
    private ActionRecorder actionRecorder;

    private KeysV2Service service;

    @BeforeEach
    void setUp() {
        var legacyAccount = new LegacyAccountTranslator(
                NakProperties.builder().ncaId(LEGACY_NCA_ID).serviceIdMap(SERVICE_ID_MAP).build());
        service = new KeysV2Service(keysDao, keysV2Dao, keyOwnerService, ownerStatusDao,
                                    rowUpdateLockDao, legacyAccount, clock, actionRecorder);
        lenient().when(keysV2Dao.getKeyByHash(anyString())).thenReturn(Optional.empty());
        lenient().when(keysDao.getKeyByHash(anyString())).thenReturn(Optional.empty());
    }

    @Test
    void createWritesKeysV2ForNewAccount() {
        when(ownerStatusDao.getEffectiveStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(KeyOwnerStatus.ACTIVE);

        assertThat(service.create(KEY_V2_VO_1)).isEqualTo(KEY_V2_VO_1);

        verify(keysV2Dao).save(KEY_V2_VO_1);
        verify(keysDao, never()).save(any(KeyVo.class), any());
        verifyNoInteractions(keyOwnerService);
        verifyKeyAction(KEY_V2_VO_1, Action.CREATE);
    }

    @Test
    void createRejectsSuspendedOwnerForNewAccount() {
        when(ownerStatusDao.getEffectiveStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(KeyOwnerStatus.SUSPENDED);

        assertThrowsExceptionWithDetails(
                ForbiddenException.class, () -> service.create(KEY_V2_VO_1), "Key owner suspended.");

        verify(keysV2Dao, never()).save(any());
        verifyNoInteractions(actionRecorder);
    }

    @Test
    void createWritesKeysV2ForLegacyAccount() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.of(KEY_OWNER_VO_1));

        assertThat(service.create(LEGACY_KEY_V2)).isEqualTo(LEGACY_KEY_V2);

        verify(keysV2Dao).save(LEGACY_KEY_V2);
        verify(keysDao, never()).save(any(KeyVo.class), any());
        verifyNoInteractions(ownerStatusDao, keyOwnerService);
        verifyKeyAction(LEGACY_KEY_V2, Action.CREATE);
    }

    @Test
    void createRejectsSuspendedLegacyOwnerForLegacyAccount() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.of(
                KEY_OWNER_VO_1.toBuilder().ownerStatus(KeyOwnerStatus.SUSPENDED).build()));

        assertThrowsExceptionWithDetails(
                ForbiddenException.class, () -> service.create(LEGACY_KEY_V2), "Key owner suspended.");

        verify(keysV2Dao, never()).save(any());
        verifyNoInteractions(actionRecorder);
    }

    @Test
    void createTreatsUnknownLegacyOwnerAsActiveAndAcceptsUnmappedIssuer() {
        var key = LEGACY_KEY_V2.toBuilder().issuerServiceId("other-service").build();
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.empty());

        assertThat(service.create(key)).isEqualTo(key);

        verify(keysV2Dao).save(key);
    }

    @Test
    void createRejectsHashAlreadyInKeysV2() {
        when(ownerStatusDao.getEffectiveStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(KeyOwnerStatus.ACTIVE);
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(KEY_V2_VO_1));

        var duplicate = KEY_V2_VO_1.toBuilder().keyId(KEY_ID_1 + "-dup").build();
        assertThrowsExceptionWithDetails(
                ConflictException.class, () -> service.create(duplicate), "Key already exists");

        verify(keysV2Dao, never()).save(any());
        verifyNoInteractions(actionRecorder);
    }

    @Test
    void createRejectsHashAlreadyInLegacyTables() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.of(KEY_OWNER_VO_1));
        when(keysDao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(KEY_VO_1));

        assertThrowsExceptionWithDetails(
                ConflictException.class, () -> service.create(LEGACY_KEY_V2), "Key already exists");

        verify(keysV2Dao, never()).save(any());
        verifyNoInteractions(actionRecorder);
    }

    @Test
    void getByHashPrefersKeysV2() {
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(KEY_V2_VO_1));

        assertThat(service.getByHash(API_KEY_HASH_1)).contains(KEY_V2_VO_1);

        verifyNoInteractions(keysDao);
    }

    @Test
    void getByHashFallsBackToLegacyKeys() {
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.empty());
        when(keysDao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(KEY_VO_1));

        assertThat(service.getByHash(API_KEY_HASH_1)).contains(LEGACY_KEY_V2);
    }

    @Test
    void getByHashIsEmptyWhenNeitherTableHasTheKey() {
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.empty());
        when(keysDao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.empty());

        assertThat(service.getByHash(API_KEY_HASH_1)).isEmpty();
    }

    @Test
    void getReadsKeysV2ForNewAccount() {
        stubKeyV2();

        assertThat(service.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .contains(KEY_V2_VO_1);

        verifyNoInteractions(keysDao);
    }

    @Test
    void getIsEmptyWithoutManagementRow() {
        when(keysV2Dao.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.empty());

        assertThat(service.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .isEmpty();

        verify(keysV2Dao, never()).getKeyByHash(anyString());
    }

    @Test
    void getFallsBackToLegacyTablesWithLegacyServiceIdForLegacyAccount() {
        stubLegacyKey();

        assertThat(service.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .contains(LEGACY_KEY_V2);

        verify(keysV2Dao, never()).getKeyByHash(anyString());
    }

    @Test
    void getPrefersKeysV2ForLegacyAccount() {
        when(keysV2Dao.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .thenReturn(Optional.of(KeysByOwnerAndAccountAndServiceVo.from(LEGACY_KEY_V2)));
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(LEGACY_KEY_V2));

        assertThat(service.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .contains(LEGACY_KEY_V2);

        verifyNoInteractions(keysDao);
    }

    @Test
    void getSkipsLegacyTablesForUnmappedIssuerOnLegacyAccount() {
        when(keysV2Dao.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, "other-service", KEY_ID_1))
                .thenReturn(Optional.empty());

        assertThat(service.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, "other-service", KEY_ID_1))
                .isEmpty();

        verifyNoInteractions(keysDao);
    }

    @Test
    void getDoesNotFallBackForNewAccount() {
        when(keysV2Dao.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.empty());

        assertThat(service.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .isEmpty();

        verifyNoInteractions(keysDao);
    }

    @Test
    void listReadsKeysV2ForNewAccount() {
        when(keysV2Dao.list(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(Stream.of(KEY_V2_ROW_1));

        assertThat(service.list(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .containsExactly(KEY_V2_ROW_1);

        verifyNoInteractions(keysDao);
    }

    @Test
    void listCombinesKeysV2AndLegacyTablesForLegacyAccount() {
        var v2Row = KeysByOwnerAndAccountAndServiceVo.from(
                LEGACY_KEY_V2.toBuilder().keyId("v2-key-id").build());
        when(keysV2Dao.list(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .thenReturn(Stream.of(v2Row));
        when(keysDao.list(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(List.of(KEY_BY_OWNER_AND_SERVICE_VO_1));

        assertThat(service.list(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .containsExactly(v2Row, KeysByOwnerAndAccountAndServiceVo.from(LEGACY_KEY_V2));
    }

    @Test
    void listReturnsMigratedKeyOnceFromKeysV2() {
        var migrated = KeysByOwnerAndAccountAndServiceVo.from(
                LEGACY_KEY_V2.toBuilder().keyStatus(KeyStatus.SUSPENDED).build());
        when(keysV2Dao.list(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .thenReturn(Stream.of(migrated));
        when(keysDao.list(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .thenReturn(List.of(KEY_BY_OWNER_AND_SERVICE_VO_1));

        assertThat(service.list(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .containsExactly(migrated);
    }

    @Test
    void listSkipsLegacyTablesForUnmappedIssuerOnLegacyAccount() {
        when(keysV2Dao.list(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, "other-service"))
                .thenReturn(Stream.empty());

        assertThat(service.list(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, "other-service")).isEmpty();

        verifyNoInteractions(keysDao);
    }

    @Test
    void updateStatusSavesKeysV2ForNewAccount() {
        stubKeyV2();
        var suspended = KEY_V2_VO_1.toBuilder().keyStatus(KeyStatus.SUSPENDED).build();

        assertThat(service.updateStatus(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1, KeyStatus.SUSPENDED))
                .isEqualTo(suspended);

        verify(keysV2Dao).save(suspended);
        verifyKeyAction(suspended, Action.SUSPEND);
    }

    @Test
    void updateStatusSavesLegacyTablesForLegacyAccount() {
        stubLegacyKey();
        when(keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);

        var revoked = service.updateStatus(
                LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1, KeyStatus.REVOKED);

        assertThat(revoked).isEqualTo(LEGACY_KEY_V2.toBuilder().keyStatus(KeyStatus.REVOKED).build());
        verify(keysDao).save(KEY_VO_1.toBuilder().keyStatus(KeyStatus.REVOKED).build(), KEY_OWNER_VO_1);
        verify(keysV2Dao, never()).save(any());
        verifyKeyAction(revoked, Action.REVOKE);
    }

    @Test
    void updateStatusSavesKeysV2ForKeysV2KeyOnLegacyAccount() {
        when(keysV2Dao.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .thenReturn(Optional.of(KeysByOwnerAndAccountAndServiceVo.from(LEGACY_KEY_V2)));
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(LEGACY_KEY_V2));
        var suspended = LEGACY_KEY_V2.toBuilder().keyStatus(KeyStatus.SUSPENDED).build();

        service.updateStatus(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1,
                             KeyStatus.SUSPENDED);

        verify(keysV2Dao).save(suspended);
        verifyNoInteractions(keysDao, keyOwnerService);
    }

    @Test
    void updateStatusToCurrentStatusWritesNothing() {
        stubKeyV2();

        assertThat(service.updateStatus(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1, KeyStatus.ACTIVE))
                .isEqualTo(KEY_V2_VO_1);

        verify(keysV2Dao, never()).save(any());
        verifyNoInteractions(actionRecorder);
    }

    @ParameterizedTest
    @EnumSource(value = KeyStatus.class, names = {"EXPIRED", "REVOKED"})
    void updateStatusRejectsFinalStatus(KeyStatus finalStatus) {
        var key = KEY_V2_VO_1.toBuilder().keyStatus(finalStatus).build();
        when(keysV2Dao.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_V2_ROW_1));
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(key));

        assertThrowsExceptionWithDetails(
                ConflictException.class,
                () -> service.updateStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1,
                                           KEY_ID_1, KeyStatus.ACTIVE),
                "Key status cannot change from " + finalStatus);

        verify(keysV2Dao, never()).save(any());
    }

    @Test
    void updateStatusOfMissingKeyIsNotFound() {
        when(keysV2Dao.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.empty());

        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> service.updateStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1,
                                           KEY_ID_1, KeyStatus.SUSPENDED),
                "Key not found");
    }

    @Test
    void updateAuthorizationsLocksKeysV2AndSaves() {
        stubKeyV2();
        when(clock.instant()).thenReturn(TEST_TIME);
        when(rowUpdateLockDao.lock(KeyV2Model.TABLE_NAME, "authz-for-key-hash-" + API_KEY_HASH_1,
                                   TEST_TIME, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS))
                .thenReturn(true);
        var updated = KEY_V2_VO_1.toBuilder().authorizations(KEY_AUTHZ_2).build();

        assertThat(service.updateAuthorizations(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1, KEY_AUTHZ_2,
                MIN_AUTHZ_UPDATE_INTERVAL_SECONDS))
                .isEqualTo(updated);

        verify(keysV2Dao).save(updated);
        verifyKeyAction(updated, Action.UPDATE);
    }

    @Test
    void updateAuthorizationsLocksLegacyKeysForLegacyAccount() {
        stubLegacyKey();
        when(clock.instant()).thenReturn(TEST_TIME);
        when(rowUpdateLockDao.lock(KeyModel.TABLE_NAME, "authz-for-key-hash-" + API_KEY_HASH_1,
                                   TEST_TIME, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS))
                .thenReturn(true);
        when(keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);

        service.updateAuthorizations(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME,
                                     KEY_ID_1, KEY_AUTHZ_2, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS);

        verify(keysDao).save(KEY_VO_1.toBuilder().authorizations(KEY_AUTHZ_2).build(), KEY_OWNER_VO_1);
    }

    @Test
    void updateAuthorizationsWithoutLockIsRejected() {
        stubKeyV2();
        when(clock.instant()).thenReturn(TEST_TIME);
        when(rowUpdateLockDao.lock(anyString(), anyString(), any(), anyInt())).thenReturn(false);

        assertThrowsExceptionWithDetails(
                TooManyRequestsException.class,
                () -> service.updateAuthorizations(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1,
                                                   KEY_ID_1, KEY_AUTHZ_2,
                                                   MIN_AUTHZ_UPDATE_INTERVAL_SECONDS),
                "Slow down.");

        verify(keysV2Dao, never()).save(any());
    }

    @Test
    void updateAuthorizationsRejectsInactiveKey() {
        when(keysV2Dao.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_V2_ROW_1));
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1))
                .thenReturn(Optional.of(KEY_V2_VO_1.toBuilder().keyStatus(KeyStatus.SUSPENDED).build()));

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> service.updateAuthorizations(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1,
                                                   KEY_ID_1, KEY_AUTHZ_2,
                                                   MIN_AUTHZ_UPDATE_INTERVAL_SECONDS),
                "Key is not active");

        verifyNoInteractions(rowUpdateLockDao);
    }

    @Test
    void deleteRemovesKeysV2RowsForNewAccount() {
        when(keysV2Dao.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_V2_ROW_1));

        service.delete(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1);

        verify(keysV2Dao).delete(KEY_V2_ROW_1);
        verifyNoInteractions(keysDao);
        verify(actionRecorder).record(API_KEY, KEY_ID_1, "USER", USER_KEY_OWNER_ID_1, Action.DELETE,
                                      SERVICE_ID_1, Set.of(SERVICE_ID_1));
    }

    @Test
    void deleteRemovesLegacyRowsForLegacyAccount() {
        when(keysV2Dao.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .thenReturn(Optional.empty());
        when(keysDao.getKeyByOwnerAndServiceAndId(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_BY_OWNER_AND_SERVICE_VO_1));

        service.delete(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1);

        verify(keysDao).deleteKey(KEY_BY_OWNER_AND_SERVICE_VO_1);
        verify(keysV2Dao, never()).delete(any());
        verify(actionRecorder).record(API_KEY, KEY_ID_1, "USER", USER_KEY_OWNER_ID_1, Action.DELETE,
                                      SERVICE_NAME, Set.of(SERVICE_NAME));
    }

    @Test
    void deleteRemovesBothCopiesOfMigratedKeyForLegacyAccount() {
        var migrated = KeysByOwnerAndAccountAndServiceVo.from(LEGACY_KEY_V2);
        when(keysV2Dao.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .thenReturn(Optional.of(migrated));
        when(keysDao.getKeyByOwnerAndServiceAndId(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_BY_OWNER_AND_SERVICE_VO_1));

        service.delete(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1);

        verify(keysV2Dao).delete(migrated);
        verify(keysDao).deleteKey(KEY_BY_OWNER_AND_SERVICE_VO_1);
        verifyKeyAction(LEGACY_KEY_V2, Action.DELETE);
    }

    @Test
    void deleteOfMissingKeyIsNotFound() {
        when(keysV2Dao.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.empty());

        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> service.delete(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1),
                "Key not found");

        verify(keysV2Dao, never()).delete(any());
    }

    @Test
    void getOwnerStatusForAccountReadsOwnerStatusTableForNewAccount() {
        var status = ownerStatusForAccount(NCA_ID_1, KeyOwnerStatus.SUSPENDED);
        when(ownerStatusDao.getOwnerStatusForAccount(NCA_ID_1, USER, USER_KEY_OWNER_ID_1))
                .thenReturn(Optional.of(status));

        assertThat(service.getOwnerStatusForAccount(NCA_ID_1, USER, USER_KEY_OWNER_ID_1)).contains(status);

        verifyNoInteractions(keysDao);
    }

    @Test
    void getOwnerStatusForAccountReadsLegacyOwnerForLegacyAccount() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.of(KEY_OWNER_VO_1));

        var status = service.getOwnerStatusForAccount(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1);

        assertThat(status).hasValueSatisfying(value -> {
            assertThat(value.getNcaId()).isEqualTo(LEGACY_NCA_ID);
            assertThat(value.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
            assertThat(value.getOwnerStatus()).isEqualTo(KeyOwnerStatus.ACTIVE);
            assertThat(value.getUpdatedAt()).isEqualTo(TEST_TIME);
        });
        verifyNoInteractions(ownerStatusDao);
    }

    @Test
    void getOwnerStatusForAccountIsEmptyForUnknownLegacyOwner() {
        when(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).thenReturn(Optional.empty());

        assertThat(service.getOwnerStatusForAccount(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1)).isEmpty();
    }

    @Test
    void getOwnerStatusForAccountAndServiceIsEmptyForLegacyAccount() {
        assertThat(service.getOwnerStatusForAccountAndService(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .isEmpty();

        verifyNoInteractions(ownerStatusDao);
    }

    @Test
    void updateOwnerStatusForAccountSavesOwnerStatusTableForNewAccount() {
        var saved = ownerStatusForAccount(NCA_ID_1, KeyOwnerStatus.SUSPENDED);
        when(ownerStatusDao.saveOwnerStatusForAccount(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, KeyOwnerStatus.SUSPENDED))
                .thenReturn(saved);

        assertThat(service.updateOwnerStatusForAccount(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, KeyOwnerStatus.SUSPENDED))
                .isEqualTo(saved);

        verifyNoInteractions(keyOwnerService);
        verify(actionRecorder).record(KEY_OWNER, "N/A", "USER", USER_KEY_OWNER_ID_1,
                                      Action.SUSPENDED, "N/A", Set.of());
    }

    @Test
    void updateOwnerStatusForAccountUpdatesLegacyOwnerForLegacyAccount() {
        var suspended = KEY_OWNER_VO_1.toBuilder().ownerStatus(KeyOwnerStatus.SUSPENDED).build();
        when(keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(USER, USER_KEY_OWNER_ID_1))
                .thenReturn(KEY_OWNER_VO_1);
        when(clock.instant()).thenReturn(TEST_TIME);
        when(keyOwnerService.updateKeyOwner(KEY_OWNER_VO_1, KeyOwnerStatus.SUSPENDED, TEST_TIME))
                .thenReturn(suspended);

        var saved = service.updateOwnerStatusForAccount(
                LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, KeyOwnerStatus.SUSPENDED);

        assertThat(saved.getNcaId()).isEqualTo(LEGACY_NCA_ID);
        assertThat(saved.getOwnerStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
        verifyNoInteractions(ownerStatusDao, actionRecorder);
    }

    @Test
    void updateOwnerStatusForAccountAndServiceSavesOwnerStatusTableForNewAccount() {
        var saved = OwnerStatusByAccountAndServiceModel.builder()
                .ncaId(NCA_ID_1)
                .ownerType(USER)
                .ownerId(USER_KEY_OWNER_ID_1)
                .issuerServiceId(SERVICE_ID_1)
                .ownerStatus(KeyOwnerStatus.ACTIVE)
                .build();
        when(ownerStatusDao.saveOwnerStatusForAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.ACTIVE))
                .thenReturn(saved);

        assertThat(service.updateOwnerStatusForAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.ACTIVE))
                .isEqualTo(saved);

        verify(actionRecorder).record(KEY_OWNER, "N/A", "USER", USER_KEY_OWNER_ID_1,
                                      Action.ACTIVE, SERVICE_ID_1, Set.of());
    }

    @Test
    void updateOwnerStatusForAccountAndServiceIsRejectedForLegacyAccount() {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> service.updateOwnerStatusForAccountAndService(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1,
                                                  SERVICE_NAME, KeyOwnerStatus.SUSPENDED),
                "Owner status for account and service is not supported for account '%s'"
                        .formatted(LEGACY_NCA_ID));

        verifyNoInteractions(ownerStatusDao, keyOwnerService);
    }

    private void stubKeyV2() {
        when(keysV2Dao.get(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_V2_ROW_1));
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(KEY_V2_VO_1));
    }

    private void stubLegacyKey() {
        when(keysV2Dao.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .thenReturn(Optional.empty());
        when(keysDao.getKeyByOwnerAndServiceAndId(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .thenReturn(Optional.of(KEY_BY_OWNER_AND_SERVICE_VO_1));
        when(keysDao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(KEY_VO_1));
    }

    private void verifyKeyAction(KeyV2Vo key, Action action) {
        verify(actionRecorder).record(API_KEY, key.getKeyId(), "USER", key.getOwnerId(), action,
                                      key.getIssuerServiceId(), key.getAudienceServiceIds());
    }

    private static OwnerStatusByAccountModel ownerStatusForAccount(String ncaId, KeyOwnerStatus status) {
        return OwnerStatusByAccountModel.builder()
                .ncaId(ncaId)
                .ownerType(USER)
                .ownerId(USER_KEY_OWNER_ID_1)
                .ownerStatus(status)
                .build();
    }
}
