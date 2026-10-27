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

import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import java.time.Instant;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeysV2BulkSelectionServiceTest {

    private static final Instant CUTOFF = Instant.parse("2026-10-21T12:00:00Z");
    private static final int PAGE_SIZE = 2;

    @Mock
    private KeysV2Dao keysV2Dao;
    @Mock
    private KeysDao keysDao;
    private KeysV2BulkSelectionService service;

    @BeforeEach
    void setUp() {
        var translator = new LegacyAccountTranslator(NakProperties.builder()
                .ncaId("legacy").serviceIdMap(Map.of("issuer", "legacy-issuer")).build());
        service = new KeysV2BulkSelectionService(keysV2Dao, keysDao, translator, PAGE_SIZE);
    }

    @Test
    void explicitOtherAccountNeverReadsLegacy() {
        var row = row("account", "key");
        when(keysV2Dao.streamForBulk("account", USER, "user", "issuer", CUTOFF, PAGE_SIZE))
                .thenReturn(Stream.of(row));
        try (var selected = service.select("account", "user", "issuer", CUTOFF)) {
            assertThat(selected).containsExactly(row);
        }
        verifyNoInteractions(keysDao);
    }

    @Test
    void wildcardSelectionIncludesCanonicalLegacyAndExcludesLaterOrMigratedRows() {
        var current = row("new-account", "current");
        var migrated = row("legacy", "migrated");
        when(keysV2Dao.streamForBulk(null, USER, null, null, CUTOFF, PAGE_SIZE))
                .thenReturn(Stream.of(current, migrated));
        when(keysDao.streamForBulk(USER, null, null, PAGE_SIZE)).thenReturn(Stream.of(
                legacy("legacy-only"), legacy("migrated"),
                legacy("newer").toBuilder().createdAt(CUTOFF.plusMillis(1)).build(),
                legacy("unmapped").toBuilder().issuerServiceId("unmapped").build()));
        when(keysV2Dao.get("legacy", USER, "user", "issuer", "migrated"))
                .thenReturn(Optional.of(migrated));
        when(keysV2Dao.get("legacy", USER, "user", "issuer", "legacy-only"))
                .thenReturn(Optional.empty());
        try (var selected = service.select(null, null, null, CUTOFF)) {
            assertThat(selected).extracting(KeysByOwnerAndAccountAndServiceVo::getKeyId)
                    .containsExactly("legacy-only", "current", "migrated");
        }
    }

    @Test
    void existingV2RowAfterCutoffPreventsLegacyCopyFromSelectingIt() {
        when(keysV2Dao.streamForBulk("legacy", USER, "user", "issuer", CUTOFF, PAGE_SIZE))
                .thenReturn(Stream.empty());
        when(keysDao.streamForBulk(USER, "user", "legacy-issuer", PAGE_SIZE))
                .thenReturn(Stream.of(legacy("same-key")));
        when(keysV2Dao.get("legacy", USER, "user", "issuer", "same-key"))
                .thenReturn(Optional.of(row("legacy", "same-key").toBuilder()
                        .createdAt(CUTOFF.plusMillis(1)).build()));
        try (var selected = service.select("legacy", "user", "issuer", CUTOFF)) {
            assertThat(selected).isEmpty();
        }
    }

    @Test
    void unmappedCanonicalIssuerCannotSelectLegacy() {
        when(keysV2Dao.streamForBulk(null, USER, null, "other", CUTOFF, PAGE_SIZE))
                .thenReturn(Stream.empty());
        try (var selected = service.select(null, null, "other", CUTOFF)) {
            assertThat(selected).isEmpty();
        }
        verifyNoInteractions(keysDao);
    }

    @Test
    void closesBothSourceStreamsWhenConsumptionStopsEarly() {
        var currentClosed = new AtomicBoolean();
        var legacyClosed = new AtomicBoolean();
        when(keysV2Dao.streamForBulk(null, USER, null, null, CUTOFF, PAGE_SIZE))
                .thenReturn(Stream.of(row("account", "first"), row("account", "second"))
                        .onClose(() -> currentClosed.set(true)));
        when(keysDao.streamForBulk(USER, null, null, PAGE_SIZE))
                .thenReturn(Stream.of(legacy("legacy")).onClose(() -> legacyClosed.set(true)));
        try (var selected = service.select(null, null, null, CUTOFF)) {
            assertThat(selected.findFirst()).isPresent();
        }
        assertThat(currentClosed).isTrue();
        assertThat(legacyClosed).isTrue();
    }

    @Test
    void closesV2StreamIfLegacyQueryFails() {
        var closed = new AtomicBoolean();
        when(keysV2Dao.streamForBulk(null, USER, null, null, CUTOFF, PAGE_SIZE))
                .thenReturn(Stream.<KeysByOwnerAndAccountAndServiceVo>empty().onClose(() -> closed.set(true)));
        when(keysDao.streamForBulk(USER, null, null, PAGE_SIZE))
                .thenThrow(new IllegalStateException("query failed"));
        assertThatThrownBy(() -> service.select(null, null, null, CUTOFF))
                .isInstanceOf(IllegalStateException.class).hasMessage("query failed");
        assertThat(closed).isTrue();
    }

    private static KeysByOwnerAndAccountAndServiceVo row(String account, String keyId) {
        return KeysByOwnerAndAccountAndServiceVo.builder().ncaId(account).ownerType(USER)
                .ownerId("user").issuerServiceId("issuer").keyId(keyId).createdAt(CUTOFF).build();
    }

    private static KeyByOwnerAndServiceVo legacy(String keyId) {
        return KeyByOwnerAndServiceVo.builder().ownerType(USER).ownerId("user")
                .issuerServiceId("legacy-issuer").keyId(keyId).createdAt(CUTOFF).build();
    }
}
