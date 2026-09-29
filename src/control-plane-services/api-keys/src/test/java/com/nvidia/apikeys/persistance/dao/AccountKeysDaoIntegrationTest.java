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
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.datastax.driver.core.Row;
import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.AccountKeysPageVo;
import com.nvidia.apikeys.vo.KeyByAccountOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import java.time.Duration;
import java.time.ZoneId;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
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
                });

        Row row = IntegrationTestConfiguration.CQL_SESSION.execute(
                "SELECT key_status, key_details FROM " + KEY_SPACE
                        + ".keys_by_account_owner_and_service WHERE nca_id = ? AND owner_type = ?"
                        + " AND owner_id = ?", NCA_1, USER.name(), OWNER_1).one();
        assertThat(row.getString("key_status")).isEqualTo(KeyStatus.ACTIVE.name());
        assertThat(row.getString("key_details"))
                .doesNotContain(key.getDescription())
                .doesNotContain(key.getKeyHash());
    }

    @Test
    void saveRequiresNcaId() {
        KeyVo key = key(null, OWNER_1, SERVICE_A, "key-1");

        assertThatThrownBy(() -> dao.save(key))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("nca_id");
    }

    @Test
    void listIsScopedToAccountOwnerAndService() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));
        dao.save(key(NCA_1, OWNER_1, SERVICE_B, "key-2"));
        dao.save(key(NCA_2, OWNER_1, SERVICE_A, "key-3"));

        assertThat(dao.list(NCA_1, USER, OWNER_1))
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactlyInAnyOrder("key-1", "key-2");
        assertThat(dao.list(NCA_1, USER, OWNER_1, SERVICE_A))
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactly("key-1");
        assertThat(dao.list(NCA_2, USER, OWNER_1))
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactly("key-3");
        assertThat(dao.get(NCA_2, USER, OWNER_1, SERVICE_A, "key-1")).isEmpty();
    }

    @Test
    void listByAccountPagesAcrossOwners() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));
        dao.save(key(NCA_1, OWNER_1, SERVICE_B, "key-2"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_A, "key-3"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_B, "key-4"));
        dao.save(key(NCA_1, "owner-3@example.com", SERVICE_A, "key-5"));
        dao.save(key(NCA_2, OWNER_1, SERVICE_A, "key-6"));

        List<String> keyIds = new ArrayList<>();
        String pagingState = null;
        int pages = 0;
        do {
            AccountKeysPageVo page = dao.listByAccount(NCA_1, 2, pagingState);
            assertThat(page.keys()).hasSizeLessThanOrEqualTo(2);
            page.keys().forEach(key -> keyIds.add(key.getKeyId()));
            pagingState = page.nextPagingState();
            pages++;
        } while (pagingState != null && pages < 10);

        assertThat(keyIds).containsExactlyInAnyOrder("key-1", "key-2", "key-3", "key-4", "key-5");
        assertThat(pages).isGreaterThan(1);
    }

    @Test
    void listByAccountAndServiceFiltersIssuer() {
        dao.save(key(NCA_1, OWNER_1, SERVICE_A, "key-1"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_A, "key-2"));
        dao.save(key(NCA_1, OWNER_2, SERVICE_B, "key-3"));
        dao.save(key(NCA_2, OWNER_1, SERVICE_A, "key-4"));

        AccountKeysPageVo page = dao.listByAccountAndService(NCA_1, SERVICE_A, 100, null);

        assertThat(page.keys())
                .extracting(KeyByAccountOwnerAndServiceVo::getKeyId)
                .containsExactlyInAnyOrder("key-1", "key-2");
        assertThat(page.nextPagingState()).isNull();
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
    }

    @Test
    void deleteRemovesHashRowAndAccountRow() {
        KeyVo key = key(NCA_1, OWNER_1, SERVICE_A, "key-1");
        KeyByAccountOwnerAndServiceVo saved = dao.save(key);

        dao.delete(saved);

        assertThat(dao.get(NCA_1, USER, OWNER_1, SERVICE_A, "key-1")).isEmpty();
        assertThat(keysDao.getKeyByHash(key.getKeyHash())).isEmpty();
    }

    private static KeyVo key(String ncaId, String ownerId, String serviceId, String keyId) {
        return KeyVo.builder()
                .keyStatus(KeyStatus.ACTIVE)
                .ncaId(ncaId)
                .ownerType(USER)
                .ownerId(ownerId)
                .issuerServiceId(serviceId)
                .audienceServiceIds(Set.of(serviceId))
                .keyId(keyId)
                .keyHash("hash-" + keyId)
                .createdAt(TEST_TIME)
                .expiresAt(TEST_TIME.plus(Duration.ofDays(30)))
                .deletesAt(TEST_TIME.plus(Duration.ofDays(60)))
                .apiKeySuffix("suffix-" + keyId)
                .authorizations("{\"policies\":[]}")
                .description("description for " + keyId)
                .build();
    }
}
