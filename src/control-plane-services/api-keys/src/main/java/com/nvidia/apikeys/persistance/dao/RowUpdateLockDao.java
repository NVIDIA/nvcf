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

import com.nvidia.apikeys.persistance.repositories.RowUpdateLockRepository;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

import java.time.Instant;

@Service
@RequiredArgsConstructor
public class RowUpdateLockDao {

    private final RowUpdateLockRepository repository;

    public boolean lock(String tableName, String recordKey, Instant updatedAt, int ttl) {
        return repository.lock(tableName, recordKey, updatedAt, ttl);
    }

    /**
     * Extends the TTL of a lock this caller holds. Returns false when the lock expired or another
     * caller holds it.
     */
    public boolean renew(String tableName, String recordKey, Instant token, int ttl) {
        return repository.renew(tableName, recordKey, token, token, ttl);
    }

    /**
     * Releases a lock only when this caller still holds it.
     */
    public boolean unlock(String tableName, String recordKey, Instant token) {
        return repository.unlock(tableName, recordKey, token);
    }

    public boolean isLocked(String tableName, String recordKey) {
        return repository.findByTableNameAndRecordKey(tableName, recordKey).isPresent();
    }

}
