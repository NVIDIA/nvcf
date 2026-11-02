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
import static com.nvidia.apikeys.TestData.KEY_V2_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.services.LegacyAccountTranslatorTest.LEGACY_KEY_V2;
import static com.nvidia.apikeys.services.LegacyAccountTranslatorTest.LEGACY_NCA_ID;
import static com.nvidia.apikeys.services.LegacyAccountTranslatorTest.SERVICE_ID_MAP;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.vo.IntrospectionKeyVo;
import java.util.Optional;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class IntrospectionKeyLoaderTest {

    @Mock
    private KeysV2Dao keysV2Dao;
    @Mock
    private KeysDao keysDao;
    @Mock
    private LegacyMigrationState migrationState;

    private IntrospectionKeyLoader loader;

    @BeforeEach
    void setUp() {
        var nakProperties = NakProperties.builder()
                .ncaId(LEGACY_NCA_ID)
                .serviceIdMap(SERVICE_ID_MAP)
                .build();
        loader = new IntrospectionKeyLoader(
                keysV2Dao, keysDao, new LegacyAccountTranslator(nakProperties), migrationState,
                nakProperties);
    }

    @Test
    void v2KeyIsReturnedWithItsAccount() {
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(KEY_V2_VO_1));

        assertThat(loader.getKeyByHash(API_KEY_HASH_1))
                .contains(new IntrospectionKeyVo(NCA_ID_1, KEY_VO_1));
        verifyNoInteractions(keysDao, migrationState);
    }

    @Test
    void legacyAccountV2KeyUsesLegacyServiceIds() {
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(LEGACY_KEY_V2));

        assertThat(loader.getKeyByHash(API_KEY_HASH_1))
                .contains(new IntrospectionKeyVo(LEGACY_NCA_ID, KEY_VO_1));
    }

    @Test
    void fallsBackToLegacyBeforeMigrationCompletes() {
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.empty());
        when(migrationState.isCompleted()).thenReturn(false);
        when(keysDao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.of(KEY_VO_1));

        assertThat(loader.getKeyByHash(API_KEY_HASH_1))
                .contains(new IntrospectionKeyVo(LEGACY_NCA_ID, KEY_VO_1));
    }

    @Test
    void readsOnlyV2AfterMigrationCompletes() {
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.empty());
        when(migrationState.isCompleted()).thenReturn(true);

        assertThat(loader.getKeyByHash(API_KEY_HASH_1)).isEmpty();
        verify(keysDao, never()).getKeyByHash(API_KEY_HASH_1);
    }

    @Test
    void missingEverywhereIsEmpty() {
        when(keysV2Dao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.empty());
        when(keysDao.getKeyByHash(API_KEY_HASH_1)).thenReturn(Optional.empty());

        assertThat(loader.getKeyByHash(API_KEY_HASH_1)).isEmpty();
    }
}
