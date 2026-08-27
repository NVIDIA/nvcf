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

import static com.nvidia.ess.constants.Constants.MSG_ENTITY_TYPE_NOT_FOUND;
import static com.nvidia.ess.constants.Constants.MSG_NAMESPACE_NOT_FOUND;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.constants.ErrorSubType;
import com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel;
import com.nvidia.ess.persistence.models.NamespaceModel;
import com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel;
import com.nvidia.ess.persistence.repositories.EntityTypeInNamespaceRepository;
import com.nvidia.ess.persistence.repositories.NamespaceRepository;
import com.nvidia.ess.persistence.repositories.NamespaceWithoutEntityTypesRepository;
import com.nvidia.ess.utils.ExceptionUtils;
import java.time.Instant;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import org.springframework.web.ErrorResponseException;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Slf4j
@Service
public class NamespaceService {

    @Setter(onMethod_ = {@Autowired})
    private NamespaceRepository repository;

    @Setter(onMethod_ = {@Autowired})
    private EntityTypeInNamespaceRepository entityTypeInNamespaceRepository;

    @Setter(onMethod_ = {@Autowired})
    private NamespaceWithoutEntityTypesRepository namespaceWithoutEntityTypesRepository;


    public Mono<NamespaceModel> createNamespace(NamespaceModel namespaceModel) {
        return repository.insertIfNotExists(namespaceModel).flatMap(success -> {
            if (!success) {
                return Mono.empty();
            }
            else {
                return Mono.just(namespaceModel);
            }
        });
    }

    public Mono<NamespaceModel> getNamespace(String namespace) {
        return getNamespace(namespace, NotFoundException.class);
    }

    /**
     * Get namespace
     *
     * @param namespace namespace
     * @param notFoundExClass custom exception class to throw if namespace not found
     * @return Mono(namespace) if exists or Mono.error if it does not exist. Never returns Mono.empty()
     */
    public Mono<NamespaceModel> getNamespace(String namespace,
            Class<? extends ErrorResponseException> notFoundExClass) {
        return repository.findByNamespace(namespace)
                .switchIfEmpty(Mono.error(
                        () -> ExceptionUtils.constructErrorResponseException(notFoundExClass,
                                String.format(MSG_NAMESPACE_NOT_FOUND, namespace),
                                ErrorSubType.NAMESPACE_NOT_FOUND)))
                .flatMap(namespaceModel -> {
                    if (namespaceModel.getDeletedAt() != null) {
                        return Mono.error(
                                () -> ExceptionUtils.constructErrorResponseException(
                                        notFoundExClass,
                                        String.format(MSG_NAMESPACE_NOT_FOUND, namespace),
                                        ErrorSubType.NAMESPACE_NOT_FOUND));
                    }
                    return Mono.just(namespaceModel);
                });
    }

    /**
     * Get namespace without thumbstone filtering or throwing exceptions.
     *
     * @param namespace namespace
     * @return Mono(namespace) if not exits returns Mono.empty()
     */
    public Mono<NamespaceModel> getNamespaceWithoutFilter(String namespace) {
        return repository.findByNamespace(namespace);
    }

    /**
     * Get namespace without the entity_types column.
     * <p>
     * This is an optimized query that fetches namespace data without deserializing
     * the potentially large entity_types map. Use this for operations that only need
     * authorization data (Non-Notary/Notary authorizations).
     * </p>
     *
     * @param namespace namespace
     * @return Mono(NamespaceWithoutEntityTypesModel) if exists or Mono.error if does not exist
     */
    public Mono<NamespaceWithoutEntityTypesModel> getNamespaceWithoutEntityTypes(String namespace) {
        return getNamespaceWithoutEntityTypes(namespace, NotFoundException.class);
    }

