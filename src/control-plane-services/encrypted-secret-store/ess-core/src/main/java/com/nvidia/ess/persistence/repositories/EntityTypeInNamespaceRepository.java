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
package com.nvidia.ess.persistence.repositories;

import static com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel.DERIVED_COLUMN_ENTITY_TYPE;
import static com.nvidia.ess.persistence.models.NamespaceModel.COLUMN_ENTITY_TYPES;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_CREATED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_DELETED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_ENTITY_HASH_SIZE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_NAMESPACE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_NOTARY_AUTHORIZATIONS;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_PREVIOUS_ENTITY_HASH_SIZE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_REQUIRE_LWT_FOR_SECRET_WRITES;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_OAUTH_AUTHORIZATIONS;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_UPDATED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.TABLE_NAME;

import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel;
import org.springframework.data.cassandra.repository.Consistency;
import org.springframework.data.cassandra.repository.Query;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import org.springframework.data.repository.query.Param;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Mono;

/**
 * Repository for optimized entity type lookups within a namespace.
 * Uses CQL map element selection to fetch only the specific entity type
 * instead of the entire entity_types map.
 */
@Repository
public interface EntityTypeInNamespaceRepository extends ReactiveCassandraRepository<EntityTypeInNamespaceModel, String> {

    /**
     * Optimized query that fetches only a single entity type from the entity_types map.
     * Uses CQL map element selection syntax: entity_types[:entityType]
     * This avoids deserializing the entire entity_types map which can be large
     * in namespaces with many entity types (including tombstoned ones).
     *
     * @param namespace the namespace to query
     * @param entityType the entity type key to fetch from the map
     * @return EntityTypeInNamespaceModel with the single entity type, or empty if namespace doesn't exist
     */
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    @Query("SELECT " + COLUMN_NAMESPACE + ", "
            + COLUMN_OAUTH_AUTHORIZATIONS + ", "
            + COLUMN_NOTARY_AUTHORIZATIONS + ", "
            + COLUMN_ENTITY_TYPES + "[ :entityType ] AS " + DERIVED_COLUMN_ENTITY_TYPE + ", "
            + COLUMN_CREATED_AT + ", "
            + COLUMN_UPDATED_AT + ", "
            + COLUMN_ENTITY_HASH_SIZE + ", "
            + COLUMN_PREVIOUS_ENTITY_HASH_SIZE + ", "
            + COLUMN_DELETED_AT + ", "
            + COLUMN_REQUIRE_LWT_FOR_SECRET_WRITES
            + " FROM " + TABLE_NAME
            + " WHERE " + COLUMN_NAMESPACE + " = :namespace")
    Mono<EntityTypeInNamespaceModel> findByNamespaceWithEntityType(
            @Param("namespace") String namespace,
            @Param("entityType") String entityType);
}
