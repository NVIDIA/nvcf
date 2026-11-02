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

package com.nvidia.apikeys.persistance.repositories;

import static com.nvidia.apikeys.persistance.models.RowUpdateLockModel.COLUMN_RECORD_KEY;
import static com.nvidia.apikeys.persistance.models.RowUpdateLockModel.COLUMN_TABLE_NAME;
import static com.nvidia.apikeys.persistance.models.RowUpdateLockModel.COLUMN_UPDATED_AT;
import static com.nvidia.apikeys.persistance.models.RowUpdateLockModel.TABLE_NAME;

import com.nvidia.apikeys.persistance.models.RowUpdateLockModel;
import java.time.Instant;
import java.util.Optional;
import org.springframework.data.cassandra.repository.MapIdCassandraRepository;
import org.springframework.data.cassandra.repository.Query;
import org.springframework.data.repository.query.Param;
import org.springframework.stereotype.Repository;

@Repository
public interface RowUpdateLockRepository extends MapIdCassandraRepository<RowUpdateLockModel> {

    String TTL = "ttl";
    String TOKEN = "token";
    String EXPECTED_TOKEN = "expected_token";

    @Query("INSERT INTO " + TABLE_NAME
            + "(" + COLUMN_TABLE_NAME
            + "," + COLUMN_RECORD_KEY
            + "," + COLUMN_UPDATED_AT + ") "
            + " VALUES "
            + "(:" + COLUMN_TABLE_NAME
            + ",:" + COLUMN_RECORD_KEY
            + ",:" + COLUMN_UPDATED_AT + ") "
            + " IF NOT EXISTS "
            + " USING TTL :" + TTL
    )
    boolean lock(
            @Param(COLUMN_TABLE_NAME) String table,
            @Param(COLUMN_RECORD_KEY) String recordKey,
            @Param(COLUMN_UPDATED_AT) Instant updatedAt,
            @Param(TTL) int ttl);

    @Query("UPDATE " + TABLE_NAME
            + " USING TTL :" + TTL
            + " SET " + COLUMN_UPDATED_AT + " = :" + TOKEN
            + " WHERE " + COLUMN_TABLE_NAME + " = :" + COLUMN_TABLE_NAME
            + " AND " + COLUMN_RECORD_KEY + " = :" + COLUMN_RECORD_KEY
            + " IF " + COLUMN_UPDATED_AT + " = :" + EXPECTED_TOKEN
    )
    boolean renew(
            @Param(COLUMN_TABLE_NAME) String table,
            @Param(COLUMN_RECORD_KEY) String recordKey,
            @Param(TOKEN) Instant token,
            @Param(EXPECTED_TOKEN) Instant expectedToken,
            @Param(TTL) int ttl);

    @Query("DELETE FROM " + TABLE_NAME
            + " WHERE " + COLUMN_TABLE_NAME + " = :" + COLUMN_TABLE_NAME
            + " AND " + COLUMN_RECORD_KEY + " = :" + COLUMN_RECORD_KEY
            + " IF " + COLUMN_UPDATED_AT + " = :" + EXPECTED_TOKEN
    )
    boolean unlock(
            @Param(COLUMN_TABLE_NAME) String table,
            @Param(COLUMN_RECORD_KEY) String recordKey,
            @Param(EXPECTED_TOKEN) Instant expectedToken);

    Optional<RowUpdateLockModel> findByTableNameAndRecordKey(String tableName, String recordKey);
}
