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
import java.util.Map;
import lombok.AllArgsConstructor;
import lombok.Data;
import lombok.EqualsAndHashCode;
import lombok.NoArgsConstructor;
import lombok.experimental.SuperBuilder;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.Table;

/**
 * Full namespace model including the entity_types map.
 * Extends NamespaceWithoutEntityTypesModel to add the entity_types field.
 * 
 * For operations that don't need entity types, consider using NamespaceWithoutEntityTypesModel
 * or EntityTypeInNamespaceModel to avoid deserializing the potentially large entity_types map.
 */
@SuperBuilder(toBuilder = true)
@Data
@EqualsAndHashCode(callSuper = true)
@NoArgsConstructor
@AllArgsConstructor
@Table(NamespaceWithoutEntityTypesModel.TABLE_NAME)
public class NamespaceModel extends NamespaceWithoutEntityTypesModel {
    
    public static final String COLUMN_ENTITY_TYPES = "entity_types";

    @Nullable
    @Column(COLUMN_ENTITY_TYPES)
    private Map<String, EntityTypeUdt> entityTypes;
}
