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

import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.EqualsAndHashCode;
import lombok.NoArgsConstructor;
import lombok.experimental.SuperBuilder;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.Table;

/**
 * Extends NamespaceWithoutEntityTypesModel to include a single entity type.
 * Used for optimized lookups that need to validate a specific entity type exists
 * without deserializing the entire entity_types map.
 * 
 * The CQL query selects only entity_types[:entityType] instead of the full map,
 * which is significantly faster for namespaces with many entity types.
 */
@SuperBuilder(toBuilder = true)
@Data
@EqualsAndHashCode(callSuper = true)
@NoArgsConstructor
@AllArgsConstructor
@Table(NamespaceWithoutEntityTypesModel.TABLE_NAME)
public class EntityTypeInNamespaceModel extends NamespaceWithoutEntityTypesModel {

    /**
     * Derived column name for the single entity type result.
     * Used in CQL: SELECT entity_types[:key] AS entity_type
     */
    public static final String DERIVED_COLUMN_ENTITY_TYPE = "entity_type";

    /**
     * Single entity type fetched via CQL map element selection: entity_types[:key].
     * Will be null if the entity type key doesn't exist in the map.
     */
    @Nullable
    @Column(DERIVED_COLUMN_ENTITY_TYPE)
    private EntityTypeUdt entityType;
}
