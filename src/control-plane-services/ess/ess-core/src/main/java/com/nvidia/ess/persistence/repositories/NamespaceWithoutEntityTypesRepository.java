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

import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_CREATED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_DELETED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_ENTITY_HASH_SIZE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_NAMESPACE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_NOTARY_AUTHORIZATIONS;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_OAUTH_AUTHORIZATIONS;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_PREVIOUS_ENTITY_HASH_SIZE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_REQUIRE_LWT_FOR_SECRET_WRITES;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_UPDATED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.TABLE_NAME;

import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel;
import org.springframework.data.cassandra.repository.Consistency;
import org.springframework.data.cassandra.repository.Query;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import org.springframework.data.repository.query.Param;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Mono;

@Repository
public interface NamespaceWithoutEntityTypesRepository extends ReactiveCassandraRepository<NamespaceWithoutEntityTypesModel, String> {

    /**
     * Optimized query that fetches namespace without deserializing the entity_types map.
     * This avoids significant overhead when a namespace has many (tombstoned) entity types.
     * Used primarily for authorization lookups that don't need entity type information.
     *
     * @param namespace the namespace to query
     * @return NamespaceWithoutEntityTypesModel without entity_types, or empty if namespace doesn't exist
     */
    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    @Query("SELECT " + COLUMN_NAMESPACE + ", "
            + COLUMN_OAUTH_AUTHORIZATIONS + ", "
            + COLUMN_NOTARY_AUTHORIZATIONS + ", "
            + COLUMN_CREATED_AT + ", "
            + COLUMN_UPDATED_AT + ", "
            + COLUMN_ENTITY_HASH_SIZE + ", "
            + COLUMN_PREVIOUS_ENTITY_HASH_SIZE + ", "
            + COLUMN_DELETED_AT + ", "
            + COLUMN_REQUIRE_LWT_FOR_SECRET_WRITES
            + " FROM " + TABLE_NAME
            + " WHERE " + COLUMN_NAMESPACE + " = :namespace")
    Mono<NamespaceWithoutEntityTypesModel> findByNamespace(@Param("namespace") String namespace);
}
