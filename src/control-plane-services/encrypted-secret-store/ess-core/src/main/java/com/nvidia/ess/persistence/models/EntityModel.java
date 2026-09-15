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
package com.nvidia.ess.persistence.models;

import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor
@Table(EntityModel.TABLE_NAME)
public class EntityModel {
    public static final String TABLE_NAME = "entities";
    public static final String COLUMN_NAMESPACE = "namespace";
    public static final String COLUMN_HASH_BUCKET = "hash_bucket";
    public static final String COLUMN_ENTITY_TYPE = "entity_type";
    public static final String COLUMN_ENTITY_ID = "entity_id";
    public static final String COLUMN_CREATED_AT = "created_at";


    @NonNull
    @PrimaryKeyColumn(name = COLUMN_NAMESPACE, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String namespace;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_ENTITY_TYPE, ordinal = 1, type = PrimaryKeyType.PARTITIONED)
    private String entityType;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_HASH_BUCKET, ordinal = 2, type = PrimaryKeyType.PARTITIONED)
    private Integer hashBucket;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_ENTITY_ID, ordinal = 3, type = PrimaryKeyType.CLUSTERED)
    private String entityId;

    @NonNull
    @Builder.Default
    @Column(COLUMN_CREATED_AT)
    private Instant createdAt = Instant.now();
}
