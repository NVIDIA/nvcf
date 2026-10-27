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

import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_V2_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.caching.CachingKeysLoader;
import com.nvidia.apikeys.caching.IntrospectionLocalCache;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.dto.keysV2.KeysOperationType;
import com.nvidia.apikeys.dto.keysV2.PlatformAdminKeysOperationRequest;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import java.time.Clock;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(classes = App.class, webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class KeysV2BulkSelectionIntegrationTest {

    @Autowired
    private CachingKeysLoader keyCache;
    @Autowired
    private IntrospectionLocalCache introspectionCache;

    private static final String LEGACY_ACCOUNT = "test-nca-id";
    private static final int PAGE_SIZE = 2;

    @Autowired
    private KeysV2Dao keysV2Dao;
    @Autowired
    private KeysDao keysDao;
    @Autowired
    private KeysV2Service keys;
    @Autowired
    private LegacyAccountTranslator legacyAccount;
    @Autowired
    private ServicesService services;

    @BeforeEach
    void setUpClock() {
        TestClock.setBaseClock(Clock.fixed(TEST_TIME, ZoneOffset.UTC));
    }

    @AfterEach
    void resetClock() {
        TestClock.resetToDefaults();
    }

    @ParameterizedTest
    @ValueSource(ints = {0, 1, 2, 3, 4, 5, 6, 7})
    void selectsEveryWildcardCombinationAcrossPages(int wildcardMask) {
        var seeded = seedV2();
        var account = (wildcardMask & 1) == 0 ? LEGACY_ACCOUNT : null;
        var user = (wildcardMask & 2) == 0 ? "u1" : null;
        var issuer = (wildcardMask & 4) == 0 ? "nvcf" : null;
        var expected = seeded.stream()
                .filter(key -> account == null || account.equals(key.getNcaId()))
                .filter(key -> user == null || user.equals(key.getOwnerId()))
                .filter(key -> issuer == null || issuer.equals(key.getIssuerServiceId()))
                .map(KeyV2Vo::getKeyId).toList();

        try (var selected = selector().select(account, user, issuer, TEST_TIME)) {
            assertThat(selected).extracting(KeysByOwnerAndAccountAndServiceVo::getKeyId)
                    .containsExactlyInAnyOrderElementsOf(expected);
        }
    }

    @Test
    void legacyScanMapsIssuersSkipsStatusOnlyRowsAndPrefersV2Copy() {
        var owner = KEY_OWNER_VO_1.toBuilder().ownerId("u1").build();
        keysDao.save(KEY_VO_1.toBuilder().ownerId("u1").keyId("legacy-only")
                .keyHash("legacy-only-hash").build(), owner);
        keysDao.save(KEY_VO_1.toBuilder().ownerId("u1").keyId("migrated")
                .keyHash("migrated-hash").build(), owner);
        keysDao.save(KEY_VO_1.toBuilder().ownerId("u1").keyId("newer")
                .keyHash("newer-hash").build(), owner);
        keysDao.save(KEY_OWNER_VO_1.toBuilder().ownerId("status-only").build());
        keysV2Dao.save(key(LEGACY_ACCOUNT, "u1", "nvcf", "migrated"));
        keysV2Dao.save(key(LEGACY_ACCOUNT, "u1", "nvcf", "newer").toBuilder()
                .createdAt(TEST_TIME.plusMillis(1)).build());

        try (var selected = selector().select(null, null, null, TEST_TIME)) {
            assertThat(selected).extracting(KeysByOwnerAndAccountAndServiceVo::getKeyId)
                    .containsExactlyInAnyOrder("migrated", "legacy-only");
        }
        try (var selected = selector().select(LEGACY_ACCOUNT, "u1", "nvcf", TEST_TIME)) {
            assertThat(selected).allSatisfy(key -> {
                assertThat(key.getNcaId()).isEqualTo(LEGACY_ACCOUNT);
                assertThat(key.getIssuerServiceId()).isEqualTo("nvcf");
            });
        }
    }

    @ParameterizedTest
    @EnumSource(KeysOperationType.class)
    void mutatesEverySelectedKeyWhileConsumingMultiplePages(KeysOperationType operation) {
        var seeded = seedV2();
        var owner = KEY_OWNER_VO_1.toBuilder().ownerId("u1").build();
        var migrated = seeded.getFirst();
        keysDao.save(KEY_VO_1.toBuilder().ownerId("u1").keyId(migrated.getKeyId())
                .keyHash(migrated.getKeyHash()).build(), owner);
        keysDao.save(KEY_VO_1.toBuilder().ownerId("u1").keyId("legacy-only")
                .keyHash("legacy-only-hash").build(), owner);
        keysDao.save(KEY_VO_1.toBuilder().ownerId("u1").keyId("legacy-later")
                .keyHash("legacy-later-hash").createdAt(TEST_TIME.plusMillis(1)).build(), owner);
        var bulk = new SyncKeysV2OperationsService(keys, services, legacyAccount,
                new MillisecondPrecisionClock(Clock.fixed(TEST_TIME, ZoneOffset.UTC)), 10, selector(),
                keyCache, introspectionCache);

        var response = bulk.operateAsPlatformAdmin(PlatformAdminKeysOperationRequest.builder()
                .operation(operation).accounts(List.of("*")).users(List.of("*"))
                .issuerServices(List.of("*")).confirmPlatformWide(true).build());

        assertThat(response.getCompletedCount()).isEqualTo(seeded.size() + 1);
        for (var key : seeded) {
            var stored = keysV2Dao.getKeyByHash(key.getKeyHash());
            var management = keysV2Dao.get(key.getNcaId(), USER, key.getOwnerId(),
                    key.getIssuerServiceId(), key.getKeyId());
            if (operation == KeysOperationType.DELETE) {
                assertThat(stored).isEmpty();
                assertThat(management).isEmpty();
            } else {
                assertThat(stored).get().extracting(KeyV2Vo::getKeyStatus).isEqualTo(KeyStatus.REVOKED);
                assertThat(management).get().extracting(KeysByOwnerAndAccountAndServiceVo::getKeyStatus)
                        .isEqualTo(KeyStatus.REVOKED);
            }
        }
        var legacy = keysDao.getKeyByHash("legacy-only-hash");
        if (operation == KeysOperationType.DELETE) {
            assertThat(legacy).isEmpty();
            assertThat(keysDao.getKeyByOwnerAndServiceAndId(USER, "u1", SERVICE_ID_1, "legacy-only"))
                    .isEmpty();
            assertThat(keysDao.getKeyByHash(migrated.getKeyHash())).isEmpty();
            assertThat(keysDao.getKeyByOwnerAndServiceAndId(USER, "u1", SERVICE_ID_1, migrated.getKeyId()))
                    .isEmpty();
        } else {
            assertThat(legacy).get().extracting(key -> key.getKeyStatus()).isEqualTo(KeyStatus.REVOKED);
            assertThat(keysDao.getKeyByOwnerAndServiceAndId(USER, "u1", SERVICE_ID_1, "legacy-only"))
                    .get().extracting(key -> key.getKeyStatus()).isEqualTo(KeyStatus.REVOKED);
        }
        assertThat(keysV2Dao.getKeyByHash("future-hash")).get()
                .extracting(KeyV2Vo::getKeyStatus).isEqualTo(KeyStatus.ACTIVE);
        assertThat(keysDao.getKeyByHash("legacy-later-hash")).get()
                .extracting(key -> key.getKeyStatus()).isEqualTo(KeyStatus.ACTIVE);
    }

    private List<KeyV2Vo> seedV2() {
        var seeded = new ArrayList<KeyV2Vo>();
        for (var account : List.of(LEGACY_ACCOUNT, "other-account")) {
            for (var user : List.of("u1", "u2")) {
                for (var issuer : List.of("nvcf", "nvct")) {
                    var key = key(account, user, issuer, account + "-" + user + "-" + issuer);
                    keysV2Dao.save(key);
                    seeded.add(key);
                }
            }
        }
        keysV2Dao.save(key(LEGACY_ACCOUNT, "u1", "nvcf", "future").toBuilder()
                .createdAt(TEST_TIME.plusMillis(1)).build());
        return seeded;
    }

    private KeysV2BulkSelectionService selector() {
        return new KeysV2BulkSelectionService(keysV2Dao, keysDao, legacyAccount, PAGE_SIZE);
    }

    private static KeyV2Vo key(String account, String user, String issuer, String id) {
        return KEY_V2_VO_1.toBuilder().ncaId(account).ownerId(user).issuerServiceId(issuer)
                .audienceServiceIds(Set.of(issuer)).keyId(id).keyHash(id + "-hash").build();
    }
}
