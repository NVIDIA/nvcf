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

import static com.nvidia.ess.constants.OpenTelemetryAttributes.LWT_WRITE_FAILURE_OPERATION_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.PARTIAL_DELETE_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_CAS_ERROR_PROVIDED_VERSION_KEY;
import static com.nvidia.ess.util.TestConstants.TEST_CREATE_SECRET_REQUEST_CAS_VERSION;
import static com.nvidia.ess.util.TestConstants.TEST_CREATE_SECRET_REQUEST_PAYLOAD;
import static com.nvidia.ess.util.TestConstants.TEST_CREATE_SECRET_SUCCESS_NEW_VERSION;
import static com.nvidia.ess.util.TestConstants.TEST_DB_NO_NODE_AVAILABLE_EXCEPTION;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY_ID;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY_TYPE;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static com.nvidia.ess.util.TestConstants.TEST_NEK_ID;
import static com.nvidia.ess.util.TestConstants.TEST_PROBLEM_SUMMARY;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_DATA_CIPHERTEXT;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_VERSION;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyBoolean;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import tools.jackson.core.type.TypeReference;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.constants.OpenTelemetryAttributes.PartialDeleteType;
import com.nvidia.ess.controller.request.CreateSecretRequest;
import com.nvidia.ess.controller.request.CreateSecretRequest.Options;
import com.nvidia.ess.controller.response.kv2.SecretInfo;
import com.nvidia.ess.controller.response.kv2.SecretResponse;
import com.nvidia.ess.controller.response.kv2.SecretVersionMetadata;
import com.nvidia.ess.exceptions.AnomalyException;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.facade.SecretFacade.NotFoundIgnoreException;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.metrics.CustomMetricsRegistry.LwtOperation;
import com.nvidia.ess.persistence.models.EntityModel;
import com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel;
import com.nvidia.ess.persistence.models.EntityTypeUdt;
import com.nvidia.ess.persistence.models.SecretPathModel;
import com.nvidia.ess.persistence.models.SecretVersionModel;
import com.nvidia.ess.persistence.services.EntityService;
import com.nvidia.ess.persistence.services.NamespaceService;
import com.nvidia.ess.persistence.services.SecretPathService;
import com.nvidia.ess.persistence.services.SecretVersionService;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.utils.EntityUtils;
import com.nvidia.ess.utils.namedtuples.SecretPathWriteArgs;
import com.nvidia.ess.encryption.crypto.CryptoService;
import com.nvidia.ess.encryption.exceptions.BadJWEException;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Predicate;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import org.apache.commons.lang3.tuple.Pair;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.ArgumentMatcher;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.web.ErrorResponseException;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.context.ContextView;

@ExtendWith(MockitoExtension.class)
class SecretFacadeTest {

    @InjectMocks
    private SecretFacade secretFacade;

    @Mock
    private NamespaceService namespaceService;

    @Mock
    private EntityService entityService;

    @Mock
    private CryptoService cryptoService;

    @Mock
    private SecretPathService secretPathService;

    @Mock
    private SecretVersionService secretVersionService;

    @Mock
    private CustomMetricsRegistry customMetricsRegistry;

    @Mock
    private TelemetryComponents telemetryComponents;

