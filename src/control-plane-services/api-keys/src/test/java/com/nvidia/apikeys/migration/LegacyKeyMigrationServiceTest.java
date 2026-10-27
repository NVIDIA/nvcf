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
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.MigrationProperties;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.persistance.dao.LegacyKeyScanDao;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.services.LegacyAccountTranslator;
import com.nvidia.apikeys.services.LegacyKeyMaintenance;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.nio.ByteBuffer;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class LegacyKeyMigrationServiceTest {

    private static final String LEGACY_NCA_ID = "legacy-nca";
    private static final String SERVICE_NAME = "nvcf";
    private static final int PAGE_SIZE = 2;
    private static final ByteBuffer NEXT_PAGE = ByteBuffer.wrap(new byte[]{1});
    private static final KeyV2Vo MIGRATED_KEY_1 = KeyV2Vo.builder()
            .keyStatus(KEY_VO_1.getKeyStatus())
            .ncaId(LEGACY_NCA_ID)
            .ownerType(KEY_VO_1.getOwnerType())
            .ownerId(KEY_VO_1.getOwnerId())
            .issuerServiceId(SERVICE_NAME)
            .audienceServiceIds(Set.of(SERVICE_NAME))
            .keyId(KEY_VO_1.getKeyId())
            .keyHash(KEY_VO_1.getKeyHash())
            .createdAt(KEY_VO_1.getCreatedAt())
            .expiresAt(KEY_VO_1.getExpiresAt())
            .deletesAt(KEY_VO_1.getDeletesAt())
            .apiKeySuffix(KEY_VO_1.getApiKeySuffix())
            .authorizations(KEY_VO_1.getAuthorizations())
            .description(KEY_VO_1.getDescription())
            .build();

    @Mock
    private LegacyKeyScanDao scanDao;
    @Mock
    private KeysV2Dao keysV2Dao;
    @Mock
    private LegacyKeyMaintenance maintenance;
    @Mock
    private MillisecondPrecisionClock clock;

    private SimpleMeterRegistry registry;
    private MigrationProperties.LegacyKeys settings;
    private LegacyKeyMigrationService service;

    @BeforeEach
    void setUp() {
        var migration = new MigrationProperties();
        settings = migration.getLegacyKeys();
        settings.setPageSize(PAGE_SIZE);
        settings.setBatchDelay(Duration.ZERO);
        settings.setPageFetchBackoff(Duration.ZERO);
        settings.setMaintenanceGracePeriod(Duration.ZERO);
        var nakProperties = NakProperties.builder()
                .ncaId(LEGACY_NCA_ID)
                .serviceIdMap(Map.of(SERVICE_NAME, SERVICE_ID_1))
                .migration(migration)
                .build();
        registry = new SimpleMeterRegistry();
        service = new LegacyKeyMigrationService(
                scanDao, keysV2Dao, new LegacyAccountTranslator(nakProperties), maintenance, clock,
                new LegacyKeyMigrationMetrics(registry), nakProperties);

        lenient().when(clock.instant()).thenReturn(TEST_TIME);
        lenient().when(maintenance.acquire(TEST_TIME)).thenReturn(true);
        lenient().when(maintenance.renew(TEST_TIME)).thenReturn(true);
        lenient().when(maintenance.release(TEST_TIME)).thenReturn(true);
        lenient().when(keysV2Dao.getKeyByHash(any())).thenReturn(Optional.empty());
        lenient().when(keysV2Dao.get(any(), any(), any(), any(), any())).thenReturn(Optional.empty());
    }

    @AfterEach
    void clearInterrupt() {
        Thread.interrupted();
    }

    @Test
    void copiesLegacyKeyIntoKeysV2() {
        var row = stubRow(KEY_VO_1);
        stubPages(List.of(row));

        var result = service.run();

        assertThat(result.outcome()).isEqualTo(RunOutcome.COMPLETED);
        assertThat(result.count(KeyResult.MIGRATED)).isEqualTo(1);
        assertThat(result.isClean()).isFalse();
        verify(keysV2Dao).save(MIGRATED_KEY_1);
        verify(maintenance).release(TEST_TIME);
        assertThat(keyCount(KeyResult.MIGRATED)).isEqualTo(1);
        assertThat(runCount(RunOutcome.COMPLETED)).isEqualTo(1);
    }

    @Test
    void copiesExpiredKeyWithStoredStatus() {
        var expired = KEY_VO_1.toBuilder().expiresAt(TEST_TIME.minusSeconds(1)).build();
        stubPages(List.of(stubRow(expired)));

        service.run();

        verify(keysV2Dao).save(MIGRATED_KEY_1.toBuilder()
                                       .keyStatus(KeyStatus.ACTIVE)
                                       .expiresAt(TEST_TIME.minusSeconds(1))
                                       .build());
    }

    @Test
    void keyInBothKeysV2TablesIsNotWrittenAgain() {
        stubPages(List.of(stubRow(KEY_VO_1)));
        when(keysV2Dao.getKeyByHash(MIGRATED_KEY_1.getKeyHash())).thenReturn(Optional.of(MIGRATED_KEY_1));
        when(keysV2Dao.get(LEGACY_NCA_ID, MIGRATED_KEY_1.getOwnerType(), MIGRATED_KEY_1.getOwnerId(),
                           SERVICE_NAME, KEY_ID_1))
                .thenReturn(Optional.of(KeysByOwnerAndAccountAndServiceVo.from(MIGRATED_KEY_1)));

        var result = service.run();

        assertThat(result.count(KeyResult.ALREADY_MIGRATED)).isEqualTo(1);
        assertThat(result.isClean()).isTrue();
        verify(keysV2Dao, never()).save(any());
    }

    @Test
    void keyInOnlyOneKeysV2TableIsWrittenAgain() {
        stubPages(List.of(stubRow(KEY_VO_1)));
        when(keysV2Dao.getKeyByHash(MIGRATED_KEY_1.getKeyHash())).thenReturn(Optional.of(MIGRATED_KEY_1));

        var result = service.run();

        assertThat(result.count(KeyResult.MIGRATED)).isEqualTo(1);
        verify(keysV2Dao).save(MIGRATED_KEY_1);
    }

    @Test
    void keyWithUnmappedIssuerIsSkipped() {
        stubPages(List.of(stubRow(KEY_VO_1.toBuilder().issuerServiceId("unmapped").build())));

        var result = service.run();

        assertThat(result.count(KeyResult.SKIPPED)).isEqualTo(1);
        assertThat(result.isClean()).isFalse();
        verify(keysV2Dao, never()).save(any());
    }

    @Test
    void failedKeyDoesNotStopTheRun() {
        var broken = mock(KeyModel.class);
        when(scanDao.decrypt(broken)).thenThrow(new IllegalStateException("decrypt failed"));
        var row = stubRow(KEY_VO_1);
        stubPages(List.of(broken, row));

        var result = service.run();

        assertThat(result.outcome()).isEqualTo(RunOutcome.COMPLETED);
        assertThat(result.count(KeyResult.FAILED)).isEqualTo(1);
        assertThat(result.count(KeyResult.MIGRATED)).isEqualTo(1);
        verify(keysV2Dao).save(MIGRATED_KEY_1);
    }

    @Test
    void walksEveryPageAndRenewsTheLeaseBeforeEach() {
        var first = stubRow(KEY_VO_1);
        var second = stubRow(KEY_VO_1.toBuilder().keyId("key-2").keyHash("hash-2").build());
        when(scanDao.page(PAGE_SIZE, null)).thenReturn(new LegacyKeyScanDao.Page(List.of(first), NEXT_PAGE));
        when(scanDao.page(PAGE_SIZE, NEXT_PAGE)).thenReturn(new LegacyKeyScanDao.Page(List.of(second), null));

        var result = service.run();

        assertThat(result.count(KeyResult.MIGRATED)).isEqualTo(2);
        verify(maintenance, times(2)).renew(TEST_TIME);
    }

    @Test
    void runIsSkippedWhenLeaseIsHeld() {
        when(maintenance.acquire(TEST_TIME)).thenReturn(false);

        var result = service.run();

        assertThat(result.outcome()).isEqualTo(RunOutcome.LEASE_NOT_ACQUIRED);
        assertThat(result.isClean()).isFalse();
        verify(scanDao, never()).page(anyInt(), any());
        verify(maintenance, never()).release(any());
    }

    @Test
    void runStopsWhenLeaseIsLost() {
        when(maintenance.renew(TEST_TIME)).thenReturn(false);

        var result = service.run();

        assertThat(result.outcome()).isEqualTo(RunOutcome.LEASE_LOST);
        verify(scanDao, never()).page(anyInt(), any());
    }

    @Test
    void pageFetchIsRetried() {
        var row = stubRow(KEY_VO_1);
        when(scanDao.page(PAGE_SIZE, null))
                .thenThrow(new IllegalStateException("timeout"))
                .thenReturn(new LegacyKeyScanDao.Page(List.of(row), null));

        var result = service.run();

        assertThat(result.outcome()).isEqualTo(RunOutcome.COMPLETED);
        assertThat(result.count(KeyResult.MIGRATED)).isEqualTo(1);
    }

    @Test
    void runFailsAfterLastPageFetchAttempt() {
        when(scanDao.page(PAGE_SIZE, null)).thenThrow(new IllegalStateException("timeout"));

        var result = service.run();

        assertThat(result.outcome()).isEqualTo(RunOutcome.FAILED);
        verify(scanDao, times(settings.getPageFetchAttempts())).page(PAGE_SIZE, null);
        verify(maintenance).release(TEST_TIME);
        assertThat(runCount(RunOutcome.FAILED)).isEqualTo(1);
    }

    @Test
    void interruptedRunReleasesTheLease() {
        stubPages(List.of(mock(KeyModel.class)));
        Thread.currentThread().interrupt();

        var result = service.run();

        assertThat(result.outcome()).isEqualTo(RunOutcome.INTERRUPTED);
        verify(keysV2Dao, never()).save(any());
        verify(maintenance).release(TEST_TIME);
    }

    @Test
    void countersArePreInitialized() {
        for (var result : KeyResult.values()) {
            assertThat(keyCount(result)).isZero();
        }
        for (var outcome : RunOutcome.values()) {
            assertThat(runCount(outcome)).isZero();
        }
        assertThat(registry.get(LegacyKeyMigrationMetrics.COMPLETED).gauge().value()).isZero();
    }

    private KeyModel stubRow(KeyVo key) {
        var row = mock(KeyModel.class);
        when(scanDao.decrypt(row)).thenReturn(key);
        return row;
    }

    private void stubPages(List<KeyModel> rows) {
        when(scanDao.page(PAGE_SIZE, null)).thenReturn(new LegacyKeyScanDao.Page(rows, null));
    }

    private double keyCount(KeyResult result) {
        return registry.get(LegacyKeyMigrationMetrics.KEYS)
                .tag(LegacyKeyMigrationMetrics.TAG_RESULT, LegacyKeyMigrationMetrics.tagValue(result))
                .counter().count();
    }

    private double runCount(RunOutcome outcome) {
        return registry.get(LegacyKeyMigrationMetrics.RUNS)
                .tag(LegacyKeyMigrationMetrics.TAG_OUTCOME, LegacyKeyMigrationMetrics.tagValue(outcome))
                .counter().count();
    }
}
