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

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.vo.IntrospectionKeyVo;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

/**
 * Finds a key by hash for introspection. Reads keys_v2 first. Falls back to the legacy keys
 * table only until the legacy key migration has completed, because every legacy key is in
 * keys_v2 after that.
 */
@Service
@RequiredArgsConstructor
public class IntrospectionKeyLoader {

    private final KeysV2Dao keysV2Dao;
    private final KeysDao keysDao;
    private final LegacyAccountTranslator legacyAccount;
    private final LegacyMigrationState migrationState;
    private final NakProperties nakProperties;

    public Optional<IntrospectionKeyVo> getKeyByHash(String keyHash) {
        Optional<IntrospectionKeyVo> key = keysV2Dao.getKeyByHash(keyHash)
                .map(v2 -> new IntrospectionKeyVo(
                        v2.getNcaId(), legacyAccount.toIntrospectionKey(v2)));
        if (key.isPresent() || migrationState.isCompleted()) {
            return key;
        }
        return keysDao.getKeyByHash(keyHash)
                .map(legacy -> new IntrospectionKeyVo(nakProperties.getNcaId(), legacy));
    }
}
