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

import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.services.LegacyAccountTranslatorTest.LEGACY_NCA_ID;
import static com.nvidia.apikeys.services.LegacyKeyMaintenance.LEASE_KEY;
import static com.nvidia.apikeys.services.LegacyKeyMaintenance.LEASE_TABLE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatNoException;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.MigrationProperties;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.config.exceptions.ServiceUnavailableException;
import com.nvidia.apikeys.persistance.dao.RowUpdateLockDao;
import java.time.Duration;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;

@ExtendWith(MockitoExtension.class)
class LegacyKeyMaintenanceTest {

    private static final Duration LEASE_DURATION = Duration.ofSeconds(90);

    @Mock
    private RowUpdateLockDao rowUpdateLockDao;

    private LegacyKeyMaintenance maintenance;

    @BeforeEach
    void setUp() {
        var migration = new MigrationProperties();
        migration.getLegacyKeys().setLeaseDuration(LEASE_DURATION);
        var nakProperties = NakProperties.builder().ncaId(LEGACY_NCA_ID).migration(migration).build();
        maintenance = new LegacyKeyMaintenance(
                rowUpdateLockDao, new LegacyAccountTranslator(nakProperties), nakProperties);
    }

    @Test
    void legacyAccountWriteIsRejectedWhileLeaseIsHeld() {
        when(rowUpdateLockDao.isLocked(LEASE_TABLE, LEASE_KEY)).thenReturn(true);

        assertThatThrownBy(() -> maintenance.assertWritable(LEGACY_NCA_ID))
                .isInstanceOfSatisfying(ServiceUnavailableException.class, e -> {
                    assertThat(e.getStatusCode()).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE);
                    assertThat(e.getBody().getDetail()).isEqualTo(LegacyKeyMaintenance.MESG_MAINTENANCE);
                    assertThat(e.getHeaders().getFirst(HttpHeaders.RETRY_AFTER)).isEqualTo("90");
                });
    }

    @Test
    void legacyAccountWriteIsAllowedWithoutLease() {
        when(rowUpdateLockDao.isLocked(LEASE_TABLE, LEASE_KEY)).thenReturn(false);

        assertThatNoException().isThrownBy(() -> maintenance.assertWritable(LEGACY_NCA_ID));
    }

    @Test
    void otherAccountsAreNeverBlocked() {
        assertThatNoException().isThrownBy(() -> maintenance.assertWritable(NCA_ID_1));

        verifyNoInteractions(rowUpdateLockDao);
    }

    @Test
    void leaseUsesConfiguredDurationAsTtl() {
        when(rowUpdateLockDao.lock(LEASE_TABLE, LEASE_KEY, TEST_TIME, 90)).thenReturn(true);
        when(rowUpdateLockDao.renew(LEASE_TABLE, LEASE_KEY, TEST_TIME, 90)).thenReturn(true);

        assertThat(maintenance.acquire(TEST_TIME)).isTrue();
        assertThat(maintenance.renew(TEST_TIME)).isTrue();
        maintenance.release(TEST_TIME);

        verify(rowUpdateLockDao).unlock(LEASE_TABLE, LEASE_KEY, TEST_TIME);
    }
}
