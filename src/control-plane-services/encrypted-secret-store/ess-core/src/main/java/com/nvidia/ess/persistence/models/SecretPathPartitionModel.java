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

import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.EqualsAndHashCode;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

/**
 * Truncated version of {@link SecretPathModel} that holds only partition-key and static columns.
 */
@Builder(toBuilder = true)
@EqualsAndHashCode(callSuper = false)
@Data
@NoArgsConstructor
@AllArgsConstructor
@Table(SecretPathModel.TABLE_NAME)
public class SecretPathPartitionModel {

    @NonNull
    @PrimaryKeyColumn(name = SecretPathModel.COLUMN_NAMESPACE, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String namespace;

    @NonNull
    @PrimaryKeyColumn(name = SecretPathModel.COLUMN_ENTITY, ordinal = 1, type = PrimaryKeyType.PARTITIONED)
    private String entity;

    @NonNull
    @Column(value = SecretPathModel.COLUMN_ENTITY_VERSION, isStatic = true)
    private UUID entityVersion;
}