    @Test
    void testDeleteSecret_namespaceNotFound_Failure() {
        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE, NotFoundIgnoreException.class)).thenReturn(Mono.error(() -> new NotFoundIgnoreException("message")));
        var result = secretFacade.deleteSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                TEST_SECRET_PATH);

        StepVerifier.create(result).expectComplete().verify();

        verifyNoInteractions(telemetryComponents);
    }

    @Test
    void testDeleteSecret_namespaceError_Failure() {
        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE, NotFoundIgnoreException.class)).thenReturn(
                Mono.error(new RuntimeException(TEST_DB_NO_NODE_AVAILABLE_EXCEPTION)));
        var result = secretFacade.deleteSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                TEST_SECRET_PATH);

        StepVerifier.create(result).expectErrorSatisfies(
                err -> assertThat(err).isInstanceOf(RuntimeException.class)
                        .hasMessageContaining(TEST_DB_NO_NODE_AVAILABLE_EXCEPTION)).verify();

        verifyNoInteractions(telemetryComponents);
    }

    @Test
    void testDeleteSecret_deleteSecretVersionError_Failure() {
        EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE, NotFoundIgnoreException.class)).thenReturn(Mono.just(namespaceModel));

        when(secretVersionService.deleteSecretVersions(TEST_NAMESPACE, TEST_ENTITY,
                TEST_SECRET_PATH)).thenReturn(
                Mono.error(new RuntimeException(TEST_DB_NO_NODE_AVAILABLE_EXCEPTION)));

        var result = secretFacade.deleteSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                TEST_SECRET_PATH);
        StepVerifier.create(result).expectErrorSatisfies(
                err -> assertThat(err).isInstanceOf(RuntimeException.class)
                        .hasMessageContaining(TEST_DB_NO_NODE_AVAILABLE_EXCEPTION)).verify();

        verifyNoInteractions(telemetryComponents);
    }

    @Test
    void testDeleteSecret_deleteSecretPathAndCleanupEmptyAncestorDirPathsError_Success() {

        EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE, NotFoundIgnoreException.class)).thenReturn(Mono.just(namespaceModel));

        when(secretVersionService.deleteSecretVersions(TEST_NAMESPACE, TEST_ENTITY,
                TEST_SECRET_PATH)).thenReturn(Mono.just(true));

        when(secretPathService.deleteSecretPathAndEmptyAncestorDirs(TEST_NAMESPACE, TEST_ENTITY, TEST_SECRET_PATH))
                .thenReturn(Mono.error(new RuntimeException(TEST_DB_NO_NODE_AVAILABLE_EXCEPTION)));

        var result = secretFacade.deleteSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                TEST_SECRET_PATH);

        StepVerifier.create(result).expectComplete().verify();

        verify(telemetryComponents)
                .setSpanAttribute(any(ContextView.class), eq(PARTIAL_DELETE_TYPE_KEY),
                        eq(PartialDeleteType.SECRET_PATH_ON_SECRET.name()));
        verifyNoMoreInteractions(telemetryComponents);
    }

    @Test
    void testDeleteSecret_deleteSecretPathAndCleanupEmptyAncestorDirPathsReturnFalse_Success() {

        EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE, NotFoundIgnoreException.class)).thenReturn(Mono.just(namespaceModel));

        when(secretVersionService.deleteSecretVersions(TEST_NAMESPACE, TEST_ENTITY,
                TEST_SECRET_PATH)).thenReturn(Mono.just(true));

        when(secretPathService.deleteSecretPathAndEmptyAncestorDirs(TEST_NAMESPACE, TEST_ENTITY, TEST_SECRET_PATH))
                .thenReturn(Mono.just(false));

        var result = secretFacade.deleteSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                TEST_SECRET_PATH);

        StepVerifier.create(result).expectComplete().verify();

        verify(telemetryComponents)
                .setSpanAttribute(any(ContextView.class), eq(PARTIAL_DELETE_TYPE_KEY),
                        eq(PartialDeleteType.SECRET_PATH_CAS_ON_SECRET.name()));
        verify(telemetryComponents)
                .setSpanAttribute(any(ContextView.class), eq(LWT_WRITE_FAILURE_OPERATION_KEY),
                        eq(LwtOperation.PATH_DELETION.name()));
        verifyNoMoreInteractions(telemetryComponents);
    }

    @Test
    void testDeleteSecret_Success() {
        EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE, NotFoundIgnoreException.class)).thenReturn(Mono.just(namespaceModel));

        when(secretVersionService.deleteSecretVersions(TEST_NAMESPACE, TEST_ENTITY,
                TEST_SECRET_PATH)).thenReturn(Mono.just(true));

        when(secretPathService.deleteSecretPathAndEmptyAncestorDirs(TEST_NAMESPACE, TEST_ENTITY, TEST_SECRET_PATH))
                .thenReturn(Mono.just(true));

        var result = secretFacade.deleteSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                TEST_SECRET_PATH);

        StepVerifier.create(result).expectComplete().verify();

        verifyNoInteractions(telemetryComponents);
    }

    @Test
    void testListSecretVersions_secretVersionServiceGetVersionsError_Failure() {
        EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE)).thenReturn(Mono.just(namespaceModel));

        when(secretVersionService.getSecretVersions(TEST_NAMESPACE, EntityUtils.getEntity(TEST_ENTITY_TYPE, TEST_ENTITY_ID), TEST_SECRET_PATH))
                .thenReturn(Flux.error(new RuntimeException("get secret versions failed")));

        var result = secretFacade.getSecretVersions(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID, TEST_SECRET_PATH);

        StepVerifier.create(result).expectErrorMatches(e ->
                e.getMessage().contains("get secret versions failed")
        ).verify();

        verifyNoInteractions(telemetryComponents);
    }

    @Test
    void testListSecretVersions_secretVersionServiceGetVersions_Success() {
        EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);

        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE)).thenReturn(Mono.just(namespaceModel));

        List<SecretVersionModel> versions = List.of(
                SecretVersionModel.builder()
                        .namespace(TEST_NAMESPACE)
                        .entity(EntityUtils.getEntity(TEST_ENTITY_TYPE, TEST_ENTITY_ID))
                        .secretPath(TEST_SECRET_PATH)
                        .version(UUID.randomUUID())
                        .currentVersion(UUID.randomUUID())
                        .value("secretValue1")
                        .createdAt(Instant.now())
                        .build(),
                SecretVersionModel.builder()
                        .namespace(TEST_NAMESPACE)
                        .entity(EntityUtils.getEntity(TEST_ENTITY_TYPE, TEST_ENTITY_ID))
                        .secretPath(TEST_SECRET_PATH)
                        .version(UUID.randomUUID())
                        .currentVersion(UUID.randomUUID())
                        .value("secretValue2")
                        .createdAt(Instant.now())
                        .build(),
                SecretVersionModel.builder()
                        .namespace(TEST_NAMESPACE)
                        .entity(EntityUtils.getEntity(TEST_ENTITY_TYPE, TEST_ENTITY_ID))
                        .secretPath(TEST_SECRET_PATH)
                        .version(UUID.randomUUID())
                        .currentVersion(UUID.randomUUID())
                        .value("secretValue3")
                        .createdAt(Instant.now())
                        .build(),
                SecretVersionModel.builder()
                        .namespace(TEST_NAMESPACE)
                        .entity(EntityUtils.getEntity(TEST_ENTITY_TYPE, TEST_ENTITY_ID))
                        .secretPath(TEST_SECRET_PATH)
                        .version(UUID.randomUUID())
                        .currentVersion(UUID.randomUUID())
                        .value("secretValue4")
                        .createdAt(Instant.now())
                        .build(),
                SecretVersionModel.builder()
                        .namespace(TEST_NAMESPACE)
                        .entity(EntityUtils.getEntity(TEST_ENTITY_TYPE, TEST_ENTITY_ID))
                        .secretPath(TEST_SECRET_PATH)
                        .version(UUID.randomUUID())
                        .currentVersion(UUID.randomUUID())
                        .value("secretValue5")
                        .createdAt(Instant.now())
                        .build()
        );

        when(secretVersionService.getSecretVersions(TEST_NAMESPACE, EntityUtils.getEntity(TEST_ENTITY_TYPE, TEST_ENTITY_ID), TEST_SECRET_PATH))
                .thenReturn(Flux.fromIterable(versions));

        Mono<SecretResponse> result = secretFacade.getSecretVersions(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID, TEST_SECRET_PATH);

        StepVerifier.create(result)
                .assertNext(response -> {
                    assertNotNull(response);
                    assertEquals(5, response.getData().getKeys().size());
                    for (int i = 0; i < versions.size(); i++) {
                        assertEquals(versions.get(i).getVersion().toString(), response.getData().getKeys().get(i));
                    }
                })
                .verifyComplete();

        verifyNoInteractions(telemetryComponents);
    }

    static final Map<String, Boolean> secretPaths = Map.of(
            "a", true,
            "a/b", true,
            "a/b/c", true,
            "a/b/d", false,
            "a/b/c/e", true,
            "a/b/c/f", false,
            "a/b/c/g", false,
            "x", false
    );

    static Stream<Arguments> secretsPathsFilteringParams() {
        return Stream.of(
                Arguments.of(secretPaths, "", Set.of("a/", "x")),
                Arguments.of(secretPaths, "a", Set.of("a/b/")),
                Arguments.of(secretPaths, "a/b", Set.of("a/b/c/", "a/b/d")),
                Arguments.of(secretPaths, "a/b/c", Set.of("a/b/c/e/", "a/b/c/f", "a/b/c/g")),
                Arguments.of(secretPaths, "a/b/d", Set.of()),
                Arguments.of(secretPaths, "a/b/c/e", Set.of()),
                Arguments.of(secretPaths, "a/b/c/f", Set.of()),
                Arguments.of(secretPaths, "a/b/c/g", Set.of()),
                Arguments.of(secretPaths, "x", Set.of()),
                Arguments.of(Map.of(), "x", Set.of()),
                Arguments.of(null, "x", Set.of())
        );
    }

    @ParameterizedTest
    @MethodSource("secretsPathsFilteringParams")
    void testListSecretPaths_Success(@Nullable Map<String, Boolean> storedSecretPaths, String partialPath, Set<String> expectedFilteredPaths) {
        EntityTypeInNamespaceModel namespaceModel = Mockito.mock(EntityTypeInNamespaceModel.class);
        Set<String> expectedFilteredFullPaths = expectedFilteredPaths.stream()
                .map(path -> String.format("%s/%s", TEST_ENTITY, path))
                .collect(Collectors.toSet());

        var entityVersion = Uuids.timeBased();

        when(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE)).thenReturn(Mono.just(namespaceModel));
        if (Objects.isNull(storedSecretPaths)) {

            // A `null` value of `storedSecretPaths` represents an entity-paths partition all of whose
            // previously existing paths were removed and is now empty, but has lingering static columns
            // (`entity_version`) indexed by a partition-key (namespace, entity). The output of
            // `secretPathService.getPaths()` in this scenario would be a single "row" with null-values for
            // all non-partition-key, non-static columns. This "row" should be skipped from the listing.
            var rowToIgnoreInEmptyPartition = Mockito.mock(SecretPathModel.class);
            doReturn(null).when(rowToIgnoreInEmptyPartition).getPath();

            when(secretPathService.getPaths(TEST_NAMESPACE, TEST_ENTITY))
                    .thenReturn(Flux.fromStream(Stream.of(rowToIgnoreInEmptyPartition)));

        } else {

            when(secretPathService.getPaths(TEST_NAMESPACE, TEST_ENTITY)).thenReturn(
                    Flux.fromStream(storedSecretPaths.entrySet().stream()
                            .map(pair -> SecretPathModel.builder()
                                    .namespace(TEST_NAMESPACE)
                                    .entity(TEST_ENTITY)
                                    .path(pair.getKey())
                                    .entityVersion(entityVersion)
                                    .isDir(pair.getValue())
                                    .updatedAt(Uuids.timeBased())
                                    .build())
                    )
            );
        }

        Mono<SecretResponse> actualMono = secretFacade.getSecretPaths(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID, partialPath);

        StepVerifier.create(actualMono)
                .assertNext(response -> {
                    assertNotNull(response.getData());
                    assertNotNull(response.getData().getKeys());
                    assertNull(response.getData().getData());
                    assertNull(response.getData().getMetadata());
                    assertEquals(expectedFilteredFullPaths, new HashSet<>(response.getData().getKeys()));
                })
                .verifyComplete();

        verifyNoInteractions(telemetryComponents);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void testGetSecret_namespaceWithEntityTypeNotFound_notFoundException(boolean versionProvided) {

        doReturn(Mono.error(new NotFoundException("Valid NS with entity-type not found")))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE));

        StepVerifier.create(secretFacade.getSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, versionProvided ? TEST_SECRET_VERSION : null))
                .expectError(NotFoundException.class)
                .verify();

        verify(namespaceService, times(1)).getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE));
        verifyNoInteractions(secretVersionService, cryptoService);

        verifyNoInteractions(telemetryComponents);
    }

    @Test
    void testGetSecret_invalidUuidVersion_notFoundException() {

        var namespaceModel = EntityTypeInNamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .entityHashSize(1)
                .previousEntityHashSize(1)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .entityType(EntityTypeUdt.builder().name(TEST_ENTITY_TYPE).build())
                .build();

        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE);

        StepVerifier.create(secretFacade.getSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, UUID.randomUUID()))
                .expectError(NotFoundException.class)
                .verify();

        verify(namespaceService, times(1)).getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE);
        verifyNoInteractions(secretVersionService, cryptoService);

        verifyNoInteractions(telemetryComponents);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void testGetSecret_validNamespaceAndEntitTypeExists_secretRetrievalEmpty_notFoundException(boolean versionProvided) {

        var namespaceModel = EntityTypeInNamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .entityHashSize(1)
                .previousEntityHashSize(1)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .entityType(EntityTypeUdt.builder().name(TEST_ENTITY_TYPE).build())
                .build();

        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE));

        if (versionProvided) {
            doReturn(Mono.error(new NotFoundException("secret not found")))
                    .when(secretVersionService)
                    .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH),
                            eq(TEST_SECRET_VERSION), eq(NotFoundException.class));
        } else {
            doReturn(Mono.error(new NotFoundException("secret not found")))
                    .when(secretVersionService)
                    .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH),
                            eq(NotFoundException.class));
        }

        StepVerifier.create(secretFacade.getSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, versionProvided ? TEST_SECRET_VERSION : null))
                .expectError(NotFoundException.class)
                .verify();

        verify(namespaceService, times(1)).getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE));

        verify(secretVersionService, versionProvided ? times(1) : never())
                .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(TEST_SECRET_VERSION),
                        eq(NotFoundException.class));

        verify(secretVersionService, !versionProvided ? times(1) : never())
                .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH),
                        eq(NotFoundException.class));

        verifyNoInteractions(cryptoService, telemetryComponents);
    }

    private static Stream<Arguments> argsTestGetSecretDecryptionThrowsExceptionsAndExpectedException() {
        return Stream.of(
                Arguments.of(BadJWEException.class, AnomalyException.class),
                Arguments.of(MissingMasterKeyException.class, MissingMasterKeyException.class),
                Arguments.of(MissingKeyException.class, AnomalyException.class),
                Arguments.of(EncryptionException.class, EncryptionException.class)
        );
    }


    @ParameterizedTest
    @MethodSource("argsTestGetSecretDecryptionThrowsExceptionsAndExpectedException")
    void testGetSecret_validNamespaceAndEntityTypeExists_secretRetrievalEmpty_decryptionFailSecretValueNotJWEString_anomalyException(
            Class<? extends Exception> decryptionException, Class<? extends Exception> facadeException) {

        var namespaceModel = EntityTypeInNamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .entityHashSize(1)
                .previousEntityHashSize(1)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .entityType(EntityTypeUdt.builder().name(TEST_ENTITY_TYPE).build())
                .build();

        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE));

        var secretCreationTime = Instant.ofEpochMilli(Uuids.unixTimestamp(TEST_SECRET_VERSION));

        var secretVersionModel = SecretVersionModel.builder()
                .namespace(TEST_NAMESPACE)
                .entity(TEST_ENTITY)
                .secretPath(TEST_SECRET_PATH)
                .value(TEST_SECRET_DATA_CIPHERTEXT)
                .currentVersion(TEST_SECRET_VERSION)
                .version(TEST_SECRET_VERSION)
                // TODO: Change these to `timeuuid`.
                .createdAt(secretCreationTime)
                .encryptedAt(secretCreationTime)
                .encryptedByKid(TEST_NEK_ID)
                .build();

        doReturn(Mono.just(secretVersionModel))
                .when(secretVersionService)
                .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH),
                        eq(NotFoundException.class));

        var typeRef = new TypeReference<HashMap<String,Object>>() {};

        doReturn(Mono.error(() -> createException(decryptionException, "decryption failed")))
                .when(cryptoService)
                .asyncDecrypt(eq(TEST_NAMESPACE), eq(TEST_SECRET_DATA_CIPHERTEXT), argThat(TypeRefMatcher.of(typeRef)));


        StepVerifier.create(secretFacade.getSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, null))
                .expectError(facadeException)
                .verify();

        verify(namespaceService, times(1)).getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE));

        verify(secretVersionService, times(1))
                .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH),
                        eq(NotFoundException.class));

        verify(cryptoService).asyncDecrypt(eq(TEST_NAMESPACE), eq(TEST_SECRET_DATA_CIPHERTEXT), argThat(TypeRefMatcher.of(typeRef)));

        verifyNoInteractions(telemetryComponents);
    }

    // eq(typeRef) and any(typeRef.getClass()) doesn't work to match `TypeReference` objects.
    private static class TypeRefMatcher<T> implements ArgumentMatcher<T> {

        private TypeRefMatcher(T ignored) {}

        public static <T> TypeRefMatcher<TypeReference<T>> of(TypeReference<T> ignored) {
            return new TypeRefMatcher<TypeReference<T>>(ignored);
        }

        @Override
        public boolean matches(T argument) {
            return true;
        }
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void testGetSecret_validNamespaceAndEntitTypeExists_secretRetrievalNonEmpty_success(boolean versionProvided) {

        var namespaceModel = EntityTypeInNamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .entityHashSize(1)
                .previousEntityHashSize(1)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .entityType(EntityTypeUdt.builder().name(TEST_ENTITY_TYPE).build())
                .build();

        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE));

        var secretCreationTime = Instant.ofEpochMilli(Uuids.unixTimestamp(TEST_SECRET_VERSION));

        var secretVersionModel = SecretVersionModel.builder()
                .namespace(TEST_NAMESPACE)
                .entity(TEST_ENTITY)
                .secretPath(TEST_SECRET_PATH)
                .value(TEST_SECRET_DATA_CIPHERTEXT)
                .currentVersion(TEST_SECRET_VERSION)
                .version(TEST_SECRET_VERSION)
                // TODO: Change these to `timeuuid`.
                .createdAt(secretCreationTime)
                .encryptedAt(secretCreationTime)
                .encryptedByKid(TEST_NEK_ID)
                .build();

        if (versionProvided) {
            doReturn(Mono.just(secretVersionModel))
                    .when(secretVersionService)
                    .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH),
                            eq(TEST_SECRET_VERSION), eq(NotFoundException.class));
        } else {
            doReturn(Mono.just(secretVersionModel))
                    .when(secretVersionService)
                    .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH),
                            eq(NotFoundException.class));
        }
        var typeRef = new TypeReference<HashMap<String,Object>>() {};

        doReturn(Mono.just(TEST_CREATE_SECRET_REQUEST_PAYLOAD))
                .when(cryptoService)
                .asyncDecrypt(eq(TEST_NAMESPACE), eq(TEST_SECRET_DATA_CIPHERTEXT), argThat(TypeRefMatcher.of(typeRef)));

        var expectedSecretResponse = SecretResponse.builder()
                .data(SecretInfo.builder()
                        .metadata(SecretVersionMetadata.builder()
                                .version(TEST_SECRET_VERSION)
                                .createdTime(secretCreationTime)
                                .build())
                        .data(TEST_CREATE_SECRET_REQUEST_PAYLOAD)
                        .build())
                .build();

        StepVerifier.create(secretFacade.getSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, versionProvided ? TEST_SECRET_VERSION : null))
                .expectNextMatches(actualSecretResponse -> expectedSecretResponse.equals(actualSecretResponse))
                .expectComplete()
                .verify();

        verify(namespaceService, times(1)).getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE));

        verify(secretVersionService, versionProvided ? times(1) : never())
                .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(TEST_SECRET_VERSION),
                        eq(NotFoundException.class));

        verify(secretVersionService, !versionProvided ? times(1) : never())
                .getSecretVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH),
                        eq(NotFoundException.class));

        verify(cryptoService).asyncDecrypt(eq(TEST_NAMESPACE), eq(TEST_SECRET_DATA_CIPHERTEXT), argThat(TypeRefMatcher.of(typeRef)));

        verifyNoInteractions(telemetryComponents);
    }

    private static CreateSecretRequest constructCreateSecretRequest(boolean isCasRequest) {
        return CreateSecretRequest.builder()
                .data(TEST_CREATE_SECRET_REQUEST_PAYLOAD)
                .options(isCasRequest
                        ? CreateSecretRequest.Options
                        .builder()
                        .cas(TEST_CREATE_SECRET_REQUEST_CAS_VERSION)
                        .build()
                        : null
                )
                .build();
    }

    private static Stream<Arguments> argsTestCreateSecretRequestsServiceThrowsExceptions(
            List<Class<? extends Exception>> serviceExceptions,
            boolean includeCasRequests,
            boolean includeNonCasRequests
    ) {
        return Stream.of(false, true)
                .filter(isCasRequest -> (isCasRequest && includeCasRequests) || (!isCasRequest && includeNonCasRequests))
                .flatMap(isCasRequest ->
                        serviceExceptions.stream().map(
                                exClass -> Arguments.of(constructCreateSecretRequest(isCasRequest), exClass)
                        )
                );
    }

    private static Stream<Arguments> argsTestCreateSecretRequestsServiceThrowsExceptions(
            List<Class<? extends Exception>> serviceExceptions
    ) {
        return argsTestCreateSecretRequestsServiceThrowsExceptions(serviceExceptions, true, true);
    }

    private static Stream<Arguments> argsTestCreateSecretFailedToRetrieveNSAndEntityType() {
        return argsTestCreateSecretRequestsServiceThrowsExceptions(List.of(
                RuntimeException.class,  // Underlying fetch failed due to spurious error.
                NotFoundException.class  // A non-deleted namespace or entity-type was not found.
        ));
    }

    private static Exception createException(Class<? extends Exception> exceptionType, String msg) {
        if (RetryableException.class.equals(exceptionType)) {
            return new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, msg));
        }
        try {
            var constructor = exceptionType.getConstructor(String.class);
            return assertDoesNotThrow(() -> constructor.newInstance(msg));
        } catch (NoSuchMethodException _) {
            var constructor = assertDoesNotThrow(
                    () -> exceptionType.getConstructor(String.class, Throwable.class));
            return assertDoesNotThrow(() -> constructor.newInstance(msg, null));
        }
    }

    private static Predicate<Throwable> testCreateSecretErrorUponServiceError(Class<? extends Exception> serviceExType) {
        return facadeEx -> {
            // All errors from `SecretFacade.createSecret(...)` must extend Spring's `ErrorResponseException`
            // (ess-encryption ships a shaded `BootResponseException` parallel to nv-boot's, so the common
            // root is `ErrorResponseException`).
            return facadeEx instanceof ErrorResponseException && (
                    ErrorResponseException.class.isAssignableFrom(serviceExType)
                            // If the underlying service-error is an ErrorResponseException, SecretFacade.createSecret(...)
                            // should echo it.
                            ? facadeEx.getClass().equals(serviceExType)
                            // Otherwise, SecretFacade.createSecret(...) should wrap it inside a RetryableException
                            : facadeEx instanceof RetryableException
            );
        };
    }

    @ParameterizedTest
    @MethodSource("argsTestCreateSecretFailedToRetrieveNSAndEntityType")
    void testCreateSecret_failedToRetrieveNSAndEntityType_returnNotFoundOrRetryableException(CreateSecretRequest request,
                                                                                             Class<? extends Exception> nsEntityTypeFetchFailure) {
        doReturn(Mono.error(createException(nsEntityTypeFetchFailure, "error during NS or entity-type fetch")))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));

        StepVerifier.create(secretFacade.createSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, request))
                .expectErrorMatches(testCreateSecretErrorUponServiceError(nsEntityTypeFetchFailure))
                .verify();

        verify(namespaceService, times(1))
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));

        verifyNoInteractions(entityService);
        verifyNoInteractions(cryptoService);
        verifyNoInteractions(secretPathService);
        verifyNoInteractions(secretVersionService);
        verifyNoInteractions(telemetryComponents);
    }

    private static Stream<Arguments> argsTestCreateSecretGetOrCreateEntityFailed() {
        return argsTestCreateSecretRequestsServiceThrowsExceptions(List.of(
                // Underlying entity-fetch or new-entity-creation attempt failed due to
                // spurious error.
                RuntimeException.class
        ));
    }

    private static EntityTypeInNamespaceModel nsWithValidEntityType(@Nullable Boolean requireLwtForSecretVersionWrites) {
        return EntityTypeInNamespaceModel.builder()
                .namespace(TEST_NAMESPACE)
                .entityHashSize(1)
                .previousEntityHashSize(1)
                .notaryAuthorizations(null)
                .entityType(EntityTypeUdt.builder().name(TEST_ENTITY_TYPE).build())
                .requireLWTForSecretVersionWrites(requireLwtForSecretVersionWrites)
                .createdAt(Instant.now())
                .updatedAt(Instant.now())
                .build();
    }

    private static EntityModel entityModel() {
        return EntityModel.builder()
                .namespace(TEST_NAMESPACE)
                .entityType(TEST_ENTITY_TYPE)
                .hashBucket(0)
                .entityId(TEST_ENTITY_ID)
                .createdAt(Instant.now())
                .build();
    }

    @Test
    void testCreateSecret_invalidUuidVersion_returnsConflictException() {
        UUID uuid = UUID.randomUUID();
        var request = CreateSecretRequest.builder()
                .data(TEST_CREATE_SECRET_REQUEST_PAYLOAD)
                .options(CreateSecretRequest.Options
                        .builder()
                        .cas(uuid)
                        .build()
                )
                .build();

        var namespaceModel = nsWithValidEntityType(null);
        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE, NotFoundException.class);

        StepVerifier.create(secretFacade.createSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, request))
                .expectError(ConflictException.class)
                .verify();

        verify(namespaceService)
                .getNamespaceWithValidEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE, NotFoundException.class);
        verify(telemetryComponents)
                .setSpanAttribute(any(ContextView.class), eq(SECRET_CAS_ERROR_PROVIDED_VERSION_KEY),
                        eq(uuid.toString()));
        verifyNoMoreInteractions(entityService, secretPathService, secretVersionService, telemetryComponents);
    }

    @ParameterizedTest
    @MethodSource("argsTestCreateSecretGetOrCreateEntityFailed")
    void testCreateSecret_getOrCreateEntityFailed_returnRetryableException(CreateSecretRequest request,
                                                                           Class<? extends Exception> fetchOrCreateEntityFailure) {

        var namespaceModel = nsWithValidEntityType(null);
        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));

        doReturn(Mono.error(createException(fetchOrCreateEntityFailure, "error during fetch-or-create-new entity op")))
                .when(entityService)
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));

        StepVerifier.create(secretFacade.createSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, request))
                .expectError(RetryableException.class)
                .verify();

        verify(namespaceService, times(1))
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));
        verify(entityService, times(1))
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));
        verifyNoInteractions(secretPathService);
        verifyNoInteractions(secretVersionService);
        verifyNoInteractions(telemetryComponents);
    }

    private static Stream<Arguments> argsTestCreateSecretFailedToRetrieveAndDecryptNEK() {
        return argsTestCreateSecretRequestsServiceThrowsExceptions(List.of(
                // [1] NEK fetch failed (crypto-ops are internal; this shouldn't be a NotFoundException).
                // OR: [2] MEK fetch failed (crypto-ops are internal; this shouldn't be a NotFoundException).
                // OR: [3] Data-corruption led to failure in deserializing fetched MEK.
                // OR: [4] Data-corruption led to failure in decrypting NEK using MEK.
                // OR: [5] Other errors from crypt-ops
                EncryptionException.class,
                // OR: [6] Master key missing used for crypto operations on a NEK
                MissingMasterKeyException.class
        ));
    }

    @ParameterizedTest
    @MethodSource("argsTestCreateSecretFailedToRetrieveAndDecryptNEK")
    void testCreateSecret_failedToEncrypt_returnRetryableOrAnomalyException(CreateSecretRequest request,
                                                                            Class<? extends Exception> nekError) {

        var namespaceModel = nsWithValidEntityType(null);
        var entityModel = entityModel();

        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));

        doReturn(Mono.just(entityModel))
                .when(entityService)
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));

        doReturn(Mono.error(createException(nekError, "error while fetching and decrypting NEK")))
                .when(cryptoService)
                .asyncEncryptAndGetKid(TEST_NAMESPACE, request.getData());

        StepVerifier.create(secretFacade.createSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, request))
                .expectErrorMatches(testCreateSecretErrorUponServiceError(nekError))
                .verify();

        verify(namespaceService, times(1))
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));
        verify(entityService, times(1))
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));
        verify(cryptoService).asyncEncryptAndGetKid(TEST_NAMESPACE, request.getData());
        verifyNoInteractions(secretPathService);
        verifyNoInteractions(secretVersionService);
        verifyNoInteractions(telemetryComponents);
    }

    private static Stream<Arguments> argsTestCreateSecretFailedToPrefetchCurrentSecretVersion() {

        var argList = argsTestCreateSecretRequestsServiceThrowsExceptions(List.of(
                // [1] SecretVersionService is supposed to throw RetryableException when reads / writes
                //     end in spurious errors.
                //
                // [2] If the fetched value of `current_version` (if one exists) has a timestamp
                //     more recent than the app-server's clock-time (and therefore more recent than
                //     the generated replacement-current_version, this exception is thrown in that
                //     scenario as well.
                //
                // Both of these above scenarios can occur irrespective of whether CAS is requested
                // or otherwise.
                RetryableException.class
        ))
                .collect(Collectors.toCollection(() -> new ArrayList<>()));

        argList.addAll(
                argsTestCreateSecretRequestsServiceThrowsExceptions(
                        List.of(
                                // [1] When a CAS request's CAS-version wasn't found by `SecretVersionService`,
                                //     a NotFoundException (404) is thrown.
                                NotFoundException.class,
                                // [2] When a CAS request's CAS-version was found by `SecretVersionService`,
                                //     but it isn't the most recent secret version for the given secret-path,
                                //     a ConflictException (409) is thrown.
                                ConflictException.class
                        ),
                        true,  // These service-exceptions apply only to CAS requests
                        false  // and non-CAS requests don't see them.
                )
                        .toList()
        );

        return argList.stream();
    }

    @ParameterizedTest
    @MethodSource("argsTestCreateSecretFailedToPrefetchCurrentSecretVersion")
    void testCreateSecret_failedToPrefetchCurrentSecretVersion_returnConflictOrNotFoundOrRetryableException(
            CreateSecretRequest request, Class<? extends Exception> secretVersionPrefetchFailure) {

        var namespaceModel = nsWithValidEntityType(null);
        var entityModel = entityModel();
        var casVersion = Optional.ofNullable(Objects.requireNonNullElse(request.getOptions(),
                Options.builder().build()).getCas());

        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));

        doReturn(Mono.just(entityModel))
                .when(entityService)
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));

        doReturn(Mono.zip(Mono.just(TEST_SECRET_DATA_CIPHERTEXT), Mono.just(TEST_NEK_ID)))
                .when(cryptoService)
                .asyncEncryptAndGetKid(TEST_NAMESPACE, request.getData());

        doReturn(Mono.error(createException(secretVersionPrefetchFailure, "error during secret-version prefetch")))
                .when(secretVersionService)
                .validateCurrentVersionAndGenValidNewVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(casVersion));

        StepVerifier.create(secretFacade.createSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, request))
                .expectError(secretVersionPrefetchFailure)
                .verify();

        verify(namespaceService, times(1))
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));
        verify(entityService, times(1))
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));
        verify(cryptoService).asyncEncryptAndGetKid(TEST_NAMESPACE, request.getData());

        verify(secretVersionService, times(1)).validateCurrentVersionAndGenValidNewVersion(
                eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(casVersion));
        verifyNoInteractions(secretPathService, telemetryComponents);
    }

    private static Stream<Arguments> argsTestCreateSecretWritePathsAndSecretVersionTestCases(
            List<Class<? extends Exception>> serviceExecOutcomes) {

        return Stream.of(false, true).flatMap(isCasRequest ->
                Stream.of(false, true)
                        // Return-value of secretVersionService.validateCurrentVersionAndGenValidNewVersion(...)
                        // (when it is executed successfully) is a
                        // Pair.of({value-of-current_version-in-DB}, {new-value-of-current_version-to-be-written}).
                        //
                        // {value-of-current_version-in-DB} can be `null` (i.e. the secret-versions' partition can be
                        // nonexistent in the DB) only when CAS is not requested (otherwise this step wouldn't be successful).
                        .filter(nullPrevVersion -> !nullPrevVersion || !isCasRequest)
                        .flatMap(nullPrevVersion ->
                                // The flag `nsRequiresLWTForSecretPathWrites` is a namespace-level property stored in the
                                // DB (`NamespaceModel`). If it is non-null and `true`, then `SecretVersionService` should
                                // always apply LWTs when writing a new secret-version, irrespective of whether CAS was
                                // requested or otherwise.
                                Stream.of(null, false, true).flatMap(nsRequiresLWTForSecretVersionWrites ->
                                        // Each element in `serviceExecOutcomes` is either a type of exception thrown
                                        // is a specific failing service-level operation (identify-and-write-missing-path-prefixes,
                                        // write-new-version) or `null` if the service being mock-tested executed successfully.
                                        serviceExecOutcomes
                                                .stream()
                                                .map(exClass ->
                                                        Arguments.of(
                                                                // The request going to `SecretFacade.createSecret(...)`.
                                                                constructCreateSecretRequest(isCasRequest),
                                                                // Property in `NamespaceModel`
                                                                nsRequiresLWTForSecretVersionWrites,
                                                                // Exec-outcome of service being mock-tested.
                                                                exClass,
                                                                // Pair.of(
                                                                //     {value-of-current_version-in-DB},
                                                                //     {new-value-of-current_version-to-be-written}
                                                                // )
                                                                Pair.of(
                                                                        nullPrevVersion ? null : TEST_CREATE_SECRET_REQUEST_CAS_VERSION,
                                                                        TEST_CREATE_SECRET_SUCCESS_NEW_VERSION
                                                                )
                                                        )
                                                )
                                )
                        )
        );
    }

    private static Stream<Arguments> argsTestCreateSecretFailedToWriteAllPathPrefixes() {
        return argsTestCreateSecretWritePathsAndSecretVersionTestCases(List.of(
                // When paths already exist within the entity-paths partition that conflict
                // with the secret-path or its ancestor directory-paths, a ConflictException (409)
                // is thrown by SecretPathService.
                ConflictException.class,
                // When there are spurious errors in any of the underlying DB reads / writes
                // executed to ensure that the secret=path and all its ancestor directory-paths
                // are written, or if the writes fail without an error [e.g. if an LWT-guarded write
                // fails due to a condition failure], a `RetryableException` is thrown by
                // SecretPathService.
                RetryableException.class
        ));
    }

    @ParameterizedTest
    @MethodSource("argsTestCreateSecretFailedToWriteAllPathPrefixes")
    void testCreateSecret_failedToWriteAllPathPrefixes_returnConflictOrRetryableException(CreateSecretRequest request,
                                                                                          @Nullable Boolean nsRequiresLWTForSecretVersionWrites, Class<? extends Exception> secretPathWriteFailure,
                                                                                          Pair<UUID, UUID> prevAndNewSecretVersions) {

        var namespaceModel = nsWithValidEntityType(nsRequiresLWTForSecretVersionWrites);
        var entityModel = entityModel();
        var casVersion = Optional.ofNullable(Objects.requireNonNullElse(request.getOptions(),
                Options.builder().build()).getCas());

        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));

        doReturn(Mono.just(entityModel))
                .when(entityService)
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));

        doReturn(Mono.zip(Mono.just(TEST_SECRET_DATA_CIPHERTEXT), Mono.just(TEST_NEK_ID)))
                .when(cryptoService)
                .asyncEncryptAndGetKid(TEST_NAMESPACE, request.getData());

        doReturn(Mono.just(prevAndNewSecretVersions))
                .when(secretVersionService)
                .validateCurrentVersionAndGenValidNewVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(casVersion));

        doReturn(Mono.error(createException(secretPathWriteFailure, "failure in determine-and-write-missing-paths op")))
                .when(secretPathService)
                .writeAllPathsForSecret(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH));

        StepVerifier.create(secretFacade.createSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                        TEST_SECRET_PATH, request))
                .expectError(secretPathWriteFailure)
                .verify();

        verify(namespaceService, times(1))
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));
        verify(entityService, times(1))
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));
        verify(cryptoService).asyncEncryptAndGetKid(TEST_NAMESPACE, request.getData());
        verify(secretVersionService, times(1)).validateCurrentVersionAndGenValidNewVersion(
                eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(casVersion));
        verify(secretPathService, times(1)).writeAllPathsForSecret(eq(TEST_NAMESPACE), eq(TEST_ENTITY),
                eq(TEST_SECRET_PATH));
        verify(secretVersionService, never()).createSecretVersion(any(), anyBoolean(), any(), anyBoolean());
        verifyNoInteractions(telemetryComponents);
    }

    private static Stream<Arguments> argsTestCreateSecretWriteSecretVersionOutcomes() {
        return argsTestCreateSecretWritePathsAndSecretVersionTestCases(
                // Use `Stream.of(...).collect(...)` instead of `List.of()` in order to
                // support `null` elements.
                Stream.of(
                                // Success.
                                null,
                                // Thrown when there's an LWT condition-failure during a secret-version write servicing
                                // a CAS-request. This is not a retryable error (it requires the caller to re-query the
                                // secret's most recent version and try again with a new CAS-request with that version).
                                ConflictException.class,
                                // Thrown if there's a spurious error during the write. This error can
                                // be followed by an outer-loop-retry (if there is an outer-loop for retries
                                // with retries left).
                                RetryableException.class
                        )
                        .collect(Collectors.toCollection(() -> new ArrayList<>()))
        );
    }

    @ParameterizedTest
    @MethodSource("argsTestCreateSecretWriteSecretVersionOutcomes")
    void testCreateSecret_writeSecretVersion_allOutcomes(CreateSecretRequest request,
                                                         @Nullable Boolean nsRequiresLWTForSecretVersionWrites,
                                                         @Nullable Class<? extends Exception> secretVersionWriteOutcome,
                                                         Pair<UUID, UUID> prevAndNewSecretVersions) {

        var namespaceModel = nsWithValidEntityType(nsRequiresLWTForSecretVersionWrites);
        var entityModel = entityModel();
        var casVersion = Optional.ofNullable(Objects.requireNonNullElse(request.getOptions(),
                Options.builder().build()).getCas());

        doReturn(Mono.just(namespaceModel))
                .when(namespaceService)
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));

        doReturn(Mono.just(entityModel))
                .when(entityService)
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));

        doReturn(Mono.zip(Mono.just(TEST_SECRET_DATA_CIPHERTEXT), Mono.just(TEST_NEK_ID)))
                .when(cryptoService)
                .asyncEncryptAndGetKid(TEST_NAMESPACE, request.getData());

        doReturn(Mono.just(prevAndNewSecretVersions))
                .when(secretVersionService)
                .validateCurrentVersionAndGenValidNewVersion(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(casVersion));

        var secretPathInsertionArgs = SecretPathWriteArgs.builder()
                .prevEntityVersionForCAS(Uuids.timeBased())
                .newEntityVersion(Uuids.timeBased())
                .pathsToWrite(List.of())
                .build();

        doReturn(Mono.just(secretPathInsertionArgs))
                .when(secretPathService)
                .writeAllPathsForSecret(eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH));

        var secretVersionModel = SecretVersionModel.builder()
                .namespace(TEST_NAMESPACE)
                .entity(TEST_ENTITY)
                .secretPath(TEST_SECRET_PATH)
                .value(TEST_SECRET_DATA_CIPHERTEXT)
                .currentVersion(prevAndNewSecretVersions.getRight())
                .version(prevAndNewSecretVersions.getRight())
                .createdAt(Instant.ofEpochMilli(Uuids.unixTimestamp(prevAndNewSecretVersions.getRight())))
                .encryptedAt(Instant.ofEpochMilli(Uuids.unixTimestamp(prevAndNewSecretVersions.getRight())))
                .encryptedByKid(TEST_NEK_ID)
                .build();

        doReturn(
                Objects.isNull(secretVersionWriteOutcome)
                        ? Mono.just(true)
                        : Mono.error(createException(secretVersionWriteOutcome, "error in new-secret-version write"))
        )
                .when(secretVersionService)
                .createSecretVersion(
                        secretVersionModel,
                        casVersion.isPresent() || Boolean.TRUE.equals(nsRequiresLWTForSecretVersionWrites),
                        prevAndNewSecretVersions.getLeft(),
                        casVersion.isPresent()
                );

        var verifier = StepVerifier.create(secretFacade.createSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID,
                TEST_SECRET_PATH, request));

        if (Objects.isNull(secretVersionWriteOutcome)) {
            verifier.expectNextCount(1).expectComplete().verify();
        } else {
            verifier.expectError(secretVersionWriteOutcome).verify();
        }

        verify(namespaceService, times(1))
                .getNamespaceWithValidEntityType(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(NotFoundException.class));
        verify(entityService, times(1))
                .createEntityIfNotExists(eq(TEST_NAMESPACE), eq(TEST_ENTITY_TYPE), eq(TEST_ENTITY_ID), eq(namespaceModel));
        verify(cryptoService).asyncEncryptAndGetKid(TEST_NAMESPACE, request.getData());
        verify(secretVersionService, times(1)).validateCurrentVersionAndGenValidNewVersion(
                eq(TEST_NAMESPACE), eq(TEST_ENTITY), eq(TEST_SECRET_PATH), eq(casVersion));
        verify(secretPathService, times(1)).writeAllPathsForSecret(eq(TEST_NAMESPACE), eq(TEST_ENTITY),
                eq(TEST_SECRET_PATH));
        verify(secretVersionService, times(1)).createSecretVersion(
                secretVersionModel,
                casVersion.isPresent() || Boolean.TRUE.equals(nsRequiresLWTForSecretVersionWrites),
                prevAndNewSecretVersions.getLeft(),
                casVersion.isPresent()
        );
        verifyNoInteractions(telemetryComponents);
    }
}

