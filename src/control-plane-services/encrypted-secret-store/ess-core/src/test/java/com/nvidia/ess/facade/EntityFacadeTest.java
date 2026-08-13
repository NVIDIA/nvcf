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


import static com.nvidia.ess.constants.OpenTelemetryAttributes.PARTIAL_DELETE_TYPE_KEY;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.constants.OpenTelemetryAttributes.PartialDeleteType;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.persistence.services.EntityService;
import com.nvidia.ess.persistence.services.NamespaceService;
import com.nvidia.ess.persistence.services.SecretPathService;
import com.nvidia.ess.persistence.services.SecretVersionService;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.utils.EntityUtils;
import java.util.List;
import java.util.UUID;
import java.util.stream.IntStream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.MockitoAnnotations;
import org.mockito.junit.jupiter.MockitoExtension;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.context.ContextView;
@ExtendWith(MockitoExtension.class)
class EntityFacadeTest {

  @InjectMocks
  private EntityFacade entityFacade;

  @Mock
  private NamespaceService namespaceService;

  @Mock
  private EntityService entityService;

  @Mock
  private SecretPathService secretPathService;

  @Mock
  private SecretVersionService secretVersionService;

  @Mock
  private CustomMetricsRegistry customMetricsRegistry;

  @Mock
  private TelemetryComponents telemetryComponents;

  private final String mockNS = "ns1";
  private final String mockEntityType = "et1";
  private final String mockEntityId = "eid1";
  private final String mockPath = "/abc";

  private final String mockEntity = EntityUtils.getEntity(mockEntityType, mockEntityId);

  @BeforeEach
  void setup() {
    MockitoAnnotations.openMocks(this);
  }

