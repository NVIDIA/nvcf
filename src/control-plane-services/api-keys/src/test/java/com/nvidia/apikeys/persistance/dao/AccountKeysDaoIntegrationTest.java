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

package com.nvidia.apikeys.persistance.dao;

import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.config.IntegrationTestConfiguration.KEY_SPACE;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.datastax.driver.core.Row;
import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.AccountKeysSliceVo;
import com.nvidia.apikeys.vo.KeyByAccountOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.Duration;
import java.time.ZoneId;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.function.BiFunction;
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
class AccountKeysDaoIntegrationTest {

    private static final String NCA_1 = "nca-1";
    private static final String NCA_2 = "nca-2";
    private static final String OWNER_1 = "owner-1@example.com";
    private static final String OWNER_2 = "owner-2@example.com";
    private static final String SERVICE_A = "service-a";
    private static final String SERVICE_B = "service-b";
    private static final int MAX_SLICES = 20;

    @Autowired
    private AccountKeysDao dao;

    @Autowired
    private KeysDao keysDao;

    @BeforeEach
    void setUp() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));
    }

    @AfterEach
    void tearDown() {
        TestClock.resetToDefaults();
    }

    @Test
    void saveWritesHashRowAndEncryptedAccountRow() {
        KeyVo key = key(NCA_1, OWNER_1, SERVICE_A, "key-1");

        KeyByAccountOwnerAndServiceVo saved = dao.save(key);

        assertThat(saved).isEqualTo(KeyByAccountOwnerAndServiceVo.from(key));
        assertThat(keysDao.getKeyByHash(key.getKeyHash()))
                .get()
                .satisfies(stored -> {
                    assertThat(stored.getNcaId()).isEqualTo(NCA_1);
                    assertThat(stored.getAuthorizations()).isEqualTo(key.getAuthorizations());
                    assertThat(stored.getAudienceServiceIds())
                            .isEqualTo(key.getAudienceServiceIds());
                });

        Row hashRow = IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT nca_id, status FROM " + KEY_SPACE + ".keys WHERE api_key_hash = ?",
                key.getKeyHash()).one();
        assertThat(hashRow.getString("nca_id")).isEqualTo(NCA_1);
        assertThat(hashRow.getString("status")).isEqualTo(KeyStatus.ACTIVE.name());

        Row accountRow = accountRow(NCA_1, OWNER_1);
        assertThat(accountRow.getString("key_status")).isEqualTo(KeyStatus.ACTIVE.name());
        assertThat(accountRow.getTimestamp("created_at").toInstant()).isEqualTo(TEST_TIME);
        assertThat(accountRow.getString("key_details"))
                .doesNotContain(key.getDescription())
                .doesNotContain(key.getKeyHash())
                .doesNotContain(key.getApiKeySuffix())
                .doesNotContain(key.getAuthorizations());
    }

    @Test
    void saveRequiresNcaId() {
        KeyVo key = key(null, OWNER_1, SERVICE_A, "key-1");

        assertThatThrownBy(() -> dao.save(key))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("nca_id");
        assertThat(keysDao.getKeyByHash(key.getKeyHash())).isEmpty();
    }

    @Test
    void saveTwiceKeepsOneRowAndLatestStatus() {
        KeyVo key = key(NCA_1, OWNER_1, SERVICE_A, "key-1");
        dao.save(key);

        dao.save(key.toBuilder().keyStatus(KeyStatus.SUSPENDED).build());

        assertThat(dao.list(NCA_1, USER, OWNER_1))
                .singleElement()
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyStatus)
                .isEqualTo(KeyStatus.SUSPENDED);
        assertThat(keysDao.getKeyByHash(key.getKeyHash()))
                .get()
                .extracting(KeyVo::getKeyStatus)
                .isEqualTo(KeyStatus.SUSPENDED);
    }

    @Test
    void getMatchesFullPrimaryKeyOnly() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));

        assertThat(dao.get(NCA_1, USER, OWNER_1, SERVICE_A, "key-1")).isPresent();
        assertThat(dao.get(NCA_2, USER, OWNER_1, SERVICE_A, "key-1")).isEmpty();
        assertThat(dao.get(NCA_1, USER, OWNER_2, SERVICE_A, "key-1")).isEmpty();
        assertThat(dao.get(NCA_1, USER, OWNER_1, SERVICE_B, "key-1")).isEmpty();
        assertThat(dao.get(NCA_1, USER, OWNER_1, SERVICE_A, "key-2")).isEmpty();
    }

    @Test
    void listIsScopedToAccountOwnerAndService() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));
        dao.save(key(NCA_1, OWNER_1, SERVICE_B, "key-2"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_A, "key-3"));
        dao.save(key(NCA_2, OWNER_1, SERVICE_A, "key-4"));

        assertThat(dao.list(NCA_1, USER, OWNER_1))
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactlyInAnyOrder("key-1", "key-2");
        assertThat(dao.list(NCA_1, USER, OWNER_1, SERVICE_A))
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactly("key-1");
        assertThat(dao.list(NCA_2, USER, OWNER_1))
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactly("key-4");
        assertThat(dao.list(NCA_2, USER, OWNER_2)).isEmpty();
        assertThat(dao.list(NCA_1, USER, OWNER_1, "unknown-service")).isEmpty();
    }

    @Test
    void listByAccountPagesAcrossOwners() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));
        dao.save(key(NCA_1, OWNER_1, SERVICE_B, "key-2"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_A, "key-3"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_B, "key-4"));
        dao.save(key(NCA_1, "owner-3@example.com", SERVICE_A, "key-5"));
        dao.save(key(NCA_2, OWNER_1, SERVICE_A, "key-6"));

        List<AccountKeysSliceVo> slices = readAll(
                (limit, cursor) -> dao.listByAccount(NCA_1, limit, cursor), 2);

        assertThat(keyIds(slices))
                .containsExactlyInAnyOrder("key-1", "key-2", "key-3", "key-4", "key-5");
        assertThat(slices).hasSizeGreaterThan(1);
        slices.forEach(slice -> assertThat(slice.keys()).hasSizeLessThanOrEqualTo(2));
    }

    @Test
    void listByAccountStopsAtExactSliceBoundary() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));
        dao.save(key(NCA_1, OWNER_1, SERVICE_B, "key-2"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_A, "key-3"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_B, "key-4"));

        List<AccountKeysSliceVo> slices = readAll(
                (limit, cursor) -> dao.listByAccount(NCA_1, limit, cursor), 2);

        assertThat(keyIds(slices))
                .containsExactlyInAnyOrder("key-1", "key-2", "key-3", "key-4");
        assertThat(slices.getLast().cursor()).isNull();
    }

    @Test
    void sliceReportsCursorAndLimitOnlyWhenMoreKeysRemain() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_A, "key-2"));
        dao.save(key(NCA_1, "owner-3@example.com", SERVICE_A, "key-3"));

        AccountKeysSliceVo first = dao.listByAccount(NCA_1, 2, null);
        assertThat(first.keys()).hasSize(2);
        assertThat(first.cursor()).startsWith("0x");
        assertThat(first.limit()).isEqualTo(2);

        AccountKeysSliceVo all = dao.listByAccount(NCA_1, 100, null);
        assertThat(all.keys()).hasSize(3);
        assertThat(all.cursor()).isNull();
        assertThat(all.limit()).isNull();
    }

    @Test
    void listByAccountReturnsEmptySliceForUnknownAccount() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));

        AccountKeysSliceVo slice = dao.listByAccount("unknown-nca", 10, null);

        assertThat(slice.keys()).isEmpty();
        assertThat(slice.cursor()).isNull();
    }

    @Test
    void listByAccountAndServiceFiltersIssuer() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_A, "key-2"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_B, "key-3"));
        dao.save(key(NCA_2, OWNER_1, SERVICE_A, "key-4"));

        AccountKeysSliceVo slice = dao.listByAccountAndService(NCA_1, SERVICE_A, 100, null);

        assertThat(slice.keys())
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactlyInAnyOrder("key-1", "key-2");
        assertThat(slice.cursor()).isNull();
    }

    @Test
    void listByAccountAndServicePagesWithCursor() {
        for (int i = 1; i <= 5; i++) {
            dao.save(key(NCA_1, "owner-" + i + "@example.com", SERVICE_A, "key-a" + i));
            dao.save(key(NCA_1, "owner-" + i + "@example.com", SERVICE_B, "key-b" + i));
        }

        List<AccountKeysSliceVo> slices = readAll(
                (limit, cursor) -> dao.listByAccountAndService(NCA_1, SERVICE_A, limit, cursor),
                2);

        assertThat(keyIds(slices))
                .containsExactlyInAnyOrder("key-a1", "key-a2", "key-a3", "key-a4", "key-a5");
    }

    @ParameterizedTest
    @ValueSource(strings = {"not-hex", "0xzz", "0xdeadbeef"})
    void invalidCursorIsBadRequest(String cursor) {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));

        assertThrowsExceptionWithDetails(
                BadRequestException.class, () -> dao.listByAccount(NCA_1, 10, cursor),
                "Invalid cursor: '" + cursor + "'");
    }

    @Test
    void expiredKeyReadsAsExpired() {
        KeyVo key = key(NCA_1, OWNER_1, SERVICE_A, "key-1").toBuilder()
                .expiresAt(TEST_TIME.minus(Duration.ofDays(1)))
                .build();
        dao.save(key);

        assertThat(dao.get(NCA_1, USER, OWNER_1, SERVICE_A, "key-1"))
                .get()
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyStatus)
                .isEqualTo(KeyStatus.EXPIRED);
        assertThat(dao.listByAccount(NCA_1, 10, null).keys())
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyStatus)
                .containsExactly(KeyStatus.EXPIRED);
        assertThat(accountRow(NCA_1, OWNER_1).getString("key_status"))
                .isEqualTo(KeyStatus.ACTIVE.name());
    }

    @Test
    void suspendedKeyPastExpiryStaysSuspended() {
        KeyVo key = key(NCA_1, OWNER_1, SERVICE_A, "key-1").toBuilder()
                .keyStatus(KeyStatus.SUSPENDED)
                .expiresAt(TEST_TIME.minus(Duration.ofDays(1)))
                .build();
        dao.save(key);

        assertThat(dao.get(NCA_1, USER, OWNER_1, SERVICE_A, "key-1"))
                .get()
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyStatus)
                .isEqualTo(KeyStatus.SUSPENDED);
    }

    @Test
    void deleteRemovesOnlyThatKey() {
        KeyVo key = key(NCA_1, OWNER_1, SERVICE_A, "key-1");
        KeyVo sibling = key(NCA_1, OWNER_1, SERVICE_A, "key-2");
        KeyVo otherAccount = key(NCA_2, OWNER_1, SERVICE_A, "key-1").toBuilder()
                .keyHash("hash-key-1-nca-2")
                .build();
        KeyByAccountOwnerAndServiceVo saved = dao.save(key);
        dao.save(sibling);
        dao.save(otherAccount);

        dao.delete(saved);

        assertThat(dao.get(NCA_1, USER, OWNER_1, SERVICE_A, "key-1")).isEmpty();
        assertThat(keysDao.getKeyByHash(key.getKeyHash())).isEmpty();
        assertThat(dao.get(NCA_1, USER, OWNER_1, SERVICE_A, "key-2")).isPresent();
        assertThat(keysDao.getKeyByHash(sibling.getKeyHash())).isPresent();
        assertThat(dao.get(NCA_2, USER, OWNER_1, SERVICE_A, "key-1")).isPresent();
        assertThat(keysDao.getKeyByHash(otherAccount.getKeyHash())).isPresent();
    }

    @Test
    void deleteOfMissingKeyIsNoOp() {
        KeyVo kept = key(NCA_1, OWNER_1, SERVICE_A, "key-1");
        dao.save(kept);
        KeyByAccountOwnerAndServiceVo missing =
                KeyByAccountOwnerAndServiceVo.from(key(NCA_1, OWNER_1, SERVICE_A, "key-2"));

        dao.delete(missing);
        dao.delete(missing);

        assertThat(dao.list(NCA_1, USER, OWNER_1))
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactly("key-1");
        assertThat(keysDao.getKeyByHash(kept.getKeyHash())).isPresent();
    }

    @Test
    void legacyKeysDaoWritesNoNcaIdAndNoAccountRow() {
        KeyVo key = key(null, OWNER_1, SERVICE_A, "key-1");
        KeyOwnerVo owner = KeyOwnerVo.builder()
                .ownerType(USER)
                .ownerId(OWNER_1)
                .ownerStatus(KeyOwnerStatus.ACTIVE)
                .ownerStatusUpdatedAt(TEST_TIME)
                .build();

        keysDao.save(key, owner);

        assertThat(keysDao.getKeyByHash(key.getKeyHash()))
                .get()
                .satisfies(stored -> {
                    assertThat(stored.getNcaId()).isNull();
                    assertThat(stored.getKeyId()).isEqualTo("key-1");
                });
        assertThat(keysDao.list(USER, OWNER_1))
                .extracting(k -> k.getKeyId())
                .containsExactly("key-1");
        assertThat(IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT COUNT(*) FROM " + KEY_SPACE + ".keys_by_account_owner_and_service")
                           .one().getLong(0)).isZero();
    }

    private static List<AccountKeysSliceVo> readAll(
            BiFunction<Integer, String, AccountKeysSliceVo> query, int limit) {
        List<AccountKeysSliceVo> slices = new ArrayList<>();
        String cursor = null;
        do {
            AccountKeysSliceVo slice = query.apply(limit, cursor);
            slices.add(slice);
            cursor = slice.cursor();
        } while (cursor != null && slices.size() < MAX_SLICES);
        assertThat(cursor).as("paging did not terminate").isNull();
        return slices;
    }

    private static List<String> keyIds(List<AccountKeysSliceVo> slices) {
        return slices.stream()
                .flatMap(slice -> slice.keys().stream())
                .map(KeyByAccountOwnerAndServiceVo::getKeyId)
                .toList();
    }

    private static Row accountRow(String ncaId, String ownerId) {
        return IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT key_status, created_at, key_details FROM " + KEY_SPACE
                        + ".keys_by_account_owner_and_service WHERE nca_id = ? AND owner_type = ?"
                        + " AND owner_id = ?", ncaId, USER.name(), ownerId).one();
    }

    private static KeyVo key(String ncaId, String ownerId, String serviceId, String keyId) {
        return KeyVo.builder()
                .keyStatus(KeyStatus.ACTIVE)
                .ncaId(ncaId)
                .ownerType(USER)
                .ownerId(ownerId)
                .issuerServiceId(serviceId)
                .audienceServiceIds(Set.of(serviceId, "audience-" + keyId))
                .keyId(keyId)
                .keyHash("hash-" + keyId + "-" + ownerId)
                .createdAt(TEST_TIME)
                .expiresAt(TEST_TIME.plus(Duration.ofDays(30)))
                .deletesAt(TEST_TIME.plus(Duration.ofDays(60)))
                .apiKeySuffix("suffix-" + keyId)
                .authorizations("{\"policies\":[\"" + keyId + "\"]}")
                .description("description for " + keyId)
                .build();
    }
}
