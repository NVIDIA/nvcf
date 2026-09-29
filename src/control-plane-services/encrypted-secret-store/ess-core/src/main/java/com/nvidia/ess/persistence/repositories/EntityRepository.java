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


import static com.nvidia.ess.persistence.models.EntityModel.COLUMN_CREATED_AT;
import static com.nvidia.ess.persistence.models.EntityModel.COLUMN_ENTITY_ID;
import static com.nvidia.ess.persistence.models.EntityModel.COLUMN_ENTITY_TYPE;
import static com.nvidia.ess.persistence.models.EntityModel.COLUMN_HASH_BUCKET;
import static com.nvidia.ess.persistence.models.EntityModel.COLUMN_NAMESPACE;
import static com.nvidia.ess.persistence.models.EntityModel.TABLE_NAME;

import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import com.nvidia.ess.persistence.models.EntityModel;
import org.springframework.data.cassandra.core.mapping.MapId;
import org.springframework.data.cassandra.repository.Consistency;
import org.springframework.data.cassandra.repository.Query;
import org.springframework.data.cassandra.repository.ReactiveCassandraRepository;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Repository
public interface EntityRepository extends ReactiveCassandraRepository<EntityModel, MapId> {
    @Query("INSERT INTO " + TABLE_NAME + " (" + COLUMN_NAMESPACE + ", " + COLUMN_ENTITY_TYPE + ", " + COLUMN_HASH_BUCKET +
            ", " + COLUMN_ENTITY_ID + ", " + COLUMN_CREATED_AT + ") "
            + "VALUES (:#{#entityModel.namespace}, :#{#entityModel.entityType}, :#{#entityModel.hashBucket}, :#{#entityModel.entityId}, :#{#entityModel.createdAt})")
    // overriding for now to avoid code changes, will change later
    Mono<Void> insertEntity(EntityModel entityModel);

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Mono<Boolean> existsByNamespaceAndEntityTypeAndHashBucketAndEntityId(String namespace, String entityType, int hashBucket, String entityId);

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Mono<EntityModel> findByNamespaceAndEntityTypeAndHashBucketAndEntityId(String namespace, String entityType, int hashBucket, String entityId);

    @Consistency(DefaultConsistencyLevel.LOCAL_QUORUM)
    Flux<EntityModel> findAllByNamespaceAndEntityTypeAndHashBucket(String namespace, String entityType, int hashBucket);

    Mono<Void> deleteByNamespaceAndEntityTypeAndHashBucketAndEntityId(String namespace, String entityType, int hashBucket, String entityId);
}
