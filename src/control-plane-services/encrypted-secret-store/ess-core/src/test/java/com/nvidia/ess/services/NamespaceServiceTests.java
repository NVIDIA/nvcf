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

import static com.nvidia.ess.constants.Constants.MSG_ENTITY_TYPE_NOT_FOUND;
import static com.nvidia.ess.constants.Constants.MSG_NAMESPACE_NOT_FOUND;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY_TYPE;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.persistence.models.EntityTypeInNamespaceModel;
import com.nvidia.ess.persistence.models.EntityTypeUdt;
import com.nvidia.ess.persistence.models.NamespaceModel;
import com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel;
import com.nvidia.ess.persistence.repositories.EntityTypeInNamespaceRepository;
import com.nvidia.ess.persistence.repositories.NamespaceRepository;
import com.nvidia.ess.persistence.repositories.NamespaceWithoutEntityTypesRepository;
import com.nvidia.ess.persistence.services.NamespaceService;
import java.time.Instant;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.web.ErrorResponseException;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@Slf4j
@ExtendWith(MockitoExtension.class)
class NamespaceServiceTests {

    @Mock
    private NamespaceRepository namespaceRepository;
    @Mock
    private EntityTypeInNamespaceRepository entityTypeInNamespaceRepository;
    @Mock
    private NamespaceWithoutEntityTypesRepository namespaceWithoutEntityTypesRepository;
    @InjectMocks
    private NamespaceService namespaceService;

    @Mock
    private EntityTypeUdt entityTypeUdt;
    @Mock
    private NamespaceModel namespaceModel;
    @Mock
    private EntityTypeInNamespaceModel entityTypeInNamespaceModel;
    @Mock
    private NamespaceWithoutEntityTypesModel namespaceWithoutEntityTypesModel;


    @BeforeEach
    void setup() {
        MockitoAnnotations.openMocks(this);
    }

    @Test
    void getNamespace_onExistingNamespace_success() {
        setupNamespace(false);

        StepVerifier.create(namespaceService.getNamespace(TEST_NAMESPACE))
                .assertNext(actual -> assertEquals(namespaceModel, actual))
                .expectComplete()
                .verify();
    }


    @Test
    void getNamespace_onTombstonedNamespace_throwsNotFoundException() {
        setupNamespace(true);

        StepVerifier.create(namespaceService.getNamespace(TEST_NAMESPACE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }


    @Test
    void getNamespace_onMissingNamespace_throwsNotFoundException() {
        setupNoNamespace();

        StepVerifier.create(namespaceService.getNamespace(TEST_NAMESPACE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }


    @Test
    void getNamespace_onMissingNamespaceWithCustomException_throwsCustomException() {
        setupNoNamespace();
        StepVerifier.create(namespaceService.getNamespace(TEST_NAMESPACE, CustomException.class))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(CustomException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }

    @Test
    void getNamespaceWithValidEntityType_onExistingNamespaceAndEntityType_success() {
        setupEntityTypeInNamespace(false, false);

        StepVerifier.create(
                        namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE,
                                TEST_ENTITY_TYPE))
                .assertNext(actual -> assertEquals(entityTypeInNamespaceModel, actual))
                .expectComplete()
                .verify();
    }


    @Test
    void getNamespaceWithValidEntityType_onTombstonedNamespace_throwsNotFoundException() {
        setupEntityTypeInNamespace(true, false);

        StepVerifier.create(
                        namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE,
                                TEST_ENTITY_TYPE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }

    @Test
    void getNamespaceWithValidEntityType_onMissingNamespace_throwsNotFoundException() {
        setupNoEntityTypeInNamespace();

        StepVerifier.create(
                        namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE,
                                TEST_ENTITY_TYPE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }

    @Test
    void getNamespaceWithValidEntityType_onTombstonedEntityType_throwsNotFoundException() {
        setupEntityTypeInNamespace(false, true);

        StepVerifier.create(
                        namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE,
                                TEST_ENTITY_TYPE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_ENTITY_TYPE_NOT_FOUND,
                                TEST_ENTITY_TYPE)))
                .verify();
    }




    @Test
    void getNamespaceWithValidEntityType_onNullEmptyTypes_throwsNotFoundException() {
        // Entity type not found in map (returns null for the selected map element)
        setupEntityTypeInNamespaceWithNullEntityType();

        StepVerifier.create(
                        namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE,
                                TEST_ENTITY_TYPE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_ENTITY_TYPE_NOT_FOUND,
                                TEST_ENTITY_TYPE)))
                .verify();
    }


    @Test
    void getNamespaceWithValidEntityType_onMissingEntityType_throwsNotFoundException() {
        // Entity type not found in map (returns null for the selected map element)
        setupEntityTypeInNamespaceWithNullEntityType();

        StepVerifier.create(
                        namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE,
                                TEST_ENTITY_TYPE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_ENTITY_TYPE_NOT_FOUND,
                                TEST_ENTITY_TYPE)))
                .verify();
    }

    @Test
    void getNamespaceWithValidEntityType_onTombstonedNamespaceAndCustomException_throwsCustomException() {
        setupEntityTypeInNamespace(true, false);

        StepVerifier.create(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE,
                        TEST_ENTITY_TYPE,
                        CustomException.class))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(CustomException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }

