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
import static com.nvidia.ess.facade.NamespaceFacade.DEFAULT_ENTITY_HASH_SIZE;
import static com.nvidia.ess.util.CustomObjectMatchers.nsModelMatchesExceptTimestamps;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertInstanceOf;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.controller.request.CreateNamespaceRequest;
import com.nvidia.ess.controller.response.EntityTypeInfo;
import com.nvidia.ess.controller.response.ListEntityTypesResponse;
import com.nvidia.ess.controller.response.NamespaceInfo;
import com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel;
import com.nvidia.ess.persistence.models.EntityTypeUdt;
import com.nvidia.ess.persistence.models.NamespaceModel;
import com.nvidia.ess.persistence.services.EntityTypeService;
import com.nvidia.ess.persistence.services.NamespaceService;
import java.time.Instant;
import java.util.HashMap;
import java.util.Map;
import java.util.Objects;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;
import org.mockito.junit.jupiter.MockitoExtension;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
class NamespaceFacadeTest {

    @Mock
    private NamespaceService namespaceService;

    @Mock
    private EntityTypeService entityTypeService;

    @InjectMocks
    private NamespaceFacade namespaceFacade;

    private NamespaceModel sampleModel;
    private EntityTypeInNamespaceModel sampleEntityTypeModel;
    private Map<String, EntityTypeUdt> entityTypes;

    @BeforeEach
    void setUp() {
        MockitoAnnotations.openMocks(this);

        entityTypes = new HashMap<>();
        sampleModel = NamespaceModel.builder()
                .namespace("testNamespace")
                .entityTypes(entityTypes)
                .entityHashSize(0)
                .previousEntityHashSize(0)
                .build();
        sampleEntityTypeModel = EntityTypeInNamespaceModel.builder()
                .namespace("testNamespace")
                .entityType(EntityTypeUdt.builder().name("existingEntityType").build())
                .entityHashSize(0)
                .previousEntityHashSize(0)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .build();
    }

    @Test
    void removeEntityType_NamespaceNotFound() {
        String missingNamespace = "missingNamespace";
        String entityType = "someEntityType";

        when(namespaceService.getNamespaceWithValidEntityType(missingNamespace, entityType)).thenReturn(Mono.error(() -> new NotFoundException("message")));
        StepVerifier.create(namespaceFacade.removeEntityType(missingNamespace, entityType))
                .expectNext()
                .verifyComplete();

        verifyNoInteractions(entityTypeService);
    }

    @Test
    void removeEntityType_GetNamespaceError() {
        String missingNamespace = "missingNamespace";
        String entityType = "someEntityType";

        when(namespaceService.getNamespaceWithValidEntityType(missingNamespace, entityType)).thenReturn(Mono.error(() -> new RuntimeException("message")));
        StepVerifier.create(namespaceFacade.removeEntityType(missingNamespace, entityType))
                // should catch only NotFoundException
                .expectError(RuntimeException.class)
                .verify();

        verifyNoInteractions(entityTypeService);
    }

    @Test
    void removeEntityType_Success() {
        String namespace = "kds";
        String entityType = "nca";

        EntityTypeUdt entityTypeUdt = EntityTypeUdt.builder().name(entityType).build();
        EntityTypeInNamespaceModel namespaceModel = EntityTypeInNamespaceModel.builder()
            .namespace(namespace)
            .notaryAuthorizations(null)
            .entityType(entityTypeUdt)
            .createdAt(Instant.now())
            .updatedAt(Instant.now())
            .entityHashSize(0)
            .previousEntityHashSize(0)
            .deletedAt(null)
            .build();

        when(namespaceService.getNamespaceWithValidEntityType(namespace, entityType)).thenReturn(Mono.just(namespaceModel));
        when(entityTypeService.update(namespace, entityTypeUdt)).thenReturn(
                Mono.just(true));
        StepVerifier.create(namespaceFacade.removeEntityType(namespace, entityType))
                .expectNext()
                .verifyComplete();
    }

    @Test
    void removeEntityType_serviceFailure() {
        String namespace = "kds";
        String entityType = "nca";

        EntityTypeUdt entityTypeUdt = EntityTypeUdt.builder().name(entityType).build();
        EntityTypeInNamespaceModel namespaceModel = EntityTypeInNamespaceModel.builder()
            .namespace(namespace)
            .notaryAuthorizations(null)
            .entityType(entityTypeUdt)
            .createdAt(Instant.now())
            .updatedAt(Instant.now())
            .entityHashSize(0)
            .previousEntityHashSize(0)
            .deletedAt(null)
            .build();

        when(namespaceService.getNamespaceWithValidEntityType(namespace, entityType)).thenReturn(Mono.just(namespaceModel));
        when(entityTypeService.update(namespace, entityTypeUdt)).thenReturn(
                Mono.error(new RuntimeException("entity type deletion failure!")));
        StepVerifier.create(namespaceFacade.removeEntityType(namespace, entityType))
                .expectErrorMatches(e -> e.getMessage().contains("entity type deletion failure!")).verify();
    }

