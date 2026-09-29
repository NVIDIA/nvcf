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
package com.nvidia.ess.persistence.services;

import com.nvidia.ess.persistence.models.EntityModel;
import com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel;
import com.nvidia.ess.persistence.repositories.EntityRepository;
import com.nvidia.ess.utils.EntityUtils;
import java.time.Instant;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Mono;

@Slf4j
@Service
public class EntityService {
    @Setter(onMethod_ = {@Autowired})
    private EntityRepository entityRepository;

    public Mono<EntityModel> createEntityIfNotExists(String namespace, String entityType, String entityId,
            EntityTypeInNamespaceModel namespaceModel) {
        int bucket = EntityUtils.getEntityIdBucket(entityId, namespaceModel.getEntityHashSize());
        var model = EntityModel.builder()
                .namespace(namespace)
                .entityType(entityType)
                .hashBucket(bucket)
                .entityId(entityId)
                .createdAt(Instant.now())
                .build();

        // Obtain an `EntityModel` from storage if the entity already exists.
        return entityRepository.findByNamespaceAndEntityTypeAndHashBucketAndEntityId(namespace, entityType, bucket, entityId)
                // Otherwise, create one, persist it and return the persisted `EntityModel`.
                .switchIfEmpty(entityRepository.insertEntity(model)
                        .thenReturn(model));
    }

    public Mono<EntityModel> getEntity(String namespace, String entityType, String entityId,
            EntityTypeInNamespaceModel namespaceModel) {
        int bucket = rehashEntityIfNeededAndReturnBucket(entityId, namespaceModel);
        return entityRepository.findByNamespaceAndEntityTypeAndHashBucketAndEntityId(namespace, entityType, bucket, entityId);
    }

    // Note: this function always returns true. The caller should ignore the value.
    // The underlying repository call returns an empty mono, but this function returns a boolean
    // mono to avoid propagating emptiness and to help with chaining subsequent reactive calls.
    public Mono<Boolean> deleteEntity(String namespace, String entityType, String entityId,
            EntityTypeInNamespaceModel namespaceModel) {
        return entityRepository.deleteByNamespaceAndEntityTypeAndHashBucketAndEntityId(
                        namespace,
                        entityType,
                        EntityUtils.getEntityIdBucket(entityId, namespaceModel.getEntityHashSize()),
                        entityId
                )
                .thenReturn(true);
    }

    public Mono<Boolean> entityExists(String namespace, String entityType, String entityId,
            EntityTypeInNamespaceModel namespaceModel) {
        int bucket = rehashEntityIfNeededAndReturnBucket(entityId, namespaceModel);

        return entityRepository.existsByNamespaceAndEntityTypeAndHashBucketAndEntityId(namespace, entityType, bucket, entityId);
    }

    private int rehashEntityIfNeededAndReturnBucket(String entityId, EntityTypeInNamespaceModel namespaceModel) {
        // TODO rehashing logic
        return EntityUtils.getEntityIdBucket(entityId, namespaceModel.getEntityHashSize());
    }
}
