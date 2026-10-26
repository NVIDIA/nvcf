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

import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import java.time.Instant;
import java.util.stream.Stream;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

/** Paged discovery for bulk selectors, including the configured legacy account. */
@Service
public class KeysV2BulkSelectionService {

    private final KeysV2Dao keysV2Dao;
    private final KeysDao keysDao;
    private final LegacyAccountTranslator legacyAccount;
    private final int pageSize;

    public KeysV2BulkSelectionService(
            KeysV2Dao keysV2Dao, KeysDao keysDao, LegacyAccountTranslator legacyAccount,
            @Value("${spring.cassandra.request.page-size:5000}") int pageSize) {
        this.keysV2Dao = keysV2Dao;
        this.keysDao = keysDao;
        this.legacyAccount = legacyAccount;
        this.pageSize = pageSize;
    }

    /** Null dimensions select all values. The caller closes the returned stream. */
    public Stream<KeysByOwnerAndAccountAndServiceVo> select(
            String account, String user, String issuer, Instant cutoff) {
        var current = keysV2Dao.streamForBulk(account, USER, user, issuer, cutoff, pageSize);
        try {
            return Stream.concat(current, selectLegacy(account, user, issuer, cutoff));
        } catch (RuntimeException | Error failure) {
            current.close();
            throw failure;
        }
    }

    private Stream<KeysByOwnerAndAccountAndServiceVo> selectLegacy(
            String account, String user, String issuer, Instant cutoff) {
        if (account != null && !legacyAccount.isLegacy(account)) {
            return Stream.empty();
        }
        var legacyIssuer = issuer == null ? null
                : legacyAccount.findLegacyIssuerServiceId(issuer).orElse(null);
        if (issuer != null && legacyIssuer == null) {
            return Stream.empty();
        }
        return keysDao.streamForBulk(USER, user, legacyIssuer, pageSize)
                .filter(key -> legacyAccount.findLegacyIssuerServiceId(
                                legacyAccount.toServiceId(key.getIssuerServiceId()))
                        .filter(key.getIssuerServiceId()::equals).isPresent())
                .map(legacyAccount::fromLegacy)
                .filter(key -> key.getCreatedAt() == null || !key.getCreatedAt().isAfter(cutoff))
                // A migrated v2 row wins even when its creation time is after the cutoff.
                .filter(key -> keysV2Dao.get(key.getNcaId(), USER, key.getOwnerId(),
                        key.getIssuerServiceId(), key.getKeyId()).isEmpty());
    }
}
