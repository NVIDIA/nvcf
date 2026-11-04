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

import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.persistence.models.AuthorizationUdt;
import com.nvidia.ess.persistence.repositories.AuthorizationsRepository;
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
class AuthorizationServiceTest {

    @Mock
    private AuthorizationsRepository repository;

    @InjectMocks
    private AuthorizationService authorizationService;

    @BeforeEach
    void setUp() {
        MockitoAnnotations.openMocks(this);
    }

    @Test
    void addNonNotaryAuthorization_whenNotRegisteredForNonNotaryAuth_shouldReturnSuccess() {
        // Given
        String namespace = "test-namespace";
        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setName("name");
        authUdt.setId("test-id");
        authUdt.setIssuer("iss");
        authUdt.setJwksUrl("jwks");
        when(repository.addNonNotaryAuthorization(namespace, "test-id", authUdt))
                .thenReturn(Mono.empty());

        // When & Then
        StepVerifier.create(authorizationService.addAuthorization(namespace, authUdt, false))
                .expectNext(true)
                .verifyComplete();

        verify(repository).addNonNotaryAuthorization(namespace, "test-id", authUdt);
    }

    @Test
    void addNonNotaryAuthorization_whenNotRegisteredForNotary_shouldReturnSuccess() {
        // Given
        String namespace = "test-namespace";
        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setName("name");
        authUdt.setId("test-id");
        authUdt.setIssuer("iss");
        authUdt.setJwksUrl("jwks");
        when(repository.addNotaryAuthorization(namespace, "test-id", authUdt))
                .thenReturn(Mono.empty());

        // When & Then
        StepVerifier.create(authorizationService.addAuthorization(namespace, authUdt, true))
                .expectNext(true)
                .verifyComplete();

        verify(repository).addNotaryAuthorization(namespace, "test-id", authUdt);
    }

    @Test
    void addAuthorization_whenClientIDAlreadyRegisteredForNonNotaryAuth_shouldReturnConflictException() {
        // Given
        String namespace = "test-namespace";

        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setId("test-id");
        when(repository.addNonNotaryAuthorization(namespace, "test-id", authUdt))
                .thenReturn(Mono.error(() -> new ConflictException(String.format(Constants.MSG_CLIENT_ID_ALREADY_REGISTERED, authUdt.getId()))));

        // When & Then
        StepVerifier.create(authorizationService.addAuthorization(namespace, authUdt, false))
                .expectErrorMatches(throwable ->
                        throwable instanceof ConflictException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_CLIENT_ID_ALREADY_REGISTERED, authUdt.getId()))
                )
                .verify();

        verify(repository).addNonNotaryAuthorization(namespace, "test-id", authUdt);
    }

    @Test
    void addAuthorization_whenClientIDAlreadyRegisteredForNotary_shouldReturnConflictException() {
        // Given
        String namespace = "test-namespace";
        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setId("test-id");
        when(repository.addNotaryAuthorization(namespace, "test-id", authUdt))
                .thenReturn(Mono.error(() -> new ConflictException(String.format(Constants.MSG_CLIENT_ID_ALREADY_REGISTERED, authUdt.getId()))));

        // When & Then
        StepVerifier.create(authorizationService.addAuthorization(namespace, authUdt, true))
                .expectErrorMatches(throwable ->
                        throwable instanceof ConflictException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_CLIENT_ID_ALREADY_REGISTERED, authUdt.getId()))
                )
                .verify();

        verify(repository).addNotaryAuthorization(namespace, "test-id", authUdt);
    }

    @Test
    void addAuthorization_whenDbError_shouldReturnRuntimeException() {
        // Given
        String namespace = "test-namespace";
        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setId("test-id");
        when(repository.addNonNotaryAuthorization(namespace, "test-id", authUdt))
                .thenReturn(Mono.error(new RuntimeException("Database error")));

        // When & Then
        StepVerifier.create(authorizationService.addAuthorization(namespace, authUdt, false))
                .expectErrorMatches(throwable ->
                        throwable instanceof RuntimeException &&
                                throwable.getMessage().equals("Database error")
                )
                .verify();

        verify(repository).addNonNotaryAuthorization(namespace, "test-id", authUdt);
    }

    @Test
    void removeNonNotaryAuthorization_whenClientIDRegisteredForNonNotaryAuth_shouldReturnSuccess() {
        // Given
        String namespace = "test-namespace";
        String id = "test-id";

        when(repository.removeNonNotaryAuthorization(namespace, id))
                .thenReturn(Mono.empty());

        // When & Then
        StepVerifier.create(authorizationService.removeAuthorization(namespace, id, false))
                .expectNext(true)
                .verifyComplete();

        verify(repository).removeNonNotaryAuthorization(namespace, id);
    }

    @Test
    void removeNotaryAuthorization_whenClientIDRegisteredForNotary_shouldReturnSuccess() {
        // Given
        String namespace = "test-namespace";
        String id = "test-id";

        when(repository.removeNotaryAuthorization(namespace, id))
                .thenReturn(Mono.empty());

        // When & Then
        StepVerifier.create(authorizationService.removeAuthorization(namespace, id, true))
                .expectNext(true)
                .verifyComplete();

        verify(repository).removeNotaryAuthorization(namespace, id);
    }

    @Test
    void removeNonNotaryAuthorization_whenDbErrorForNonNotaryAuth_shouldReturnRuntimeException() {
        // Given
        String namespace = "test-namespace";
        String id = "test-id";

        when(repository.removeNonNotaryAuthorization(namespace, id))
                .thenReturn(Mono.error(new RuntimeException("Database error")));

        // When & Then
        StepVerifier.create(authorizationService.removeAuthorization(namespace, id, false))
                .expectErrorMatches(throwable ->
                        throwable instanceof RuntimeException &&
                                throwable.getMessage().contains("Database error")
                )
                .verify();

        verify(repository).removeNonNotaryAuthorization(namespace, id);
    }

    @Test
    void removeAuthorization_whenDbErrorForNotary_shouldReturnRuntimeException() {
        // Given
        String namespace = "test-namespace";
        String id = "test-id";

        when(repository.removeNotaryAuthorization(namespace, id))
                .thenReturn(Mono.error(new RuntimeException("Database error")));

        // When & Then
        StepVerifier.create(authorizationService.removeAuthorization(namespace, id, true))
                .expectErrorMatches(throwable ->
                        throwable instanceof RuntimeException &&
                                throwable.getMessage().contains("Database error")
                )
                .verify();

        verify(repository).removeNotaryAuthorization(namespace, id);
    }
}
