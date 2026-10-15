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
package com.nvidia.ess.facade;

import static com.nvidia.ess.constants.Constants.MSG_NAMESPACE_BEING_DELETED;
import static com.nvidia.ess.constants.Constants.MSG_NAMESPACE_EXISTS;

import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.controller.request.CreateNamespaceRequest;
import com.nvidia.ess.controller.response.EntityTypeInfo;
import com.nvidia.ess.controller.response.ListEntityTypesResponse;
import com.nvidia.ess.controller.response.ListNamespacesResponse;
import com.nvidia.ess.controller.response.NamespaceInfo;
import com.nvidia.ess.persistence.models.EntityTypeUdt;
import com.nvidia.ess.persistence.models.NamespaceModel;
import com.nvidia.ess.persistence.services.EntityService;
import com.nvidia.ess.persistence.services.EntityTypeService;
import com.nvidia.ess.persistence.services.NamespaceService;
import com.nvidia.ess.persistence.services.SecretPathService;
import com.nvidia.ess.persistence.services.SecretVersionService;
import java.time.Instant;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Mono;

@Slf4j
@Service
public class NamespaceFacade {


    @Setter(onMethod_ = {@Autowired})
    private SecretVersionService secretVersionService;

    @Setter(onMethod_ = {@Autowired})
    private SecretPathService secretPathService;

    @Setter(onMethod_ = {@Autowired})
    private EntityService entityService;

    @Setter(onMethod_ = {@Autowired})
    private EntityTypeService entityTypeService;

    @Setter(onMethod_ = {@Autowired})
    private NamespaceService namespaceService;

    public static final int DEFAULT_ENTITY_HASH_SIZE = 1000;

    public Mono<NamespaceInfo> createNamespace(CreateNamespaceRequest request) {
        return namespaceService.createNamespace(
                        NamespaceModel.builder()
                                .namespace(request.getNamespace())
                                .createdAt(Instant.now())
                                .updatedAt(Instant.now())
                                .entityHashSize(DEFAULT_ENTITY_HASH_SIZE)
                                .requireLWTForSecretVersionWrites(false)
                                .build()
                )
                .map(namespaceModel -> NamespaceInfo.builder()
                        .namespace(namespaceModel.getNamespace())
                        .createdAt(namespaceModel.getCreatedAt())
                        .updatedAt(namespaceModel.getUpdatedAt())
                        .build())
                .switchIfEmpty(
                        namespaceService.getNamespaceWithoutFilter(request.getNamespace())
                                .flatMap(existingNamespace -> {
                                    if (existingNamespace.getDeletedAt() != null) {
                                        // Namespace exists but is being deleted.
                                        return Mono.<NamespaceInfo>error(new ConflictException(String.format(MSG_NAMESPACE_BEING_DELETED, existingNamespace.getNamespace())));
                                    } else {
                                        // Namespace exists and is not deleted.
                                        return Mono.<NamespaceInfo>error(new ConflictException(String.format(MSG_NAMESPACE_EXISTS, existingNamespace.getNamespace())));
                                    }
                                })
                                // If a namespace-row is still missing in the DB, likely the namespace has
                                // just garbage collected.
                                .switchIfEmpty(Mono.<NamespaceInfo>error(new ConflictException(String.format(
                                        MSG_NAMESPACE_BEING_DELETED, request.getNamespace()
                                ))))
                );
    }

    public Mono<NamespaceInfo> getNamespace(String namespace) {
        return namespaceService.getNamespace(namespace)
                .flatMap(namespaceModel -> {
                    return Mono.just(NamespaceInfo.builder()
                            .namespace(namespaceModel.getNamespace())
                            .createdAt(namespaceModel.getCreatedAt())
                            .updatedAt(namespaceModel.getUpdatedAt())
                            .build());
                });
    }

    public Mono<ListNamespacesResponse> getNamespaces() {
        return namespaceService.getNamespaces()
                .filter(namespaceModel -> Objects.isNull(namespaceModel.getDeletedAt())) // Filter by deletedAt.
                .map(namespaceModel -> NamespaceInfo.builder()
                        .namespace(namespaceModel.getNamespace())
                        .createdAt(namespaceModel.getCreatedAt())
                        .updatedAt(namespaceModel.getUpdatedAt())
                        .build())
                .collectList() // Collect results into a List
                .map(listNamespaceInfo -> ListNamespacesResponse.builder().namespaces(listNamespaceInfo).build());
    }


