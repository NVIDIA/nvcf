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
package com.nvidia.icms.outbound.cassandra.lockbyttl.entity;

import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(LockByTtlEntity.TABLE_NAME)
public class LockByTtlEntity {

    public static final String TABLE_NAME = "lock_by_ttl";

    public static final String COLUMN_LOCK_NAME = "lock_name";
    public static final String COLUMN_LOCKED_AT = "locked_at";
    public static final String COLUMN_LOCKED_BY = "locked_by";
    public static final String COLUMN_LOCK_TTL = "lock_ttl";

    @NonNull
    @PrimaryKey
    @PrimaryKeyColumn(ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    @Column(COLUMN_LOCK_NAME)
    private String lockName;

    @Column(COLUMN_LOCKED_AT)
    private Instant lockedAt;

    @Column(COLUMN_LOCKED_BY)
    private String lockedBy;

    @Column(COLUMN_LOCK_TTL)
    private Integer lockTtl;
}