  @Test
  void testDeleteEntity_listPathReturnEmpty_Success() {
    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType))
            .thenReturn(Mono.just(namespaceModel));
    when(secretPathService.getPaths(mockNS, mockEntity))
            .thenReturn(Flux.empty());
    when(entityService.deleteEntity(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.just(true));

    StepVerifier.create(entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId))
            .verifyComplete();

    verifyNoInteractions(telemetryComponents);
  }

  @Test
  void testDeleteEntity_pathsPartitionEmptiedEarlier_listPathsReturnOneRowWithStaticColumnValues_Success() {
    var namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType))
            .thenReturn(Mono.just(namespaceModel));

    var secretPathRowWithOnlyStaticColumns = Mockito.mock(SecretPathModel.class);
    doReturn(null).when(secretPathRowWithOnlyStaticColumns).getPath();

    when(secretPathService.getPaths(mockNS, mockEntity))
            .thenReturn(Flux.fromIterable(List.of(secretPathRowWithOnlyStaticColumns)));

    when(entityService.deleteEntity(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.just(true));

    StepVerifier.create(entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId))
            .verifyComplete();

    verifyNoInteractions(telemetryComponents);
  }

  @Test
  void testDeleteEntity_listPathError_Fail() {
    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType)).thenReturn(Mono.just(namespaceModel));

    when(secretPathService.getPaths(mockNS, mockEntity)).thenReturn(Flux.error(new RuntimeException("error on getPaths")));

    var result = entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId);

    StepVerifier.create(result).expectErrorSatisfies( err ->
            assertThat(err).isInstanceOf(RuntimeException.class)
                    .hasMessageContaining("error on getPaths")
    ).verify();

    verifyNoInteractions(telemetryComponents);
  }

  @ParameterizedTest
  @ValueSource(ints = {1, 10, 100})
  void testDeleteEntity_listPathReturn_N_Path_SecretDeleteResultRandom_Success(int numOfSecrets) {
    final String mockSecPath = "/abc/i-";

    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

    List<SecretPathModel> lstSecretPathModel =
            IntStream.range(0,numOfSecrets).mapToObj(i->Mockito.mock(SecretPathModel.class)).toList();

    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType))
            .thenReturn(Mono.just(namespaceModel));

    when(secretPathService.getPaths(mockNS, mockEntity))
            .thenReturn(Flux.range(0, numOfSecrets).map(lstSecretPathModel::get));

    for (int i=0; i<numOfSecrets; i++) {
      var curPath = mockSecPath + i;
      when(lstSecretPathModel.get(i).getPath()).thenReturn(curPath);
      when(secretVersionService.deleteSecretVersions(mockNS,mockEntity,curPath))
              .thenReturn(Mono.just(true));
    }
    when(secretPathService.deleteAll(mockNS, mockEntity))
            .thenReturn(Mono.just(true));

    when(entityService.deleteEntity(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.just(true));

    var result = entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId);
    StepVerifier.create(result).expectComplete().verify();

    verifyNoInteractions(telemetryComponents);
  }

  private SecretPathModel dummySecretPathModel(String secPath, Boolean isDir) {
    return
      SecretPathModel.builder()
        .path(secPath)
        .namespace(mockNS)
        .entity(mockEntity)
        .isDir(isDir)
        .entityVersion(UUID.randomUUID())
        .updatedAt(Uuids.timeBased())
        .build();
  }

  @Test
  void testDeleteEntity_onlyDirs_success() {
    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType))
            .thenReturn(Mono.just(namespaceModel));

    var pathA = mockPath + "a";
    var pathB = mockPath + "b";
    when(secretPathService.getPaths(mockNS, mockEntity)).thenReturn(
            Flux.just(
                    dummySecretPathModel(pathA, true),
                    dummySecretPathModel(pathB, true)));

    when(secretPathService.deleteAll(mockNS, mockEntity))
            .thenReturn(Mono.just(true));

    when(entityService.deleteEntity(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.just(true));

    var result = entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId);
    StepVerifier.create(result)
            .expectComplete()
            .verify();
    // correctly filter out dirs
    verifyNoInteractions(secretVersionService);

    verifyNoInteractions(telemetryComponents);
  }

  @Test
  void testDeleteEntity_deleteTwoSecretVersion_oneFail_Fail() {

    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType))
            .thenReturn(Mono.just(namespaceModel));

    var pathA = mockPath + "a";
    var pathB = mockPath + "b";
    SecretPathModel pathModelA = dummySecretPathModel(pathA, null);
    SecretPathModel pathModelB = dummySecretPathModel(pathB, false);
    when(secretPathService.getPaths(mockNS, mockEntity)).thenReturn(
            Flux.just(pathModelA, pathModelB));

    when(secretVersionService.deleteSecretVersions(mockNS, mockEntity, pathA))
            .thenReturn(Mono.just(true));
    when(secretVersionService.deleteSecretVersions(mockNS, mockEntity, pathB))
            .thenReturn(Mono.error(new RuntimeException("sec ver delete fail")));

    var result = entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId);
    StepVerifier.create(result)
            .expectErrorMatches(e -> e instanceof RuntimeException &&
                    e.getMessage().contains("sec ver delete fail"))
            .verify();

    verify(telemetryComponents)
        .setSpanAttribute(any(ContextView.class), eq(PARTIAL_DELETE_TYPE_KEY),
                eq(PartialDeleteType.SECRET_VERSION_ON_ENTITY.name()));
    verifyNoMoreInteractions(telemetryComponents);
  }

  @Test
  void testDeleteEntity_deleteSecretPathFail_Success() {

    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType))
            .thenReturn(Mono.just(namespaceModel));

    when(secretPathService.getPaths(mockNS, mockEntity))
            .thenReturn(Flux.just(dummySecretPathModel(mockPath, false)));
    when(secretVersionService.deleteSecretVersions(mockNS,mockEntity,mockPath)).thenReturn(Mono.just(true));
    when(secretPathService.deleteAll(mockNS,mockEntity))
            .thenReturn(Mono.error(new RuntimeException("sec path delete fail")));

    when(entityService.deleteEntity(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.just(true));

    var result = entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId);
    StepVerifier.create(result).expectComplete().verify();

    verify(telemetryComponents)
        .setSpanAttribute(any(ContextView.class), eq(PARTIAL_DELETE_TYPE_KEY),
                eq(PartialDeleteType.SECRET_PATH_ON_ENTITY.name()));
    verifyNoMoreInteractions(telemetryComponents);
  }

  @Test
  void testDeleteEntity_NamespaceNotFound_Returns204() {
    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType))
            .thenReturn(Mono.error(new NotFoundException("Namespace not found")));

    StepVerifier.create(entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId))
            .verifyComplete();

    verifyNoInteractions(telemetryComponents);
  }

  @Test
  void testDeleteEntity_GetPathsError_PropagatesError() {
    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType))
            .thenReturn(Mono.just(namespaceModel));
    when(secretPathService.getPaths(mockNS, mockEntity))
            .thenReturn(Flux.error(new RuntimeException("Error getting paths")));

    StepVerifier.create(entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId))
            .expectErrorMatches(throwable ->
                    throwable instanceof RuntimeException &&
                            throwable.getMessage().equals("Error getting paths"))
            .verify();

    verifyNoInteractions(telemetryComponents);
  }

  @Test
  void testDeleteEntity_secretVersionsAndPathsDeletionSucceeded_entityDeletionFailed_Returns204() {

    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType)).thenReturn(Mono.just(namespaceModel));
    when(secretPathService.getPaths(mockNS, mockEntity)).thenReturn(Flux.just(dummySecretPathModel(mockPath, false)));
    when(secretVersionService.deleteSecretVersions(mockNS, mockEntity, mockPath)).thenReturn(Mono.just(true));
    when(secretPathService.deleteAll(mockNS, mockEntity)).thenReturn(Mono.just(true));
    when(entityService.deleteEntity(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.error(new RuntimeException("Entity deletion failed")));

    var result = entityFacade.deleteEntity(mockNS, mockEntityType, mockEntityId);
    StepVerifier.create(result).expectComplete().verify();

    verify(telemetryComponents)
        .setSpanAttribute(any(ContextView.class), eq(PARTIAL_DELETE_TYPE_KEY),
                eq(PartialDeleteType.ENTITY_ON_ENTITY.name()));
    verifyNoMoreInteractions(telemetryComponents);
  }

  @Test
  void testEntityExists_entityServiceEntityExistsError_Failure() {
    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType)).thenReturn(Mono.just(namespaceModel));

    when(entityService.entityExists(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.error(new RuntimeException("delete entity fail!")));


    var result = entityFacade.entityExists(mockNS, mockEntityType, mockEntityId);

    StepVerifier.create(result).expectErrorMatches(e ->
            e.getMessage().contains("delete entity fail!")
    ).verify();

    verifyNoInteractions(telemetryComponents);
  }

  @Test
  void testEntityExists_entityServiceEntityExists_Exists() {
    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType)).thenReturn(Mono.just(namespaceModel));

    when(entityService.entityExists(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.just(Boolean.TRUE));

    var result = entityFacade.entityExists(mockNS, mockEntityType, mockEntityId);

    StepVerifier.create(result).expectComplete().verify();

    verifyNoInteractions(telemetryComponents);
  }

  @Test
  void testEntityExists_entityServiceEntityExists_NotExists() {
    EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

    when(namespaceService.getNamespaceWithValidEntityType(mockNS, mockEntityType)).thenReturn(Mono.just(namespaceModel));

    when(entityService.entityExists(mockNS, mockEntityType, mockEntityId, namespaceModel))
            .thenReturn(Mono.just(Boolean.FALSE));

    var result = entityFacade.entityExists(mockNS, mockEntityType, mockEntityId);

    StepVerifier.create(result).expectErrorSatisfies( err ->
            assertThat(err).isInstanceOf(NotFoundException.class)
                    .hasMessageContaining(String.format(Constants.MSG_ENTITY_NOT_FOUND, mockEntityType, mockEntityId))
    ).verify();

    verifyNoInteractions(telemetryComponents);
  }

}