    /**
     * Mark the namespace for deletion
     *
     * @param namespace to be deleted
     * @return Mono<Void>
     */
    public Mono<Void> removeNamespace(String namespace) {
        return namespaceService.getNamespace(namespace)
                .onErrorResume(NotFoundException.class, e -> {
                    log.debug("Noop. attempted to delete non existent namespace {}. {}",
                            namespace, e.getMessage());
                    return Mono.empty();
                })
                .flatMap(namespaceModel -> namespaceService.markForDeletion(namespace))
                .then();
    }

    public Mono<EntityTypeInfo> addEntityType(String namespace, String entityType) {
        return Mono.empty();

    }

    /**
     * Mark the entityType for deletion.
     *
     * @param namespace  in which the entity type to be deleted exists
     * @param entityType to be deleted
     * @return Mono<Void>
     */
    public Mono<Void> removeEntityType(String namespace, String entityType) {
        return namespaceService.getNamespaceWithValidEntityType(namespace, entityType)
                .onErrorResume(NotFoundException.class, e -> {
                    log.debug(
                            "Noop. attempted to delete non existent entity type {} in namespace {}. {}",
                            namespace, entityType, e.getMessage());
                    return Mono.empty();
                })
                .map(model -> model.getEntityType())
                .flatMap(entityTypeUdt -> {
                    entityTypeUdt.setDeletedAt(Instant.now());
                    return entityTypeService.update(namespace, entityTypeUdt);
                })
                .then();
    }

    public Mono<EntityTypeInfo> createEntityType(String namespace, String entityType) {
        return namespaceService.getNamespace(namespace)
                .flatMap(namespaceModel -> {
                    Map<String, EntityTypeUdt> entityTypes = namespaceModel.getEntityTypes();
                    if (entityTypes == null) {
                        entityTypes = new HashMap<>();
                        namespaceModel.setEntityTypes(entityTypes);
                    }
                    EntityTypeUdt existingEntityType = entityTypes.get(entityType);
                    if (existingEntityType != null) {
                        if (existingEntityType.getDeletedAt() != null) {
                            return Mono.error(new ConflictException("Entity type '" + entityType + "' is being deleted in the background. Please wait until the background process completes."));
                        } else {
                            return Mono.error(new ConflictException("Entity type '" + entityType + "' already exists. Please delete it first if you want to recreate it."));
                        }
                    }
                    return Mono.just(namespaceModel);
                })
                .flatMap(namespaceModel -> entityTypeService.update(namespace, EntityTypeUdt.builder().name(entityType).build()))
                .flatMap(ignored -> Mono.just(EntityTypeInfo.builder().name(entityType).build()));
    }

    public Mono<EntityTypeInfo> getEntityType(String namespace, String entityType) {
        return namespaceService.getNamespaceWithValidEntityType(namespace, entityType)
                .map(namespaceModel -> EntityTypeInfo.builder().name(entityType).build());
    }

    public Mono<ListEntityTypesResponse> listEntityTypes(String namespace) {
        return namespaceService.getNamespace(namespace)
                .map(namespaceModel -> {
                    // Extract entity types from the namespace model
                    Map<String, EntityTypeUdt> entityTypesMap = namespaceModel.getEntityTypes();
                    if (entityTypesMap == null) {
                        return ListEntityTypesResponse.builder().entityTypes(Collections.emptyList()).build();
                    }

                    // Filter out entries where deletedAt is not null and convert to EntityTypeInfo objects
                    List<EntityTypeInfo> entityTypes = entityTypesMap.entrySet().stream()
                            .filter(entry -> entry.getValue().getDeletedAt() == null)
                            .map(entry -> EntityTypeInfo.builder().name(entry.getKey()).build())
                            .toList();

                    // Build and return the ListEntityTypesResponse
                    return ListEntityTypesResponse.builder().entityTypes(entityTypes).build();
                });
    }
}
