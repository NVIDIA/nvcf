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

import static com.nvidia.ess.persistence.models.NamespaceModel.COLUMN_ENTITY_TYPES;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_CREATED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_DELETED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_ENTITY_HASH_SIZE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_NAMESPACE;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_REQUIRE_LWT_FOR_SECRET_WRITES;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.COLUMN_UPDATED_AT;
import static com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel.TABLE_NAME;

import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import com.nvidia.ess.persistence.models.EntityTypeUdt;
import com.nvidia.ess.persistence.models.NamespaceModel;
import java.time.Instant;
import org.springframework.data.cassandra.repository.Consistency;
import org.springframework.data.cassandra.repository.Query;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import org.springframework.data.repository.query.Param;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;


@Repository
public interface NamespaceRepository extends ReactiveCassandraRepository<NamespaceModel, String> {

    @Query("INSERT INTO " + TABLE_NAME + " (" + COLUMN_NAMESPACE + ", " + COLUMN_CREATED_AT + ", " + COLUMN_UPDATED_AT +
            ", " + COLUMN_ENTITY_HASH_SIZE + ", " + COLUMN_REQUIRE_LWT_FOR_SECRET_WRITES + ") "
            + "VALUES (:#{#namespace.namespace}, :#{#namespace.createdAt}, :#{#namespace.updatedAt}, :#{#namespace.entityHashSize}, :#{#namespace.requireLWTForSecretVersionWrites}) IF NOT EXISTS")
    Mono<Boolean> insertIfNotExists(@Param("namespace") NamespaceModel namespace);

    @Query("UPDATE " + TABLE_NAME + " SET " + COLUMN_ENTITY_TYPES + "[ :key ] = :value "
            + "WHERE " + COLUMN_NAMESPACE + " = :namespace")
    Mono<Void> updateEntityType(String namespace, String key, EntityTypeUdt value);

    @Query("UPDATE " + TABLE_NAME + " SET " + COLUMN_DELETED_AT + " = :value "
            + "WHERE " + COLUMN_NAMESPACE + " = :namespace")
    Mono<Void> updateDeletedAt(String namespace, Instant value);

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Mono<NamespaceModel> findByNamespace(String namespace);

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    @Query("SELECT * from " + TABLE_NAME)
    @Override
    Flux<NamespaceModel> findAll();
}
