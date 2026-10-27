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
import static com.nvidia.apikeys.TestData.INTROSPECTION_REQUEST_1;
import static com.nvidia.apikeys.TestData.KEY_AUTHZ_2;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_ID_2;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_V2_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.config.IntegrationTestConfiguration.KEY_SPACE;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.caching.CachingKeysLoader;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.persistance.dao.OwnerStatusByAccountDao;
import com.nvidia.apikeys.persistance.dao.RowUpdateLockDao;
import com.nvidia.apikeys.persistance.models.KeyV2Model;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.validators.IntrospectionValidator;
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
import java.time.ZoneId;
import java.util.Set;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class KeysV2ServiceIntegrationTest {

    // apikeys.nca-id and the apikeys.service-id-map entry for SERVICE_ID_1 in
    // application-integrationtest.yaml
    private static final String LEGACY_NCA_ID = "test-nca-id";
    private static final String SERVICE_NAME = "nvcf";
    private static final String OTHER_SERVICE_ID = "other-service";
    private static final int MIN_AUTHZ_UPDATE_INTERVAL_SECONDS = 60;

    private static final KeyV2Vo LEGACY_KEY_V2 = KEY_V2_VO_1.toBuilder()
            .ncaId(LEGACY_NCA_ID)
            .issuerServiceId(SERVICE_NAME)
            .audienceServiceIds(Set.of(SERVICE_NAME))
            .build();
    private static final KeyVo LEGACY_KEY_VO_2 = KEY_VO_1.toBuilder()
            .keyId(KEY_ID_2)
            .keyHash("legacy-hash-2")
            .build();

    @Autowired
    private KeysV2Service service;

    @Autowired
    private KeysDao keysDao;

    @Autowired
    private KeysV2Dao keysV2Dao;

    @Autowired
    private OwnerStatusByAccountDao ownerStatusDao;

    @Autowired
    private RowUpdateLockDao rowUpdateLockDao;

    @Autowired
    private CachingKeysLoader cachingKeysLoader;

    @Autowired
    private IntrospectionValidator introspectionValidator;

    @BeforeEach
    void setUp() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));
    }

    @AfterEach
    void tearDown() {
        TestClock.resetToDefaults();
    }

    @Test
    void createKeyWritesKeysV2ForNewAccount() {
        assertThat(createKey(KEY_V2_VO_1)).isEqualTo(KEY_V2_VO_1);

        assertThat(service.getKeyByHash(API_KEY_HASH_1)).contains(KEY_V2_VO_1);
        assertThat(service.getKeyById(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .contains(KEY_V2_VO_1);
        assertThat(service.listKeys(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .extracting(KeysByOwnerAndAccountAndServiceVo::getKeyId)
                .containsExactly(KEY_ID_1);
        assertThat(countRows("keys_v2")).isOne();
        assertThat(countRows("keys")).isZero();
        assertThat(lockExists(KeyV2Model.TABLE_NAME)).isTrue();
        assertThat(lockExists("keys")).isFalse();
    }

    @Test
    void createKeyWritesKeysV2ForLegacyAccount() {
        assertThat(createKey(LEGACY_KEY_V2)).isEqualTo(LEGACY_KEY_V2);

        assertThat(service.getKeyById(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .contains(LEGACY_KEY_V2);
        assertThat(countRows("keys_v2")).isOne();
        assertThat(countRows("keys")).isZero();
        assertThat(keysDao.getKeyByHash(API_KEY_HASH_1)).isEmpty();
        assertThat(lockExists(KeyV2Model.TABLE_NAME)).isTrue();
    }

    @Test
    void createKeyRejectsOwnerSuspendedForNewAccount() {
        ownerStatusDao.saveOwnerStatusByAccount(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, KeyOwnerStatus.SUSPENDED);

        assertThrowsExceptionWithDetails(
                ForbiddenException.class, () -> createKey(KEY_V2_VO_1), "Key owner suspended.");
        assertThat(countRows("keys_v2")).isZero();
    }

    @Test
    void createKeyRejectsOwnerSuspendedForIssuerService() {
        service.updateOwnerStatusByAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.SUSPENDED);

        assertThrowsExceptionWithDetails(
                ForbiddenException.class, () -> createKey(KEY_V2_VO_1), "Key owner suspended.");
        assertThat(countRows("keys_v2")).isZero();
    }

    @Test
    void createKeyRejectsSuspendedLegacyOwnerForLegacyAccount() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        keysDao.save(KEY_OWNER_VO_1.toBuilder().ownerStatus(KeyOwnerStatus.SUSPENDED).build());

        assertThrowsExceptionWithDetails(
                ForbiddenException.class,
                () -> createKey(LEGACY_KEY_V2.toBuilder()
                                             .keyId(KEY_ID_2)
                                             .keyHash("v2-hash-2")
                                             .build()),
                "Key owner suspended.");
        assertThat(countRows("keys_v2")).isZero();
    }

    @Test
    void createKeyRejectsHashAlreadyInLegacyTables() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);

        assertThrowsExceptionWithDetails(
                ConflictException.class, () -> createKey(LEGACY_KEY_V2), "Key already exists");
        assertThat(countRows("keys_v2")).isZero();
        assertThat(service.getKeyByHash(API_KEY_HASH_1)).contains(LEGACY_KEY_V2);
        assertThat(keysDao.getKeyByHash(API_KEY_HASH_1)).contains(KEY_VO_1);
    }

    @Test
    void createKeyRejectsHashAlreadyInKeysV2() {
        createKey(KEY_V2_VO_1);
        var duplicate = KEY_V2_VO_1.toBuilder()
                .keyId(KEY_ID_2)
                .ownerId("other-owner")
                .build();

        assertThrowsExceptionWithDetails(
                ConflictException.class, () -> createKey(duplicate), "Key already exists");
        assertThat(service.getKeyByHash(API_KEY_HASH_1)).contains(KEY_V2_VO_1);
        assertThat(service.listKeys(NCA_ID_1, USER, "other-owner", SERVICE_ID_1)).isEmpty();
        assertThat(countRows("keys_v2")).isOne();
    }

    @Test
    void createKeyRejectsWhenAuthzLockAlreadyHeld() {
        assertThat(rowUpdateLockDao.lock(
                KeyV2Model.TABLE_NAME, KeysService.UPDATE_LOCK_KEY_PREFIX + API_KEY_HASH_1,
                TEST_TIME, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS)).isTrue();

        assertThrowsExceptionWithDetails(
                TooManyRequestsException.class, () -> createKey(KEY_V2_VO_1), "Slow down.");
        assertThat(countRows("keys_v2")).isZero();
    }

    @Test
    void readsFallBackToLegacyTablesForLegacyAccount() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);

        assertThat(service.getKeyByHash(API_KEY_HASH_1)).contains(LEGACY_KEY_V2);
        assertThat(service.getKeyById(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .contains(LEGACY_KEY_V2);
        assertThat(service.listKeys(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .singleElement()
                .satisfies(key -> {
                    assertThat(key.getKeyId()).isEqualTo(KEY_ID_1);
                    assertThat(key.getNcaId()).isEqualTo(LEGACY_NCA_ID);
                    assertThat(key.getIssuerServiceId()).isEqualTo(SERVICE_NAME);
                });
    }

    @Test
    void readsDoNotFallBackToLegacyTablesForOtherAccounts() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);

        assertThat(service.getKeyById(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .isEmpty();
        assertThat(service.listKeys(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME)).isEmpty();
    }

    @Test
    void readsPreferKeysV2ForMigratedKey() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        var migrated = LEGACY_KEY_V2.toBuilder().description("migrated").build();
        keysV2Dao.save(migrated);

        assertThat(service.getKeyByHash(API_KEY_HASH_1)).contains(migrated);
        assertThat(service.getKeyById(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .contains(migrated);
    }

    @Test
    void listKeysReturnsMigratedKeyOnce() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        keysDao.save(LEGACY_KEY_VO_2, KEY_OWNER_VO_1);
        keysV2Dao.save(LEGACY_KEY_V2);

        assertThat(service.listKeys(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .extracting(KeysByOwnerAndAccountAndServiceVo::getKeyId)
                .containsExactly(KEY_ID_1, KEY_ID_2);
    }

    @Test
    void updateKeyStatusOfLegacyKeyWritesLegacyTables() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);

        var updated = service.updateKeyStatus(
                LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1,
                KeyStatus.SUSPENDED);

        assertThat(updated.getKeyStatus()).isEqualTo(KeyStatus.SUSPENDED);
        assertThat(keysDao.getKeyByHash(API_KEY_HASH_1))
                .get()
                .extracting(KeyVo::getKeyStatus)
                .isEqualTo(KeyStatus.SUSPENDED);
        assertThat(keysDao.getKeyByOwnerAndServiceAndId(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1,
                                                        KEY_ID_1))
                .get()
                .extracting(key -> key.getKeyStatus())
                .isEqualTo(KeyStatus.SUSPENDED);
        assertThat(countRows("keys_v2")).isZero();
    }

    @Test
    void revokingMigratedKeyDeniesLegacyIntrospection() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        keysV2Dao.save(LEGACY_KEY_V2);

        service.updateKeyStatus(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1,
                             KeyStatus.REVOKED);

        assertRevokedCopies(false, LEGACY_KEY_V2);
        assertRevokedCopies(true, LEGACY_KEY_V2);
        cachingKeysLoader.invalidate();
        assertThrowsExceptionWithDetails(NotFoundException.class,
                () -> introspectionValidator.validate(INTROSPECTION_REQUEST_1, API_KEY_HASH_1),
                "Key not found");
    }

    @Test
    void updateKeyStatusRejectsChangeFromRevoked() {
        createKey(KEY_V2_VO_1);
        service.updateKeyStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1,
                             KeyStatus.REVOKED);

        assertThrowsExceptionWithDetails(
                ConflictException.class,
                () -> service.updateKeyStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1,
                                           KEY_ID_1, KeyStatus.ACTIVE),
                "Key status cannot change from REVOKED");
        assertThat(service.getKeyById(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .get()
                .extracting(KeyV2Vo::getKeyStatus)
                .isEqualTo(KeyStatus.REVOKED);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void revokeExpiredKeyUpdatesBothStoredCopies(boolean legacy) {
        var expiredAt = TEST_TIME.minusSeconds(1);
        var key = (legacy ? LEGACY_KEY_V2 : KEY_V2_VO_1).toBuilder().expiresAt(expiredAt).build();
        if (legacy) {
            keysDao.save(KEY_VO_1.toBuilder().expiresAt(expiredAt).build(), KEY_OWNER_VO_1);
        } else {
            keysV2Dao.save(key);
        }
        assertThat(service.getKeyById(key.getNcaId(), USER, key.getOwnerId(),
                key.getIssuerServiceId(), key.getKeyId())).get()
                .extracting(KeyV2Vo::getKeyStatus).isEqualTo(KeyStatus.EXPIRED);

        service.updateKeyStatus(key.getNcaId(), USER, key.getOwnerId(), key.getIssuerServiceId(),
                key.getKeyId(), KeyStatus.REVOKED);

        assertRevokedCopies(legacy, key);
        assertThat(service.getKeyByHash(key.getKeyHash())).get()
                .extracting(KeyV2Vo::getExpiresAt).isEqualTo(expiredAt);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void repeatedRevocationRepairsStaleManagementStatus(boolean legacy) {
        var key = (legacy ? LEGACY_KEY_V2 : KEY_V2_VO_1).toBuilder()
                .keyStatus(KeyStatus.REVOKED).build();
        if (legacy) {
            keysDao.save(KEY_VO_1.toBuilder().keyStatus(KeyStatus.REVOKED).build(), KEY_OWNER_VO_1);
            IntegrationTestConfiguration.CQL_SESSION.execute(
                    "UPDATE " + KEY_SPACE + ".keys_by_owner_and_service SET key_status = 'ACTIVE'"
                            + " WHERE owner_type = 'USER' AND owner_id = ? AND issuer_service_id = ? AND key_id = ?",
                    key.getOwnerId(), SERVICE_ID_1, key.getKeyId());
        } else {
            keysV2Dao.save(key);
            IntegrationTestConfiguration.CQL_SESSION.execute(
                    "UPDATE " + KEY_SPACE + ".keys_by_owner_and_account_and_service SET key_status = 'ACTIVE'"
                            + " WHERE nca_id = ? AND owner_type = 'USER' AND owner_id = ?"
                            + " AND issuer_service_id = ? AND key_id = ?",
                    key.getNcaId(), key.getOwnerId(), key.getIssuerServiceId(), key.getKeyId());
        }

        service.updateKeyStatus(key.getNcaId(), USER, key.getOwnerId(), key.getIssuerServiceId(),
                key.getKeyId(), KeyStatus.REVOKED);

        assertRevokedCopies(legacy, key);
    }

    private void assertRevokedCopies(boolean legacy, KeyV2Vo key) {
        if (legacy) {
            assertThat(keysDao.getKeyByHash(key.getKeyHash())).get()
                    .extracting(KeyVo::getKeyStatus).isEqualTo(KeyStatus.REVOKED);
            assertThat(keysDao.getKeyByOwnerAndServiceAndId(USER, key.getOwnerId(), SERVICE_ID_1, key.getKeyId()))
                    .get().extracting(row -> row.getKeyStatus()).isEqualTo(KeyStatus.REVOKED);
        } else {
            assertThat(keysV2Dao.getKeyByHash(key.getKeyHash())).get()
                    .extracting(KeyV2Vo::getKeyStatus).isEqualTo(KeyStatus.REVOKED);
            assertThat(keysV2Dao.get(key.getNcaId(), USER, key.getOwnerId(), key.getIssuerServiceId(), key.getKeyId()))
                    .get().extracting(KeysByOwnerAndAccountAndServiceVo::getKeyStatus).isEqualTo(KeyStatus.REVOKED);
        }
    }

    @Test
    void updateKeyAuthorizationsOfV2KeyLocksKeysV2Row() {
        createKey(KEY_V2_VO_1);
        assertThat(lockExists(KeyV2Model.TABLE_NAME)).isTrue();
        assertThat(lockExists("keys")).isFalse();
        assertThrowsExceptionWithDetails(
                TooManyRequestsException.class,
                () -> service.updateKeyAuthorizations(
                        NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1,
                        KEY_AUTHZ_2, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS),
                "Slow down.");

        deleteLock(KeyV2Model.TABLE_NAME);
        service.updateKeyAuthorizations(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1,
                                     KEY_AUTHZ_2, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS);

        assertThat(keysV2Dao.getKeyByHash(API_KEY_HASH_1))
                .get()
                .extracting(KeyV2Vo::getAuthorizations)
                .isEqualTo(KEY_AUTHZ_2);
        assertThrowsExceptionWithDetails(
                TooManyRequestsException.class,
                () -> service.updateKeyAuthorizations(
                        NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1,
                        KEY_AUTHZ_2, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS),
                "Slow down.");
    }

    @Test
    void updateKeyAuthorizationsOfLegacyKeyLocksLegacyRow() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);

        service.updateKeyAuthorizations(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME,
                                     KEY_ID_1, KEY_AUTHZ_2, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS);

        assertThat(keysDao.getKeyByHash(API_KEY_HASH_1))
                .get()
                .extracting(KeyVo::getAuthorizations)
                .isEqualTo(KEY_AUTHZ_2);
        assertThat(lockExists("keys")).isTrue();
        assertThat(lockExists("keys_v2")).isFalse();
        assertThat(countRows("keys_v2")).isZero();
        assertThrowsExceptionWithDetails(
                TooManyRequestsException.class,
                () -> service.updateKeyAuthorizations(
                        LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1,
                        KEY_AUTHZ_2, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS),
                "Slow down.");
    }

    @Test
    void updateKeyAuthorizationsRejectsInactiveKey() {
        createKey(KEY_V2_VO_1);
        service.updateKeyStatus(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1,
                             KeyStatus.SUSPENDED);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> service.updateKeyAuthorizations(
                        NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1,
                        KEY_AUTHZ_2, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS),
                "Key is not active");
        assertThat(lockExists(KeyV2Model.TABLE_NAME)).isTrue();
    }

    @Test
    void deleteKeyByIdRemovesV2AndLegacyCopies() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        keysV2Dao.save(LEGACY_KEY_V2);

        service.deleteKeyById(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1);

        assertThat(service.getKeyByHash(API_KEY_HASH_1)).isEmpty();
        assertThat(service.getKeyById(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .isEmpty();
        assertThat(service.listKeys(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME)).isEmpty();
        assertThat(keysDao.getKeyByOwnerAndServiceAndId(USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1,
                                                        KEY_ID_1)).isEmpty();
        assertThat(countRows("keys")).isZero();
        assertThat(countRows("keys_v2")).isZero();
    }

    @Test
    void deleteKeyByIdRemovesLegacyOnlyKey() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        keysDao.save(LEGACY_KEY_VO_2, KEY_OWNER_VO_1);

        service.deleteKeyById(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1);

        assertThat(keysDao.getKeyByHash(API_KEY_HASH_1)).isEmpty();
        assertThat(service.listKeys(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .extracting(KeysByOwnerAndAccountAndServiceVo::getKeyId)
                .containsExactly(KEY_ID_2);
    }

    @Test
    void deleteKeyByIdOfMissingKeyThrowsNotFound() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);

        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> service.deleteKeyById(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1),
                "Key not found");
        assertThat(keysDao.getKeyByHash(API_KEY_HASH_1)).isPresent();
    }

    @Test
    void ownerStatusByAccountIsStoredWithLegacyOwner() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);

        var saved = service.updateOwnerStatusByAccount(
                LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, KeyOwnerStatus.SUSPENDED);

        assertThat(saved.getNcaId()).isEqualTo(LEGACY_NCA_ID);
        assertThat(saved.getOwnerStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1))
                .get()
                .extracting(owner -> owner.getOwnerStatus())
                .isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(service.getOwnerStatusByAccount(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1))
                .get()
                .extracting(OwnerStatusByAccountModel::getOwnerStatus)
                .isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(ownerStatusDao.getOwnerStatusByAccount(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1))
                .isEmpty();
        assertThat(service.getOwnerStatusByAccountAndService(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME))
                .isEmpty();
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> service.updateOwnerStatusByAccountAndService(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1,
                                                  SERVICE_NAME, KeyOwnerStatus.SUSPENDED),
                "Owner status for account and service is not supported for account 'test-nca-id'");
    }

    @Test
    void ownerStatusForOtherAccountsUsesOwnerStatusTables() {
        service.updateOwnerStatusByAccount(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, KeyOwnerStatus.SUSPENDED);
        service.updateOwnerStatusByAccountAndService(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1,
                                    KeyOwnerStatus.SUSPENDED);

        assertThat(service.getOwnerStatusByAccount(NCA_ID_1, USER, USER_KEY_OWNER_ID_1))
                .get()
                .extracting(OwnerStatusByAccountModel::getOwnerStatus)
                .isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(service.getOwnerStatusByAccountAndService(NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .get()
                .extracting(OwnerStatusByAccountAndServiceModel::getOwnerStatus)
                .isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(keysDao.getKeyOwner(USER, USER_KEY_OWNER_ID_1)).isEmpty();
    }

    @Test
    void ownerStatusByAccountAndServiceRoundTrip() {
        assertThat(service.getOwnerStatusByAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1)).isEmpty();

        var saved = service.updateOwnerStatusByAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.SUSPENDED);

        assertThat(saved.getNcaId()).isEqualTo(NCA_ID_1);
        assertThat(saved.getOwnerType()).isEqualTo(USER);
        assertThat(saved.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
        assertThat(saved.getIssuerServiceId()).isEqualTo(SERVICE_ID_1);
        assertThat(saved.getOwnerStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(service.getOwnerStatusByAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1))
                .get()
                .extracting(OwnerStatusByAccountAndServiceModel::getOwnerStatus)
                .isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(service.getOwnerStatusByAccount(NCA_ID_1, USER, USER_KEY_OWNER_ID_1)).isEmpty();
        assertThat(countRows("owner_status_by_account")).isZero();
        assertThat(countRows("owner_status_by_account_and_service")).isOne();
    }

    @Test
    void ownerStatusByAccountAndServiceDoesNotAffectOtherIssuers() {
        service.updateOwnerStatusByAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KeyOwnerStatus.SUSPENDED);

        assertThat(service.getOwnerStatusByAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, OTHER_SERVICE_ID)).isEmpty();
        var otherIssuerKey = KEY_V2_VO_1.toBuilder()
                .issuerServiceId(OTHER_SERVICE_ID)
                .audienceServiceIds(Set.of(OTHER_SERVICE_ID))
                .keyId(KEY_ID_2)
                .keyHash("hash-other-issuer")
                .build();
        assertThat(createKey(otherIssuerKey)).isEqualTo(otherIssuerKey);
        assertThrowsExceptionWithDetails(
                ForbiddenException.class, () -> createKey(KEY_V2_VO_1), "Key owner suspended.");
    }

    @Test
    void missingOwnerStatusByAccountAndServiceAllowsCreate() {
        assertThat(service.getOwnerStatusByAccountAndService(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1)).isEmpty();
        assertThat(createKey(KEY_V2_VO_1)).isEqualTo(KEY_V2_VO_1);
    }

    private static long countRows(String table) {
        return IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT COUNT(*) FROM " + KEY_SPACE + "." + table).one().getLong(0);
    }

    private KeyV2Vo createKey(KeyV2Vo key) {
        return service.createKey(key, MIN_AUTHZ_UPDATE_INTERVAL_SECONDS);
    }

    private static boolean lockExists(String table) {
        return IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT updated_at FROM " + KEY_SPACE + ".row_update_lock"
                        + " WHERE table_name = ? AND record_key = ?",
                table, KeysService.UPDATE_LOCK_KEY_PREFIX + API_KEY_HASH_1).one() != null;
    }

    private static void deleteLock(String table) {
        // Use the same conditional-write timestamps as lock acquisition.
        IntegrationTestConfiguration.CQL_SESSION.execute(
                "DELETE FROM " + KEY_SPACE + ".row_update_lock"
                        + " WHERE table_name = ? AND record_key = ? IF EXISTS",
                table, KeysService.UPDATE_LOCK_KEY_PREFIX + API_KEY_HASH_1);
    }
}