    /**
     * Get namespace without the entity_types column.
     * <p>
     * This is an optimized query that fetches namespace data without deserializing
     * the potentially large entity_types map. Use this for operations that only need
     * authorization data (Non-Notary/Notary authorizations).
     * </p>
     *
     * @param namespace namespace
     * @param notFoundExClass custom exception class to throw if namespace not found
     * @return Mono(NamespaceWithoutEntityTypesModel) if exists or Mono.error if does not exist
     */
    public Mono<NamespaceWithoutEntityTypesModel> getNamespaceWithoutEntityTypes(String namespace,
            Class<? extends ErrorResponseException> notFoundExClass) {
        return namespaceWithoutEntityTypesRepository.findByNamespace(namespace)
                .switchIfEmpty(Mono.error(
                        () -> ExceptionUtils.constructErrorResponseException(notFoundExClass,
                                String.format(MSG_NAMESPACE_NOT_FOUND, namespace),
                                ErrorSubType.NAMESPACE_NOT_FOUND)))
                .flatMap(model -> {
                    if (model.getDeletedAt() != null) {
                        return Mono.error(
                                () -> ExceptionUtils.constructErrorResponseException(
                                        notFoundExClass,
                                        String.format(MSG_NAMESPACE_NOT_FOUND, namespace),
                                        ErrorSubType.NAMESPACE_NOT_FOUND));
                    }
                    return Mono.just(model);
                });
    }

    // helper method since same business logic is used across different Facades.
    public Mono<EntityTypeInNamespaceModel> getNamespaceWithValidEntityType(String namespace,
            String entityType) {
        return getNamespaceWithValidEntityType(namespace, entityType, NotFoundException.class);
    }

    /**
     * Get namespace and validate existence of an entity type in a namespace.
     * <p>
     * This is an optimized query that fetches only the specific entity type from the entity_types map
     * using CQL map element selection syntax: {@code entity_types[:entityType]}.
     * This avoids deserializing the entire entity_types map which can be large in namespaces
     * with many entity types (including tombstoned ones).
     * </p>
     *
     * @param namespace namespace
     * @param entityType entity type
     * @param notFoundExClass custom exception class to throw if namespace/entity type not found
     * @return Mono(EntityTypeInNamespaceModel) if exists or Mono.error if does not exist. Never returns Mono.empty()
     */
    public Mono<EntityTypeInNamespaceModel> getNamespaceWithValidEntityType(String namespace, String entityType,
            Class<? extends ErrorResponseException> notFoundExClass) {
        return entityTypeInNamespaceRepository.findByNamespaceWithEntityType(namespace, entityType)
                .switchIfEmpty(Mono.error(
                        () -> ExceptionUtils.constructErrorResponseException(notFoundExClass,
                                String.format(MSG_NAMESPACE_NOT_FOUND, namespace),
                                ErrorSubType.NAMESPACE_NOT_FOUND)))
                .flatMap(model -> {
                    // Check if namespace is tombstoned
                    if (model.getDeletedAt() != null) {
                        return Mono.error(
                                () -> ExceptionUtils.constructErrorResponseException(
                                        notFoundExClass,
                                        String.format(MSG_NAMESPACE_NOT_FOUND, namespace),
                                        ErrorSubType.NAMESPACE_NOT_FOUND));
                    }
                    // Check if entity type exists (key was present in the map)
                    if (model.getEntityType() == null) {
                        return Mono.error(
                                () -> ExceptionUtils.constructErrorResponseException(
                                        notFoundExClass,
                                        String.format(MSG_ENTITY_TYPE_NOT_FOUND, entityType),
                                        ErrorSubType.ENTITY_TYPE_NOT_FOUND));
                    }
                    // Check if entity type is tombstoned
                    if (model.getEntityType().getDeletedAt() != null) {
                        return Mono.error(
                                () -> ExceptionUtils.constructErrorResponseException(
                                        notFoundExClass,
                                        String.format(MSG_ENTITY_TYPE_NOT_FOUND, entityType),
                                        ErrorSubType.ENTITY_TYPE_NOT_FOUND));
                    }
                    return Mono.just(model);
                });
    }

    public Flux<NamespaceModel> getNamespaces() {
        return repository.findAll();
    }

    public Mono<Boolean> markForDeletion(String namespace) {
        return repository.updateDeletedAt(namespace, Instant.now())
                .thenReturn(true);
    }
}
