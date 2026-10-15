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

package com.nvidia.apikeys.persistance.models;

import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

/**
 * This table helps enforce updates frequency in other tables. The primary key consist of the
 * table tobe updated and record key is combination of descriptive partition key name plus key
 * value. updated_at is when record was last written and TTL on the row signals when record can be
 * written again.
 * This check happens in this table to avoid LWTs in tables which engaged in critical flows.
 * For example, we don't want to lock KEYS table with LWT. When we get to millions of keys we can
 * distribute locking to more tables.
 */
@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(RowUpdateLockModel.TABLE_NAME)
public class RowUpdateLockModel {

    public static final String TABLE_NAME = "row_update_lock";
    public static final String COLUMN_TABLE_NAME = "table_name";
    public static final String COLUMN_RECORD_KEY = "record_key";
    public static final String COLUMN_UPDATED_AT = "updated_at";

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_TABLE_NAME, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String tableName;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_RECORD_KEY, ordinal = 1, type = PrimaryKeyType.PARTITIONED)
    private String recordKey;

    @Column(COLUMN_UPDATED_AT)
    private Instant updatedAt;
}
