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

import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.controller.request.CreateAuthorizationRequest;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import com.nvidia.ess.controller.response.ListAuthorizationsResponse;
import com.nvidia.ess.persistence.models.AuthorizationUdt;
import com.nvidia.ess.persistence.models.NamespaceModel;
import com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel;
import com.nvidia.ess.persistence.services.AuthorizationService;
import com.nvidia.ess.persistence.services.NamespaceService;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.Map;
import java.util.stream.Collectors;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;
import org.mockito.junit.jupiter.MockitoExtension;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
class AuthorizationsFacadeTest {

    @Mock
    private NamespaceService namespaceService;

    @Mock
    private AuthorizationService authorizationService;

    @InjectMocks
    private AuthorizationsFacade authorizationsFacade;

    @BeforeEach
    void setUp() {
        MockitoAnnotations.openMocks(this);
    }

    @Test
    void addAuthorization_whenNonNotaryAuth_shouldReturnSuccess() {
        String namespace = "testNamespace";

        CreateAuthorizationRequest req = CreateAuthorizationRequest.builder()
                .sub("testId")
                .iss("testIssuer")
                .name("testName")
                .build();

        AuthorizationUdt authUDT = new AuthorizationUdt();
        authUDT.setId(req.getSub());
        authUDT.setIssuer(req.getIss());
        authUDT.setName(req.getName());
        authUDT.setJwksUrl(req.getIss() + Constants.JWKS_URI);

        AuthorizationInfo expectedAuthorizationInfo = AuthorizationInfo.builder()
                .jwks(authUDT.getJwksUrl())
                .iss(authUDT.getIssuer())
                .id(authUDT.getId())
                .name(authUDT.getName())
                .build();

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);
        namespaceModel.setOauthAuthorizations(new HashMap<>());

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));
        when(authorizationService.addAuthorization(namespace, authUDT, false)).thenReturn(Mono.just(true));

        StepVerifier.create(authorizationsFacade.addAuthorization(namespace, false, req))
                .expectNext(expectedAuthorizationInfo)
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
        verify(authorizationService).addAuthorization(namespace, authUDT, false);
    }

    @Test
    void addAuthorization_whenNotary_Success() {
        String namespace = "testNamespace";

        CreateAuthorizationRequest req = CreateAuthorizationRequest.builder()
                .sub("testId")
                .iss("testIssuer")
                .name("testName")
                .build();

        AuthorizationUdt authUDT = new AuthorizationUdt();
        authUDT.setId(req.getSub());
        authUDT.setIssuer(req.getIss());
        authUDT.setName(req.getName());
        authUDT.setJwksUrl(req.getIss() + Constants.JWKS_URI);

        AuthorizationInfo expectedAuthorizationInfo = AuthorizationInfo.builder()
                .jwks(authUDT.getJwksUrl())
                .iss(authUDT.getIssuer())
                .id(authUDT.getId())
                .name(authUDT.getName())
                .build();

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);
        namespaceModel.setNotaryAuthorizations(new HashMap<>());

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));
        when(authorizationService.addAuthorization(namespace, authUDT, true)).thenReturn(Mono.just(true));

        StepVerifier.create(authorizationsFacade.addAuthorization(namespace, true, req))
                .expectNext(expectedAuthorizationInfo)
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
        verify(authorizationService).addAuthorization(namespace, authUDT, true);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void addAuthorization_whenNamespaceNotFound_shouldReturnError(boolean isNotary) {
        String namespace = "nonExistentNamespace";
        CreateAuthorizationRequest req = CreateAuthorizationRequest.builder().build();

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.error(new NotFoundException(String.format(Constants.MSG_NAMESPACE_NOT_FOUND, namespace))));

        StepVerifier.create(authorizationsFacade.addAuthorization(namespace, isNotary, req))
                .expectErrorMatches(throwable ->
                        throwable instanceof NotFoundException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_NAMESPACE_NOT_FOUND, namespace)))
                .verify();

        verify(namespaceService).getNamespace(namespace);
        verifyNoInteractions(authorizationService);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void addAuthorization_whenClientIdAlreadyRegistered_shouldReturnError(boolean isNotary) {
        String namespace = "testNamespace";

        CreateAuthorizationRequest req = CreateAuthorizationRequest.builder()
                .sub("existingId")
                .iss("testIssuer")
                .name("testName")
                .build();

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);
        Map<String, AuthorizationUdt> existingAuths = new HashMap<>();
        existingAuths.put("existingId", new AuthorizationUdt());
        if (isNotary) {
            namespaceModel.setNotaryAuthorizations(existingAuths);
        } else {
            namespaceModel.setOauthAuthorizations(existingAuths);
        }

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));

        StepVerifier.create(authorizationsFacade.addAuthorization(namespace, isNotary, req))
                .expectErrorMatches(throwable ->
                        throwable instanceof ConflictException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_CLIENT_ID_ALREADY_REGISTERED, "existingId")))
                .verify();

        verify(namespaceService).getNamespace(namespace);
        verifyNoInteractions(authorizationService);
    }

    @Test
    void removeNonNotaryAuthorization_whenNonNotaryAuthClientIDExists_Success() {
        String namespace = "testNamespace";
        AuthorizationUdt authUDT = new AuthorizationUdt();
        authUDT.setId("testId");

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);
        Map<String, AuthorizationUdt> auths = new HashMap<>();
        auths.put("testId", authUDT);
        namespaceModel.setOauthAuthorizations(auths);

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));
        when(authorizationService.removeAuthorization(namespace, "testId", false)).thenReturn(Mono.empty());

        StepVerifier.create(authorizationsFacade.removeAuthorization(namespace, false, "testId"))
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
        verify(authorizationService).removeAuthorization(namespace, "testId", false);
    }

    @Test
    void removeNotaryAuthorization__whenNotaryClientIDExists_Success() {
        String namespace = "testNamespace";
        AuthorizationUdt authUDT = new AuthorizationUdt();
        authUDT.setId("testId");

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);
        Map<String, AuthorizationUdt> auths = new HashMap<>();
        auths.put("testId", authUDT);
        namespaceModel.setNotaryAuthorizations(auths);

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));
        when(authorizationService.removeAuthorization(namespace, "testId", true)).thenReturn(Mono.empty());

        StepVerifier.create(authorizationsFacade.removeAuthorization(namespace, true, "testId"))
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
        verify(authorizationService).removeAuthorization(namespace, "testId", true);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void removeAuthorization_whenNamespaceNotFound_shouldReturnError(boolean isNotary) {
        String namespace = "nonExistentNamespace";

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.error(new NotFoundException(String.format(Constants.MSG_NAMESPACE_NOT_FOUND, namespace))));

        StepVerifier.create(authorizationsFacade.removeAuthorization(namespace, isNotary, "xxx"))
                .expectErrorMatches(throwable ->
                        throwable instanceof NotFoundException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_NAMESPACE_NOT_FOUND, namespace)))
                .verify();

        verify(namespaceService).getNamespace(namespace);
        verifyNoInteractions(authorizationService);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void removeAuthorization_whenClientIdNotRegistered_shouldReturnNoOp(boolean isNotary) {
        String namespace = "testNamespace";

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));

        StepVerifier.create(authorizationsFacade.removeAuthorization(namespace, isNotary, "nonExistentId"))
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
        verifyNoInteractions(authorizationService);
    }

    @Test
    void getAuthorization_whenNonNotaryAuthClientIDExists_Success() {
        String namespace = "testNamespace";
        String id = "testId";

        NamespaceWithoutEntityTypesModel model = new NamespaceWithoutEntityTypesModel();
        model.setNamespace(namespace);

        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setId(id);
        authUdt.setIssuer("testIssuer");
        authUdt.setName("testName");
        authUdt.setJwksUrl("testJwksUrl");

        Map<String, AuthorizationUdt> auths = new HashMap<>();
        auths.put(id, authUdt);
        model.setOauthAuthorizations(auths);

        when(namespaceService.getNamespaceWithoutEntityTypes(namespace)).thenReturn(Mono.just(model));

        StepVerifier.create(authorizationsFacade.getAuthorization(namespace, false, id))
                .expectNext(AuthorizationInfo.builder()
                        .iss(authUdt.getIssuer())
                        .id(authUdt.getId())
                        .name(authUdt.getName())
                        .jwks(authUdt.getJwksUrl())
                        .build())
                .verifyComplete();

        verify(namespaceService).getNamespaceWithoutEntityTypes(namespace);
    }

    @Test
    void getAuthorization__whenNotaryClientIDExists_Success() {
        String namespace = "testNamespace";
        String id = "testId";

        NamespaceWithoutEntityTypesModel model = new NamespaceWithoutEntityTypesModel();
        model.setNamespace(namespace);

        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setId(id);
        authUdt.setIssuer("testIssuer");
        authUdt.setName("testName");
        authUdt.setJwksUrl("testJwksUrl");

        Map<String, AuthorizationUdt> auths = new HashMap<>();
        auths.put(id, authUdt);
        model.setNotaryAuthorizations(auths);

        when(namespaceService.getNamespaceWithoutEntityTypes(namespace)).thenReturn(Mono.just(model));

        StepVerifier.create(authorizationsFacade.getAuthorization(namespace, true, id))
                .expectNext(AuthorizationInfo.builder()
                        .iss(authUdt.getIssuer())
                        .id(authUdt.getId())
                        .name(authUdt.getName())
                        .jwks(authUdt.getJwksUrl())
                        .build())
                .verifyComplete();

        verify(namespaceService).getNamespaceWithoutEntityTypes(namespace);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void getAuthorization_whenNamespaceNotFound_shouldReturnError(boolean isNotary) {
        String namespace = "nonExistentNamespace";
        String id = "testId";

        when(namespaceService.getNamespaceWithoutEntityTypes(namespace)).thenReturn(Mono.error(new NotFoundException(String.format(Constants.MSG_NAMESPACE_NOT_FOUND, namespace))));

        StepVerifier.create(authorizationsFacade.getAuthorization(namespace, isNotary, id))
                .expectErrorMatches(throwable ->
                        throwable instanceof NotFoundException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_NAMESPACE_NOT_FOUND, namespace)))
                .verify();

        verify(namespaceService).getNamespaceWithoutEntityTypes(namespace);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void getAuthorization_whenClientIdNotRegistered_shouldReturnError(boolean isNotary) {
        String namespace = "testNamespace";
        String id = "nonExistentId";

        NamespaceWithoutEntityTypesModel model = new NamespaceWithoutEntityTypesModel();
        model.setNamespace(namespace);
        if (isNotary) {
            model.setNotaryAuthorizations(new HashMap<>());
        } else {
            model.setOauthAuthorizations(new HashMap<>());
        }

        when(namespaceService.getNamespaceWithoutEntityTypes(namespace)).thenReturn(Mono.just(model));

        StepVerifier.create(authorizationsFacade.getAuthorization(namespace, isNotary, id))
                .expectErrorMatches(throwable ->
                        throwable instanceof NotFoundException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_CLIENT_ID_NOT_REGISTERED, id)))
                .verify();

        verify(namespaceService).getNamespaceWithoutEntityTypes(namespace);
    }

    @Test
    void listAuthorizations_ForNonNotaryAuth_Success() {
        String namespace = "testNamespace";

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);

        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setId("testId");
        authUdt.setIssuer("testIssuer");
        authUdt.setName("testName");
        authUdt.setJwksUrl("testJwksUrl");

        Map<String, AuthorizationUdt> auths = new HashMap<>();
        auths.put("testId", authUdt);
        namespaceModel.setOauthAuthorizations(auths);

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));

        ListAuthorizationsResponse expectedResponse = ListAuthorizationsResponse.builder()
                .authorizations(auths.values().stream()
                        .map(value -> AuthorizationInfo.builder()
                                .name(value.getName())
                                .id(value.getId())
                                .iss(value.getIssuer())
                                .jwks(value.getJwksUrl())
                                .build())
                        .collect(Collectors.toList()))
                .build();

        StepVerifier.create(authorizationsFacade.listAuthorizations(namespace, false))
                .expectNext(expectedResponse)
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
    }

    @Test
    void listAuthorizations_ForNotary_Success() {
        String namespace = "testNamespace";

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);

        AuthorizationUdt authUdt = new AuthorizationUdt();
        authUdt.setId("testId");
        authUdt.setIssuer("testIssuer");
        authUdt.setName("testName");
        authUdt.setJwksUrl("testJwksUrl");

        Map<String, AuthorizationUdt> auths = new HashMap<>();
        auths.put("testId", authUdt);
        namespaceModel.setNotaryAuthorizations(auths);

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));

        ListAuthorizationsResponse expectedResponse = ListAuthorizationsResponse.builder()
                .authorizations(auths.values().stream()
                        .map(value -> AuthorizationInfo.builder()
                                .name(value.getName())
                                .id(value.getId())
                                .iss(value.getIssuer())
                                .jwks(value.getJwksUrl())
                                .build())
                        .collect(Collectors.toList()))
                .build();

        StepVerifier.create(authorizationsFacade.listAuthorizations(namespace, true))
                .expectNext(expectedResponse)
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void listAuthorizations_whenNamespaceNotFound_shouldReturnError(boolean isNotary) {
        String namespace = "nonExistentNamespace";

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.error(new NotFoundException(String.format(Constants.MSG_NAMESPACE_NOT_FOUND, namespace))));

        StepVerifier.create(authorizationsFacade.listAuthorizations(namespace, isNotary))
                .expectErrorMatches(throwable ->
                        throwable instanceof NotFoundException &&
                                throwable.getMessage().contains(String.format(Constants.MSG_NAMESPACE_NOT_FOUND, namespace)))
                .verify();

        verify(namespaceService).getNamespace(namespace);
    }

    @Test
    void listNonNotaryAuthorizations_whenNoRegistrationsForNonNotaryAuth_shouldReturnEmptyList() {
        String namespace = "testNamespace";

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);
        namespaceModel.setOauthAuthorizations(null);

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));

        ListAuthorizationsResponse expectedResponse = ListAuthorizationsResponse
                .builder()
                .authorizations(new ArrayList<>())
                .build();
        StepVerifier.create(authorizationsFacade.listAuthorizations(namespace, false))
                .expectNext(expectedResponse)
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
    }

    @Test
    void listNotaryAuthorizations_whenNoRegistrationsForNotary_shouldReturnEmptyList() {
        String namespace = "testNamespace";

        NamespaceModel namespaceModel = new NamespaceModel();
        namespaceModel.setNamespace(namespace);
        namespaceModel.setNotaryAuthorizations(null);

        when(namespaceService.getNamespace(namespace)).thenReturn(Mono.just(namespaceModel));

        ListAuthorizationsResponse expectedResponse = ListAuthorizationsResponse
                .builder()
                .authorizations(new ArrayList<>())
                .build();
        StepVerifier.create(authorizationsFacade.listAuthorizations(namespace, true))
                .expectNext(expectedResponse)
                .verifyComplete();

        verify(namespaceService).getNamespace(namespace);
    }

}
