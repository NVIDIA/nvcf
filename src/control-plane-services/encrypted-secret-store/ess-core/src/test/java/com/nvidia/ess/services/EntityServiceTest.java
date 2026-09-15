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
package com.nvidia.ess.services;

import static com.nvidia.ess.util.TestConstants.TEST_ENTITY_HASH_SIZE;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY_ID;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY_TYPE;
import static com.nvidia.ess.util.TestConstants.TEST_HASH_BUCKET;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;

import com.nvidia.ess.persistence.models.EntityModel;
import com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel;
import com.nvidia.ess.persistence.models.EntityTypeUdt;
import com.nvidia.ess.persistence.repositories.EntityRepository;
import com.nvidia.ess.persistence.services.EntityService;
import com.nvidia.ess.util.CustomObjectMatchers;
import java.time.Instant;
import java.util.Objects;
import java.util.function.Consumer;
import lombok.Getter;
import lombok.NoArgsConstructor;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
public class EntityServiceTest {
  
  @Mock
  private EntityRepository repository;

  @InjectMocks
  private EntityService entityService;

  private static EntityModel entityInstance() {
    return EntityModel.builder()
        .namespace(TEST_NAMESPACE)
        .entityType(TEST_ENTITY_TYPE)
        .hashBucket(TEST_HASH_BUCKET)
        .entityId(TEST_ENTITY_ID)
        .createdAt(Instant.now())
        .build();
  }

  private EntityTypeInNamespaceModel namespaceInstance() {
    return EntityTypeInNamespaceModel.builder()
        .namespace(TEST_NAMESPACE)
        .entityType(EntityTypeUdt.builder().name(TEST_ENTITY_TYPE).build())
        .entityHashSize(TEST_ENTITY_HASH_SIZE)
        .previousEntityHashSize(1)
        .createdAt(Instant.now())
        .updatedAt(Instant.now())
        .build();
  }

  @NoArgsConstructor
  private static class ExecRecorder<T> implements Consumer<T> {

    @Getter
    private boolean called = false;

    @Override
    public void accept(T t) {
      called = true;
    }
  }

  @Test
  void testCreateEntityIfNotExists_repositoryFailsEntityFetch_echoError() {

    var namespaceModel = namespaceInstance();
    var entityModel = entityInstance();

    // Repository fetch returns an error.
    doReturn(Mono.error(new RuntimeException("error during DB fetch")))
        .when(repository)
        .findByNamespaceAndEntityTypeAndHashBucketAndEntityId(any(), any(), anyInt(), any());
    // Mocking this as it does get called (the value emitted by the `Mono` doesn't matter).
    var recordSaveExec = new ExecRecorder<Object>();
    doReturn(Mono.empty().doOnSuccess(recordSaveExec)).when(repository).insertEntity(any());
    
    var verifier = StepVerifier.create(
        entityService.createEntityIfNotExists(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID, namespaceModel)
    );
    verifier.expectError(RuntimeException.class).verify();
    // Verify that the `Mono` returned by `insertEntity()` wasn't subscribed (and therefore the
    // save itself wasn't executed).
    assertFalse(recordSaveExec.isCalled());

    verify(repository, times(1))
        .findByNamespaceAndEntityTypeAndHashBucketAndEntityId(
            eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_HASH_BUCKET), eq(TEST_ENTITY_ID)
        );
    verify(repository, times(1)).insertEntity(
        argThat(CustomObjectMatchers.entityModelMatchesExceptTimestamps(entityModel))
    );
  }

  @Test
  void testCreateEntityIfNotExists_repositoryFetchReturnsExistingEntity_dontCreateNewEntity() {

    var namespaceModel = namespaceInstance();
    var entityInDB = entityInstance();

    // Repository fetch returns a non-empty result.
    doReturn(Mono.just(entityInDB))
        .when(repository)
        .findByNamespaceAndEntityTypeAndHashBucketAndEntityId(any(), any(), anyInt(), any());
    // Mocking this as it does get called (the value emitted by the `Mono` doesn't matter).
    var recordSaveExec = new ExecRecorder<Object>();
    doReturn(Mono.empty().doOnSuccess(recordSaveExec)).when(repository).insertEntity(any());

    // Repository fetch succeeds and `createEntityIfNotExists()` returns the fetched entity.
    var verifier = StepVerifier.create(
        entityService.createEntityIfNotExists(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID, namespaceModel)
    );
    verifier
        .expectNextMatches(fetchedEntity -> Objects.equals(entityInDB, fetchedEntity))
        .expectComplete()
        .verify();
    // Verify that the `Mono` returned by `insertEntity()` wasn't subscribed (and therefore the
    // save itself wasn't executed).
    assertFalse(recordSaveExec.isCalled());

    verify(repository, times(1))
        .findByNamespaceAndEntityTypeAndHashBucketAndEntityId(
            eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_HASH_BUCKET), eq(TEST_ENTITY_ID)
        );
    verify(repository, times(1)).insertEntity(
        argThat(CustomObjectMatchers.entityModelMatchesExceptTimestamps(entityInDB))
    );
  }

  @Test
  void testCreateEntityIfNotExists_repositoryFetchReturnsEmptyResult_createNewEntity() {
    var namespaceModel = namespaceInstance();
    var entityModel = entityInstance();

    // Repository fetch returns an empty result.
    doReturn(Mono.empty())
        .when(repository)
        .findByNamespaceAndEntityTypeAndHashBucketAndEntityId(any(), any(), anyInt(), any());
    // `insertEntity()` DOES get executed this time.
    var recordSaveExec = new ExecRecorder<EntityModel>();
    doReturn(Mono.just(entityModel).doOnSuccess(recordSaveExec))
        .when(repository)
        .insertEntity(argThat(CustomObjectMatchers.entityModelMatchesExceptTimestamps(entityModel)));

    // Repository fetch succeeds but returns an empty result, but insertEntity() was called later.
    var verifier = StepVerifier.create(
        entityService.createEntityIfNotExists(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID, namespaceModel)
    );
    verifier.expectNextCount(1).expectComplete().verify();
    // Verify that the `Mono` returned by `insertEntity()` WAS subscribed (and therefore the
    // save itself WAS executed).
    assertTrue(recordSaveExec.isCalled());

    verify(repository, times(1))
        .findByNamespaceAndEntityTypeAndHashBucketAndEntityId(
            eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_HASH_BUCKET), eq(TEST_ENTITY_ID)
        );
    verify(repository, times(1)).insertEntity(
        argThat(CustomObjectMatchers.entityModelMatchesExceptTimestamps(entityModel))
    );
  }
}