    @Test
    void removeNamespace_NamespaceNotFound() {
        String missingNamespace = "missingNamespace";

        when(namespaceService.getNamespace(missingNamespace)).thenReturn(Mono.error(() -> new NotFoundException("message")));
        StepVerifier.create(namespaceFacade.removeNamespace(missingNamespace))
                .expectNext()
                .verifyComplete();
    }

    @Test
    void removeNamespace_GetNamespaceError() {
        String missingNamespace = "missingNamespace";

        when(namespaceService.getNamespace(missingNamespace)).thenReturn(Mono.error(() -> new RuntimeException("message")));
        StepVerifier.create(namespaceFacade.removeNamespace(missingNamespace))
                // should catch only NotFoundException
                .expectError(RuntimeException.class)
                .verify();
    }

    @Test
    void removeNamespace_Success() {
        String namespace = "kds";

        NamespaceModel namespaceModel = NamespaceModel.builder()
            .namespace(namespace)
            .notaryAuthorizations(null)
            .entityTypes(null)
            .createdAt(Instant.now())
            .updatedAt(Instant.now())
            .entityHashSize(0)
            .previousEntityHashSize(0)
            .deletedAt(null)
            .requireLWTForSecretVersionWrites(null)
            .build();

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));
        when(namespaceService.markForDeletion(namespace)).thenReturn(
                Mono.just(true));
        StepVerifier.create(namespaceFacade.removeNamespace(namespace))
                .expectNext()
                .verifyComplete();
    }

    @Test
    void removeNamespace_serviceFailure() {
        String namespace = "kds";

        NamespaceModel namespaceModel = NamespaceModel.builder()
            .namespace(namespace)
            .notaryAuthorizations(null)
            .entityTypes(null)
            .createdAt(Instant.now())
            .updatedAt(Instant.now())
            .entityHashSize(0)
            .previousEntityHashSize(0)
            .deletedAt(null)
            .requireLWTForSecretVersionWrites(null)
            .build();

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));
        when(namespaceService.markForDeletion(namespace)).thenReturn(
                Mono.error(new RuntimeException("namespace deletion failure!")));
        StepVerifier.create(namespaceFacade.removeNamespace(namespace))
                .expectErrorMatches(e -> e.getMessage().contains("namespace deletion failure!")).verify();
    }

    @Test
    void createEntityType_EntityTypeAlreadyExists_ShouldThrowConflictException() {
        entityTypes.put("existingEntityType", EntityTypeUdt.builder().name("existingEntityType").build());
        when(namespaceService.getNamespace("testNamespace"))
                .thenReturn(Mono.just(sampleModel));

        Mono<EntityTypeInfo> result = namespaceFacade.createEntityType("testNamespace", "existingEntityType");

        StepVerifier.create(result)
                .expectError(ConflictException.class)
                .verify();
    }
    
    @Test
    void createEntityType_UpdateFailed_ShouldReturnError() {
        when(namespaceService.getNamespace("testNamespace"))
                .thenReturn(Mono.just(sampleModel));
        when(entityTypeService.update(any(), any()))
                .thenReturn(Mono.error(new RuntimeException("Update failed")));

        Mono<EntityTypeInfo> result = namespaceFacade.createEntityType("testNamespace", "newEntityType");

        StepVerifier.create(result)
                .expectErrorMatches(throwable -> throwable instanceof RuntimeException && throwable.getMessage().equals("Update failed"))
                .verify();
    }

    @Test
    void createEntityType_EntityTypeTombstoned_ShouldThrowTombstonedException() {
        entityTypes.put("tombstonedEntityType", EntityTypeUdt.builder().name("tombstonedEntityType").deletedAt(Instant.now()).build());
        when(namespaceService.getNamespace("testNamespace"))
                .thenReturn(Mono.just(sampleModel));

        Mono<EntityTypeInfo> result = namespaceFacade.createEntityType("testNamespace", "tombstonedEntityType");

        StepVerifier.create(result)
                .expectErrorSatisfies(throwable -> {
                    assert throwable instanceof ConflictException;
                    assert throwable.getMessage().contains("Entity type 'tombstonedEntityType' is being deleted in the background. Please wait until the background process completes.");
                })
                .verify();
    }

    @Test
    void createEntityType_Success_ShouldReturnEntityTypeInfo() {
        when(namespaceService.getNamespace("testNamespace"))
                .thenReturn(Mono.just(sampleModel));
        when(entityTypeService.update(any(), any()))
                .thenReturn(Mono.just(true));

        Mono<EntityTypeInfo> result = namespaceFacade.createEntityType("testNamespace", "newEntityType");

        StepVerifier.create(result)
                .expectNextMatches(entityTypeInfo -> "newEntityType".equals(entityTypeInfo.getName()))
                .verifyComplete();
    }

    @Test
    void getEntityType_Success_ShouldReturnEntityTypeInfo() {
        when(namespaceService.getNamespaceWithValidEntityType(anyString(), anyString()))
                .thenReturn(Mono.just(sampleEntityTypeModel));

        Mono<EntityTypeInfo> result = namespaceFacade.getEntityType("testNamespace", "existingEntityType");

        StepVerifier.create(result)
                .expectNextMatches(entityTypeInfo -> "existingEntityType".equals(entityTypeInfo.getName()))
                .verifyComplete();
    }

    @Test
    void getEntityType_NotFound_ShouldThrowNotFoundException() {
        when(namespaceService.getNamespaceWithValidEntityType("testNamespace", "nonExistingEntityType"))
                .thenReturn(Mono.error(new NotFoundException("Entity type not found")));

        Mono<EntityTypeInfo> result = namespaceFacade.getEntityType("testNamespace", "nonExistingEntityType");

        StepVerifier.create(result)
                .expectError(NotFoundException.class)
                .verify();
    }

    @Test
    void listEntityTypes_Success_ShouldReturnEntityTypes() {
        Map<String, EntityTypeUdt> entityTypes = new HashMap<>();
        entityTypes.put("entityType1", EntityTypeUdt.builder().name("entityType1").build());
        entityTypes.put("entityType2", EntityTypeUdt.builder().name("entityType2").build());
        sampleModel.setEntityTypes(entityTypes);

        when(namespaceService.getNamespace(anyString()))
                .thenReturn(Mono.just(sampleModel));

        Mono<ListEntityTypesResponse> result = namespaceFacade.listEntityTypes("testNamespace");

        StepVerifier.create(result)
                .expectNextMatches(response -> response.getEntityTypes().size() == 2 &&
                        response.getEntityTypes().stream().anyMatch(e -> "entityType1".equals(e.getName())) &&
                        response.getEntityTypes().stream().anyMatch(e -> "entityType2".equals(e.getName())))
                .verifyComplete();
    }

    @Test
    void listEntityTypes_NoEntityTypes_ShouldReturnEmptyList() {
        when(namespaceService.getNamespace(anyString()))
                .thenReturn(Mono.just(sampleModel));

        Mono<ListEntityTypesResponse> result = namespaceFacade.listEntityTypes("testNamespace");

        StepVerifier.create(result)
                .expectNextMatches(response -> response.getEntityTypes().isEmpty())
                .verifyComplete();
    }

    @Test
    void listEntityTypes_WithDeletedEntries_ShouldFilterOutDeletedEntries() {
        Map<String, EntityTypeUdt> entityTypes = new HashMap<>();
        entityTypes.put("entityType1", EntityTypeUdt.builder().name("entityType1").build());
        entityTypes.put("entityType2", EntityTypeUdt.builder().name("entityType2").deletedAt(Instant.now()).build());
        sampleModel.setEntityTypes(entityTypes);

        when(namespaceService.getNamespace(anyString()))
                .thenReturn(Mono.just(sampleModel));

        Mono<ListEntityTypesResponse> result = namespaceFacade.listEntityTypes("testNamespace");

        StepVerifier.create(result)
                .expectNextMatches(response -> response.getEntityTypes().size() == 1 &&
                        response.getEntityTypes().stream().anyMatch(e -> "entityType1".equals(e.getName())) &&
                        response.getEntityTypes().stream().noneMatch(e -> "entityType2".equals(e.getName())))
                .verifyComplete();
    }

    @Test
    void getNamespace_namespaceFetchThrowsError_echoError() {
        doReturn(Mono.error(new RuntimeException("error during fetch")))
                .when(namespaceService)
                .getNamespace(TEST_NAMESPACE);

        StepVerifier.create(namespaceFacade.getNamespace(TEST_NAMESPACE))
                .expectError(RuntimeException.class)
                .verify();
    }

    @Test
    void getNamespace_namespaceNotFound_404() {
        doReturn(Mono.error(new NotFoundException("NS not found")))
                .when(namespaceService)
                .getNamespace(TEST_NAMESPACE);

        StepVerifier.create(namespaceFacade.getNamespace(TEST_NAMESPACE))
                .expectError(NotFoundException.class)
                .verify();
    }

    @Test
    void getNamespace_namespaceFound_returnInfo() {
        var expectedNsModel = NamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .entityHashSize(10)
                .requireLWTForSecretVersionWrites(false)
                .build();

        doReturn(Mono.just(expectedNsModel))
                .when(namespaceService)
                .getNamespace(TEST_NAMESPACE);

        StepVerifier.create(namespaceFacade.getNamespace(TEST_NAMESPACE))
                .expectNext(
                    NamespaceInfo.builder()
                            .namespace(TEST_NAMESPACE)
                            .createdAt(expectedNsModel.getCreatedAt())
                            .updatedAt(expectedNsModel.getUpdatedAt())
                            .build()
                )
                .expectComplete()
                .verify();
    }

    @Test
    void createNamespace_insertionSuccessful_returnSuccess() {
        var request = CreateNamespaceRequest.builder()
                .namespace(TEST_NAMESPACE)
                .build();
        
        var expectedNsModel = NamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .entityHashSize(DEFAULT_ENTITY_HASH_SIZE)
                .requireLWTForSecretVersionWrites(false)
                .build();

        doReturn(Mono.just(expectedNsModel))
                .when(namespaceService)
                .createNamespace(argThat(nsModelMatchesExceptTimestamps(expectedNsModel)));
        
        doReturn(Mono.empty()).when(namespaceService).getNamespaceWithoutFilter(eq(TEST_NAMESPACE));

        StepVerifier.create(namespaceFacade.createNamespace(request))
                .expectNextMatches(nsInfo -> Objects.equals(nsInfo.getNamespace(), TEST_NAMESPACE))
                .expectComplete()
                .verify();
    }

    @Test
    void createNamespace_errorDuringInsertion_echoError() {
        var request = CreateNamespaceRequest.builder()
                .namespace(TEST_NAMESPACE)
                .build();

        doReturn(Mono.error(new RuntimeException("error during insertion")))
                .when(namespaceService)
                .createNamespace(any());

        doReturn(Mono.empty()).when(namespaceService).getNamespaceWithoutFilter(eq(TEST_NAMESPACE));

        StepVerifier.create(namespaceFacade.createNamespace(request))
                .expectError(RuntimeException.class)
                .verify();
    }

    @Test
    void createNamespace_insertionDidNotComplete_tombstonedNamespaceFound_409() {
        var request = CreateNamespaceRequest.builder()
                .namespace(TEST_NAMESPACE)
                .build();

        doReturn(Mono.empty()).when(namespaceService).createNamespace(any());

        var preexistingNs = NamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .entityHashSize(10)
                .requireLWTForSecretVersionWrites(false)
                .deletedAt(Instant.now())
                .build();

        doReturn(Mono.just(preexistingNs)).when(namespaceService).getNamespaceWithoutFilter(eq(TEST_NAMESPACE));

        StepVerifier.create(namespaceFacade.createNamespace(request))
                .expectErrorSatisfies(ex -> {
                    assertInstanceOf(ConflictException.class, ex);
                    var expectedMsg = new ConflictException(String.format(MSG_NAMESPACE_BEING_DELETED, TEST_NAMESPACE))
                            .getMessage();
                    assertEquals(expectedMsg, ex.getMessage());
                })
                .verify();
    }

    @Test
    void createNamespace_insertionDidNotComplete_extantNamespaceFound_409() {
        var request = CreateNamespaceRequest.builder()
                .namespace(TEST_NAMESPACE)
                .build();

        doReturn(Mono.empty()).when(namespaceService).createNamespace(any());

        var preexistingNs = NamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .entityHashSize(10)
                .requireLWTForSecretVersionWrites(false)
                .build();

        doReturn(Mono.just(preexistingNs)).when(namespaceService).getNamespaceWithoutFilter(eq(TEST_NAMESPACE));

        StepVerifier.create(namespaceFacade.createNamespace(request))
                .expectErrorSatisfies(ex -> {
                    assertInstanceOf(ConflictException.class, ex);
                    var expectedMsg = new ConflictException(String.format(MSG_NAMESPACE_EXISTS, TEST_NAMESPACE))
                            .getMessage();
                    assertEquals(expectedMsg, ex.getMessage());
                })
                .verify();
    }

    @Test
    void createNamespace_insertionDidNotComplete_noNamespaceFoundAfterInsertionAttempt_409() {
        var request = CreateNamespaceRequest.builder()
                .namespace(TEST_NAMESPACE)
                .build();

        doReturn(Mono.empty()).when(namespaceService).createNamespace(any());

        doReturn(Mono.empty()).when(namespaceService).getNamespaceWithoutFilter(eq(TEST_NAMESPACE));

        StepVerifier.create(namespaceFacade.createNamespace(request))
                .expectErrorSatisfies(ex -> {
                    assertInstanceOf(ConflictException.class, ex);
                    var expectedMsg = new ConflictException(String.format(MSG_NAMESPACE_BEING_DELETED, TEST_NAMESPACE))
                            .getMessage();
                    assertEquals(expectedMsg, ex.getMessage());
                })
                .verify();
    }
}
