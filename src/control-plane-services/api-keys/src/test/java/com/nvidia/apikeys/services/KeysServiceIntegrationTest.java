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
import static com.nvidia.apikeys.TestData.CREATE_KEY_REQUEST_VO;
import static com.nvidia.apikeys.TestData.DELETE_KEY_BY_ID_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_DELETES_AT_1;
import static com.nvidia.apikeys.TestData.KEY_EXPIRES_AT_1;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_OWNER_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1_AUTHZ_2;
import static com.nvidia.apikeys.TestData.LIST_KEYS_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_MIN_AUTHZ_UPDATE_INTERVAL_SECONDS_1;
import static com.nvidia.apikeys.TestData.SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.SUSPEND_KEYS_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.UPDATE_KEY_REQUEST_VO_1;
import static com.nvidia.apikeys.TestData.UPDATE_KEY_REQUEST_VO_1_AUTHZ_2;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.config.IntegrationTestConfiguration.KEY_SPACE;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyStatus.EXPIRED;
import static com.nvidia.apikeys.vo.KeyStatus.SUSPENDED;
import static java.lang.Math.toIntExact;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import com.datastax.driver.core.Session;
import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.ListKeysRequestVo;
import com.nvidia.apikeys.vo.UpdateKeyRequestVo;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneId;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.assertj.core.data.Offset;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@Slf4j
@ExtendWith(IntegrationTestConfiguration.TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = App.class,
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active:integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class KeysServiceIntegrationTest {

    @Autowired
    private KeysService service;

    @Autowired
    private ValidatingKeyLoader keyLoader;

    @Autowired
    private KeysDao keysDao;

    @AfterEach
    void cleanup() {
        TestClock.resetToDefaults();
    }

    @Test
    void update_shouldStoreKeyInThreeTables() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));
        assertThat(service.createKey(CREATE_KEY_REQUEST_VO))
                .isEqualTo(CREATE_KEY_REQUEST_VO);

        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);

        // verify TTL has been set in all 3 tables
        int expectedTtl = toIntExact(ChronoUnit.SECONDS.between(TEST_TIME, KEY_DELETES_AT_1));

        // verify TTL is set in primary 'keys' table
        var result = session.execute(
                "select ttl(key_details) from keys WHERE api_key_hash='Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos'");
        assertThat(result.one().getInt(0)).isZero();

        // verify TTL is set in reverse lookup table
        result = session.execute(
                "select ttl(key_details) from "
                        + "keys_by_owner_and_service WHERE "
                        + " owner_type='USER' "
                        + " AND owner_id='" + USER_KEY_OWNER_ID_1 + "'"
                        + " AND issuer_service_id='nvidia-cloud-functions-ncp-service-id-aketm'"
                        + " AND key_id='" + KEY_ID_1 + "'");
        assertThat(result.one().getInt(0)).isZero();

        // verify TTL is set on the lock table
        result = session.execute(
                "select ttl(updated_at) from row_update_lock WHERE"
                        + " table_name='keys' "
                        + " and record_key='authz-for-key-hash-Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos' ");
        assertThat(result.one().getInt(0))
                .isCloseTo(SERVICE_MIN_AUTHZ_UPDATE_INTERVAL_SECONDS_1, Offset.offset(1));
    }

    @Test
    void update_usesTtlOfZeroForForeverKeys() {
        Clock testTime = TestClock.fixed(TEST_TIME.minus(800, ChronoUnit.DAYS),
                                         ZoneId.systemDefault());
        TestClock.setBaseClock(testTime);

        assertThat(service.createKey(CREATE_KEY_REQUEST_VO))
                .isEqualTo(CREATE_KEY_REQUEST_VO);

        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);

        // verify TTL is set in primary 'keys' table
        var result = session.execute(
                "select ttl(key_details) from keys WHERE api_key_hash='Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos'");
        assertThat(result.one().getInt(0)).isZero();

        // verify TTL is set in reverse lookup table
        result = session.execute(
                "select ttl(key_details) from "
                        + "keys_by_owner_and_service WHERE "
                        + " owner_type='USER' "
                        + " AND owner_id='" + USER_KEY_OWNER_ID_1 + "'"
                        + " AND issuer_service_id='nvidia-cloud-functions-ncp-service-id-aketm'"
                        + " AND key_id='" + KEY_ID_1 + "'");
        assertThat(result.one().getInt(0)).isZero();

        // verify TTL is set on the lock table
        result = session.execute(
                "select ttl(updated_at) from row_update_lock WHERE"
                        + " table_name='keys' "
                        + " and record_key='authz-for-key-hash-Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos' ");
        assertThat(result.one().getInt(0)).isCloseTo(SERVICE_MIN_AUTHZ_UPDATE_INTERVAL_SECONDS_1,
                                                     Offset.offset(1));
    }

    @Test
    void read_shouldBeAbleToReadCreatedKeys() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        assertThat(service.createKey(CREATE_KEY_REQUEST_VO))
                .isEqualTo(CREATE_KEY_REQUEST_VO);

        // read by hash
        assertThat(keyLoader.loadKeyByHash(API_KEY_HASH_1)).isEqualTo(KEY_VO_1);
        // read by id
        assertThat(keyLoader.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1)).isEqualTo(KEY_VO_1);
        // read by listing
        assertThat(service.listKeys(LIST_KEYS_REQUEST_VO_1))
                .isEqualTo(List.of(KEY_BY_OWNER_AND_SERVICE_VO_1));
    }

    @Test
    void list_shouldReturnEmptyListIfNoKeys() {
        assertThat(service.listKeys(LIST_KEYS_REQUEST_VO_1))
                .isEqualTo(List.of());
    }

    @Test
    void update_shouldFailToUpdateTooOften() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        // first call creates records
        assertThat(service.createKey(CREATE_KEY_REQUEST_VO))
                .isEqualTo(CREATE_KEY_REQUEST_VO);

        //  should be able to update without lock if no authorization change
        assertThat(service.updateKey(UPDATE_KEY_REQUEST_VO_1)).isEqualTo(KEY_VO_1);
        assertThat(keyLoader.loadKeyByHash(API_KEY_HASH_1)).isEqualTo(KEY_VO_1);
        assertThat(keyLoader.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1)).isEqualTo(KEY_VO_1);

        // should not be able to update authorization until lock expires
        assertThrowsExceptionWithDetails(
                TooManyRequestsException.class,
                () -> service.updateKey(UPDATE_KEY_REQUEST_VO_1_AUTHZ_2), "Slow down.");

        // delete lock
        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);

        session.execute(
                "delete from row_update_lock WHERE"
                        + " table_name='keys' "
                        + " and record_key='authz-for-key-hash-Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos' ");

        // should update when lock expired
        assertThat(service.updateKey(UPDATE_KEY_REQUEST_VO_1_AUTHZ_2))
                .isEqualTo(KEY_VO_1_AUTHZ_2);

        assertThat(keyLoader.loadKeyByHash(API_KEY_HASH_1)).isEqualTo(KEY_VO_1_AUTHZ_2);
        assertThat(keyLoader.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1))
                .isEqualTo(KEY_VO_1_AUTHZ_2);
    }

    @Test
    void update_shouldSetTtlOfZeroForForeverKeys() {
        Instant testTime = TEST_TIME.minus(800, ChronoUnit.DAYS);
        TestClock.setBaseClock(TestClock.fixed(testTime, ZoneId.systemDefault()));

        UpdateKeyRequestVo updateKeyRequestVo = UpdateKeyRequestVo.builder()
                .key(KEY_VO_1)
                .keyOwner(KEY_OWNER_VO_1)
                .service(SERVICE_VO_1.toBuilder()
                                 .maxApiKeyTtlDays(800)
                                 .build())
                .build();
        ;

        assertThat(service.updateKey(updateKeyRequestVo)).isEqualTo(KEY_VO_1);

        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);

        // verify TTL is set in primary 'keys' table
        var result = session.execute(
                "select ttl(key_details) from keys WHERE api_key_hash='Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos'");
        assertThat(result.one().getInt(0)).isZero();

        // verify TTL is set in reverse lookup table
        result = session.execute(
                "select ttl(key_details) from "
                        + "keys_by_owner_and_service WHERE "
                        + " owner_type='USER' "
                        + " AND owner_id='" + USER_KEY_OWNER_ID_1 + "'"
                        + " AND issuer_service_id='nvidia-cloud-functions-ncp-service-id-aketm'"
                        + " AND key_id='" + KEY_ID_1 + "'");
        assertThat(result.one().getInt(0)).isZero();
    }

    @Test
    void read_shouldThrowIfKeyDoesNotExist() {
        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> keyLoader.loadKeyByHash(API_KEY_HASH_1),
                "Key not found");
        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> keyLoader.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1),
                "Key not found");
    }

    @Test
    void delete_shouldBeAbleToDeleteKey() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        assertThat(service.createKey(CREATE_KEY_REQUEST_VO))
                .isEqualTo(CREATE_KEY_REQUEST_VO);
        assertThat(keyLoader.loadKeyByHash(API_KEY_HASH_1)).isEqualTo(KEY_VO_1);
        assertThat(keyLoader.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1)).isEqualTo(KEY_VO_1);

        assertDoesNotThrow(() -> service.deleteKeyById(DELETE_KEY_BY_ID_REQUEST_VO_1));

        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> keyLoader.loadKeyByHash(API_KEY_HASH_1),
                "Key not found");
        assertThrowsExceptionWithDetails(
                NotFoundException.class,
                () -> keyLoader.loadKeyVo(KEY_OWNER_VO_1, SERVICE_ID_1, KEY_ID_1),
                "Key not found");
    }

    @Test
    void suspendKeys_returnsEmptyListWhenNoKeys() {
        assertThat(service.suspendKeys(SUSPEND_KEYS_REQUEST_VO_1)).isEqualTo(List.of());
    }

    @Test
    void suspendKeys_suspendsActiveKeys() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        service.createKey(CREATE_KEY_REQUEST_VO);
        var key = KEY_BY_OWNER_AND_SERVICE_VO_1.toBuilder()
                .keyStatus(SUSPENDED)
                .build();

        // should suspend keys
        assertThat(service.suspendKeys(SUSPEND_KEYS_REQUEST_VO_1))
                .isNotEmpty()
                .containsOnly(key);

        // verify by hash
        assertThat(keyLoader.getKeyByHashIfExists(API_KEY_HASH_1).get().getKeyStatus())
                .isEqualTo(SUSPENDED);

        // should return same result if repeated
        assertThat(service.suspendKeys(SUSPEND_KEYS_REQUEST_VO_1))
                .isNotEmpty()
                .containsOnly(key);

        // should return new status in reverse lookup
        assertThat(service.listKeys(new ListKeysRequestVo(KEY_OWNER_VO_1, SERVICE_VO_1))
                           .get(0).getKeyStatus()).isEqualTo(SUSPENDED);

        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);

        // verify TTL is not set in primary 'keys' table
        var result = session.execute(
                "select ttl(key_details) from keys WHERE api_key_hash='Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos'");
        assertThat(result.one().getInt(0)).isZero();

        // verify TTL is not set in reverse lookup table
        result = session.execute(
                "select ttl(key_details) from "
                        + "keys_by_owner_and_service WHERE "
                        + " owner_type='USER' "
                        + " AND owner_id='" + USER_KEY_OWNER_ID_1 + "'"
                        + " AND issuer_service_id='nvidia-cloud-functions-ncp-service-id-aketm'"
                        + " AND key_id='" + KEY_ID_1 + "'");
        assertThat(result.one().getInt(0)).isZero();
    }

    @Test
    void suspendKeys_suspendsActiveForeverKeysWithoutTtl() {
        // test is happening 100 years before the expiration date
        Instant testTime = TEST_TIME.minus(100 * 365, ChronoUnit.DAYS);
        TestClock.setBaseClock(TestClock.fixed(testTime, ZoneId.systemDefault()));

        service.createKey(CREATE_KEY_REQUEST_VO);
        var key = KEY_BY_OWNER_AND_SERVICE_VO_1.toBuilder()
                .keyStatus(SUSPENDED)
                .build();

        // should suspend keys
        assertThat(service.suspendKeys(SUSPEND_KEYS_REQUEST_VO_1)).containsOnly(key);

        // verify by hash
        assertThat(keyLoader.getKeyByHashIfExists(API_KEY_HASH_1).get().getKeyStatus()).isEqualTo(SUSPENDED);

        // should return same result if repeated
        assertThat(service.suspendKeys(SUSPEND_KEYS_REQUEST_VO_1)).containsOnly(key);

        // should return new status in reverse lookup
        assertThat(service.listKeys(new ListKeysRequestVo(KEY_OWNER_VO_1, SERVICE_VO_1))
                           .get(0).getKeyStatus()).isEqualTo(SUSPENDED);

        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);

        // verify TTL is ZERO in primary 'keys' table
        var result = session.execute(
                "select ttl(key_details) from keys WHERE api_key_hash='Q82nHmgIZ9Fm3DGgLxMMZl_J5E6HPkk9tWCeX7EVxos'");
        assertThat(result.one().getInt(0)).isZero();

        // verify TTL is ZERO in reverse lookup table
        result = session.execute(
                "select ttl(key_details) from "
                        + "keys_by_owner_and_service WHERE "
                        + " owner_type='USER' "
                        + " AND owner_id='" + USER_KEY_OWNER_ID_1 + "'"
                        + " AND issuer_service_id='nvidia-cloud-functions-ncp-service-id-aketm'"
                        + " AND key_id='" + KEY_ID_1 + "'");
        assertThat(result.one().getInt(0)).isZero();
    }

    @Test
    void suspendKeys_skipsExpiredKeys() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        service.createKey(CREATE_KEY_REQUEST_VO);

        TestClock.setBaseClock(TestClock.fixed(
                KEY_EXPIRES_AT_1.plusSeconds(1), ZoneId.systemDefault()));

        var key = KEY_BY_OWNER_AND_SERVICE_VO_1.toBuilder()
                .keyStatus(EXPIRED)
                .build();

        // should skip EXPIRED keys
        assertThat(service.suspendKeys(SUSPEND_KEYS_REQUEST_VO_1))
                .isNotEmpty()
                .containsOnly(key);

        // verify by hash
        assertThat(keyLoader.getKeyByHashIfExists(API_KEY_HASH_1).get().getKeyStatus())
                .isEqualTo(EXPIRED);

        // should return same result if repeated
        assertThat(service.suspendKeys(SUSPEND_KEYS_REQUEST_VO_1))
                .isNotEmpty()
                .containsOnly(key);

        // should return new status in reverse lookup
        assertThat(service.listKeys(new ListKeysRequestVo(KEY_OWNER_VO_1, SERVICE_VO_1))
                           .get(0).getKeyStatus()).isEqualTo(EXPIRED);
    }

    @Test
    void listKeys() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        // returns empty when no keys
        assertThat(service.listKeys(KEY_OWNER_VO_1)).isEmpty();

        // returns key when exist
        service.createKey(CREATE_KEY_REQUEST_VO);
        assertThat(service.listKeys(KEY_OWNER_VO_1)).containsOnly(KEY_BY_OWNER_AND_SERVICE_VO_1);

        // tolerates when user record has no keys
        service.deleteKeyById(DELETE_KEY_BY_ID_REQUEST_VO_1);
        assertThat(service.listKeys(KEY_OWNER_VO_1)).isEmpty();
    }

    @Test
    void deleteKeys_nothingIfNoKeys() {
        assertThat(service.listKeys(KEY_OWNER_VO_1)).isEmpty();

        assertDoesNotThrow(() -> service.deleteKeys(KEY_OWNER_VO_1));

        assertThat(service.listKeys(KEY_OWNER_VO_1)).isEmpty();
    }

    @Test
    void deleteKeys_deletesUserKeys() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        service.createKey(CREATE_KEY_REQUEST_VO);
        assertThat(service.listKeys(KEY_OWNER_VO_1)).containsOnly(KEY_BY_OWNER_AND_SERVICE_VO_1);

        service.deleteKeys(KEY_OWNER_VO_1);

        assertThat(service.listKeys(KEY_OWNER_VO_1)).isEmpty();
    }

    @Test
    void shouldUpdateKeyOwnerStatus() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        service.createKey(CREATE_KEY_REQUEST_VO);
        assertThat(service.listKeys(KEY_OWNER_VO_1)).containsOnly(KEY_BY_OWNER_AND_SERVICE_VO_1);

        Optional<KeyOwnerVo> keyOwner = keysDao.getKeyOwner(
                KeyOwnerType.USER, USER_KEY_OWNER_ID_1);
        assertThat(keyOwner).contains(KEY_OWNER_VO_1);

        Instant suspendedAt = TEST_TIME.plusSeconds(10);
        TestClock.setBaseClock(TestClock.fixed(suspendedAt, ZoneId.systemDefault()));
        KeyOwnerVo suspendedOwner = KEY_OWNER_VO_1.toBuilder()
                .ownerStatus(KeyOwnerStatus.SUSPENDED)
                .ownerStatusUpdatedAt(suspendedAt)
                .build();

        assertThat(keysDao.save(suspendedOwner)).isEqualTo(suspendedOwner);

        Instant reactivatedAt = TEST_TIME.plusSeconds(20);
        TestClock.setBaseClock(TestClock.fixed(suspendedAt, ZoneId.systemDefault()));
        KeyOwnerVo reactivatedOwner = KEY_OWNER_VO_1.toBuilder()
                .ownerStatus(KeyOwnerStatus.ACTIVE)
                .ownerStatusUpdatedAt(reactivatedAt)
                .build();

        assertThat(keysDao.save(reactivatedOwner)).isEqualTo(reactivatedOwner);
    }
}
