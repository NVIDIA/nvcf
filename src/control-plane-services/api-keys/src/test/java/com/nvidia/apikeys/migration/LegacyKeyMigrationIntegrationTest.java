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

package com.nvidia.apikeys.migration;

import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_ID_2;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.config.IntegrationTestConfiguration.KEY_SPACE;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.config.exceptions.ServiceUnavailableException;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.persistance.dao.RowUpdateLockDao;
import com.nvidia.apikeys.services.KeysV2Service;
import com.nvidia.apikeys.services.LegacyKeyMaintenance;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.NotFoundException;
import java.time.ZoneId;
import java.util.Set;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.test.context.ContextConfiguration;

@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class LegacyKeyMigrationIntegrationTest {

    // apikeys.nca-id and the apikeys.service-id-map entry for SERVICE_ID_1 in
    // application-integrationtest.yaml
    private static final String LEGACY_NCA_ID = "test-nca-id";
    private static final String SERVICE_NAME = "nvcf";

    private static final KeyVo LEGACY_KEY_2 = KEY_VO_1.toBuilder()
            .keyId(KEY_ID_2)
            .keyHash("legacy-hash-2")
            .build();
    private static final KeyVo EXPIRED_LEGACY_KEY = KEY_VO_1.toBuilder()
            .keyId("legacy-key-expired")
            .keyHash("legacy-hash-expired")
            .expiresAt(TEST_TIME.minusSeconds(60))
            .build();

    @Autowired
    private LegacyKeyMigrationService migrationService;

    @Autowired
    private LegacyKeyMaintenance maintenance;

    @Autowired
    private KeysV2Service keysV2Service;

    @Autowired
    private KeysDao keysDao;

    @Autowired
    private KeysV2Dao keysV2Dao;

    @Autowired
    private RowUpdateLockDao rowUpdateLockDao;

    @BeforeEach
    void setUp() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));
    }

    @AfterEach
    void tearDown() {
        TestClock.resetToDefaults();
    }

    @Test
    void copiesEveryLegacyKeyAcrossPagesThenStops() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        keysDao.save(LEGACY_KEY_2, KEY_OWNER_VO_1);
        keysDao.save(EXPIRED_LEGACY_KEY, KEY_OWNER_VO_1);

        var first = migrationService.run();

        assertThat(first.outcome()).isEqualTo(RunOutcome.COMPLETED);
        assertThat(first.count(KeyResult.MIGRATED)).isEqualTo(3);
        assertThat(first.isClean()).isFalse();
        assertThat(countRows("keys_v2")).isEqualTo(3);
        assertThat(countRows("keys_by_owner_and_account_and_service")).isEqualTo(3);
        assertThat(keysV2Dao.get(LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .isPresent();
        assertThat(keysV2Dao.getKeyByHash(KEY_VO_1.getKeyHash()))
                .get()
                .extracting(KeyV2Vo::getNcaId, KeyV2Vo::getIssuerServiceId,
                            KeyV2Vo::getAudienceServiceIds)
                .containsExactly(LEGACY_NCA_ID, SERVICE_NAME, Set.of(SERVICE_NAME));
        assertThat(storedV2Status(EXPIRED_LEGACY_KEY.getKeyHash())).isEqualTo(KeyStatus.ACTIVE.name());
        assertThat(rowUpdateLockDao.isLocked(
                LegacyKeyMaintenance.LEASE_TABLE, LegacyKeyMaintenance.LEASE_KEY)).isFalse();

        assertThat(maintenance.isCompleted()).isFalse();

        var second = migrationService.run();

        assertThat(second.count(KeyResult.ALREADY_MIGRATED)).isEqualTo(3);
        assertThat(second.isClean()).isTrue();
        assertThat(maintenance.isCompleted()).isTrue();
        assertThat(completionMarkerTtl()).isNull();

        var third = migrationService.run();

        assertThat(third.outcome()).isEqualTo(RunOutcome.ALREADY_COMPLETED);
        assertThat(third.counts()).isEmpty();
        assertThat(rowUpdateLockDao.isLocked(
                LegacyKeyMaintenance.LEASE_TABLE, LegacyKeyMaintenance.LEASE_KEY)).isFalse();
    }

    @Test
    void legacyKeyWithUnmappedIssuerIsSkippedAndCompletionIsRecorded() {
        keysDao.save(KEY_VO_1.toBuilder().issuerServiceId("unmapped-service").build(), KEY_OWNER_VO_1);

        var result = migrationService.run();

        assertThat(result.count(KeyResult.SKIPPED)).isEqualTo(1);
        assertThat(result.isClean()).isTrue();
        assertThat(maintenance.isCompleted()).isTrue();
        assertThat(countRows("keys_v2")).isZero();
    }

    @Test
    void completionMarkerDoesNotBlockLegacyAccountWrites() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        maintenance.markCompleted(TEST_TIME);

        assertThat(keysV2Service.updateKeyStatus(
                LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1, KeyStatus.SUSPENDED)
                .getKeyStatus()).isEqualTo(KeyStatus.SUSPENDED);
    }

    @Test
    void runIsSkippedWhileAnotherReplicaHoldsTheLease() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        assertThat(maintenance.acquire(TEST_TIME.minusSeconds(1))).isTrue();

        var result = migrationService.run();

        assertThat(result.outcome()).isEqualTo(RunOutcome.LEASE_NOT_ACQUIRED);
        assertThat(countRows("keys_v2")).isZero();
    }

    @Test
    void legacyAccountWritesReturn503DuringMaintenance() {
        keysDao.save(KEY_VO_1, KEY_OWNER_VO_1);
        assertThat(maintenance.acquire(TEST_TIME)).isTrue();

        assertThatThrownBy(() -> keysV2Service.updateKeyStatus(
                LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1, KeyStatus.SUSPENDED))
                .isInstanceOfSatisfying(ServiceUnavailableException.class, e -> {
                    assertThat(e.getStatusCode()).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE);
                    assertThat(e.getHeaders().getFirst(HttpHeaders.RETRY_AFTER)).isNotBlank();
                });
        assertThatThrownBy(() -> keysV2Service.deleteKeyById(
                LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1))
                .isInstanceOf(ServiceUnavailableException.class);
        assertThatThrownBy(() -> keysV2Service.deleteKeyById(
                NCA_ID_1, USER, USER_KEY_OWNER_ID_1, SERVICE_ID_1, KEY_ID_1))
                .isInstanceOf(NotFoundException.class);

        assertThat(maintenance.release(TEST_TIME)).isTrue();

        assertThat(keysV2Service.updateKeyStatus(
                LEGACY_NCA_ID, USER, USER_KEY_OWNER_ID_1, SERVICE_NAME, KEY_ID_1, KeyStatus.SUSPENDED)
                .getKeyStatus()).isEqualTo(KeyStatus.SUSPENDED);
    }

    @Test
    void onlyTheLeaseHolderCanRenewOrReleaseIt() {
        var holder = TEST_TIME;
        var other = TEST_TIME.plusMillis(1);
        assertThat(maintenance.acquire(holder)).isTrue();

        assertThat(maintenance.acquire(other)).isFalse();
        assertThat(maintenance.renew(other)).isFalse();
        assertThat(maintenance.release(other)).isFalse();
        assertThat(maintenance.renew(holder)).isTrue();
        assertThat(maintenance.release(holder)).isTrue();
        assertThat(rowUpdateLockDao.isLocked(
                LegacyKeyMaintenance.LEASE_TABLE, LegacyKeyMaintenance.LEASE_KEY)).isFalse();
    }

    private static long countRows(String table) {
        return IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT COUNT(*) FROM " + KEY_SPACE + "." + table).one().getLong(0);
    }

    private static Integer completionMarkerTtl() {
        var row = IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT TTL(updated_at) FROM " + KEY_SPACE + ".row_update_lock"
                        + " WHERE table_name = ? AND record_key = ?",
                LegacyKeyMaintenance.LEASE_TABLE, LegacyKeyMaintenance.COMPLETED_KEY).one();
        return row.isNull(0) ? null : row.getInt(0);
    }

    private static String storedV2Status(String keyHash) {
        return IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT status FROM " + KEY_SPACE + ".keys_v2 WHERE api_key_hash = ?", keyHash)
                .one().getString(0);
    }
}
