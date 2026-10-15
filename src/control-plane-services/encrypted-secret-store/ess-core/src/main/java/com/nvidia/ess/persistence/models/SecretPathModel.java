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

import com.datastax.oss.driver.api.core.uuid.Uuids;
import jakarta.annotation.Nullable;
import java.util.UUID;
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
@Table(SecretPathModel.TABLE_NAME)
public class SecretPathModel {
    public static final String TABLE_NAME = "secret_paths_by_entity";
    public static final String COLUMN_NAMESPACE = "namespace";
    public static final String COLUMN_ENTITY = "entity";
    public static final String COLUMN_PATH = "path";
    public static final String COLUMN_ENTITY_VERSION = "entity_version";
    public static final String COLUMN_UPDATED_AT = "updated_at";
    public static final String COLUMN_IS_DIR = "is_dir";

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_NAMESPACE, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String namespace;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_ENTITY, ordinal = 1, type = PrimaryKeyType.PARTITIONED)
    private String entity;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_PATH, ordinal = 2, type = PrimaryKeyType.CLUSTERED)
    private String path;

    @NonNull
    @Column(value = COLUMN_ENTITY_VERSION, isStatic = true)
    private UUID entityVersion;

    @NonNull
    @Builder.Default
    @Column(COLUMN_UPDATED_AT)
    private UUID updatedAt = Uuids.timeBased();

    @Nullable
    @Column(COLUMN_IS_DIR)
    private Boolean isDir;
}