    @Test
    void getNamespaceWithValidEntityType_onTombstonedEntityTypeAndCustomException_throwsCustomException() {
        setupEntityTypeInNamespace(false, true);

        StepVerifier.create(namespaceService.getNamespaceWithValidEntityType(TEST_NAMESPACE,
                        TEST_ENTITY_TYPE,
                        CustomException.class))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(CustomException.class)
                        .hasMessageContaining(String.format(MSG_ENTITY_TYPE_NOT_FOUND,
                                TEST_ENTITY_TYPE)))
                .verify();
    }


    @Test
    void getNamespaceWithoutFilter_onExistingNamespace_success() {
        when(namespaceRepository.findByNamespace(TEST_NAMESPACE)).thenReturn(Mono.just(namespaceModel));

        StepVerifier.create(namespaceService.getNamespaceWithoutFilter(TEST_NAMESPACE))
                .assertNext(actual -> assertEquals(namespaceModel, actual))
                .expectComplete()
                .verify();
    }

    @Test
    void getNamespaceWithoutEntityTypes_onExistingNamespace_success() {
        setupNamespaceWithoutEntityTypes(false);

        StepVerifier.create(namespaceService.getNamespaceWithoutEntityTypes(TEST_NAMESPACE))
                .assertNext(actual -> assertEquals(namespaceWithoutEntityTypesModel, actual))
                .expectComplete()
                .verify();
    }

    @Test
    void getNamespaceWithoutEntityTypes_onTombstonedNamespace_throwsNotFoundException() {
        setupNamespaceWithoutEntityTypes(true);

        StepVerifier.create(namespaceService.getNamespaceWithoutEntityTypes(TEST_NAMESPACE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }

    @Test
    void getNamespaceWithoutEntityTypes_onMissingNamespace_throwsNotFoundException() {
        setupNoNamespaceWithoutEntityTypes();

        StepVerifier.create(namespaceService.getNamespaceWithoutEntityTypes(TEST_NAMESPACE))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(NotFoundException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }

    @Test
    void getNamespaceWithoutEntityTypes_onMissingNamespaceWithCustomException_throwsCustomException() {
        setupNoNamespaceWithoutEntityTypes();

        StepVerifier.create(namespaceService.getNamespaceWithoutEntityTypes(TEST_NAMESPACE, CustomException.class))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(CustomException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }

    @Test
    void getNamespaceWithoutEntityTypes_onTombstonedNamespaceWithCustomException_throwsCustomException() {
        setupNamespaceWithoutEntityTypes(true);

        StepVerifier.create(namespaceService.getNamespaceWithoutEntityTypes(TEST_NAMESPACE, CustomException.class))
                .expectErrorSatisfies(err -> assertThat(err).isInstanceOf(CustomException.class)
                        .hasMessageContaining(String.format(MSG_NAMESPACE_NOT_FOUND,
                                TEST_NAMESPACE)))
                .verify();
    }

    private void setupNamespace(boolean isTombstoned) {
        when(namespaceRepository.findByNamespace(TEST_NAMESPACE)).thenReturn(Mono.just(namespaceModel));
        when(namespaceModel.getDeletedAt()).thenReturn(isTombstoned ? Instant.now() : null);
    }

    private void setupNoNamespace() {
        when(namespaceRepository.findByNamespace(TEST_NAMESPACE)).thenReturn(Mono.empty());
    }

    // Setup methods for the new findByNamespaceWithEntityType approach
    private void setupEntityTypeInNamespace(boolean isNamespaceTombstoned, boolean isEntityTypeTombstoned) {
        when(entityTypeInNamespaceRepository.findByNamespaceWithEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE))
                .thenReturn(Mono.just(entityTypeInNamespaceModel));
        when(entityTypeInNamespaceModel.getDeletedAt())
                .thenReturn(isNamespaceTombstoned ? Instant.now() : null);
        if (!isNamespaceTombstoned) {
            when(entityTypeInNamespaceModel.getEntityType()).thenReturn(entityTypeUdt);
            when(entityTypeUdt.getDeletedAt()).thenReturn(isEntityTypeTombstoned ? Instant.now() : null);
        }
    }

    private void setupNoEntityTypeInNamespace() {
        when(entityTypeInNamespaceRepository.findByNamespaceWithEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE))
                .thenReturn(Mono.empty());
    }

    private void setupEntityTypeInNamespaceWithNullEntityType() {
        when(entityTypeInNamespaceRepository.findByNamespaceWithEntityType(TEST_NAMESPACE, TEST_ENTITY_TYPE))
                .thenReturn(Mono.just(entityTypeInNamespaceModel));
        when(entityTypeInNamespaceModel.getDeletedAt()).thenReturn(null);
        when(entityTypeInNamespaceModel.getEntityType()).thenReturn(null);
    }

    private void setupNamespaceWithoutEntityTypes(boolean isTombstoned) {
        when(namespaceWithoutEntityTypesRepository.findByNamespace(TEST_NAMESPACE))
                .thenReturn(Mono.just(namespaceWithoutEntityTypesModel));
        when(namespaceWithoutEntityTypesModel.getDeletedAt())
                .thenReturn(isTombstoned ? Instant.now() : null);
    }

    private void setupNoNamespaceWithoutEntityTypes() {
        when(namespaceWithoutEntityTypesRepository.findByNamespace(TEST_NAMESPACE))
                .thenReturn(Mono.empty());
    }


    // both have to be public to access constructor
    public static class CustomException extends ErrorResponseException {

        public CustomException(String message) {
            super(HttpStatus.INTERNAL_SERVER_ERROR,
                    ProblemDetail.forStatusAndDetail(HttpStatus.INTERNAL_SERVER_ERROR, message),
                    null);
        }
    }
}
