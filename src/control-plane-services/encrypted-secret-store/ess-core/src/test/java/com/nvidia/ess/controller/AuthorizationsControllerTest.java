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
package com.nvidia.ess.controller;

import static org.mockito.AdditionalMatchers.aryEq;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.controller.request.CreateAuthorizationRequest;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import com.nvidia.ess.controller.response.ListAuthorizationsResponse;
import com.nvidia.ess.facade.AuthorizationsFacade;
import java.util.ArrayList;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
class AuthorizationsControllerTest {

    @Mock
    private AuthChecker authChecker;

    @Mock
    private AuthorizationsFacade authorizationFacade;

    @InjectMocks
    private AuthorizationsController authorizationsController;

    private final String namespace = "testNamespace";
    private final String authHeader = "Bearer token";
    private final String clientId = "testClientId";

    private CreateAuthorizationRequest createAuthorizationRequest;
    private AuthorizationInfo expectedAuthInfo;

    @BeforeEach
    public void setUp() {

        createAuthorizationRequest = new CreateAuthorizationRequest();
        createAuthorizationRequest.setSub("sub");
        createAuthorizationRequest.setName("name");
        createAuthorizationRequest.setIss("iss");

        expectedAuthInfo = AuthorizationInfo.builder()
                .id(createAuthorizationRequest.getSub())
                .name(createAuthorizationRequest.getName())
                .iss(createAuthorizationRequest.getIss())
                .jwks(createAuthorizationRequest.getIss() + Constants.JWKS_URI)
                .build();
    }

    @Test
    void addNonNotaryAuthorization__whenAuthorized_shouldReturnAuthorizationInfo() {
        // Arrange

        when(authChecker.authOperatorOrTenant(
                namespace,
                authHeader,
                new String[]{AuthScope.ESS_OPERATOR},
                new String[]{AuthScope.ESS_NAMESPACE_ADMIN}
        )).thenReturn(Mono.just(true));

        when(authorizationFacade.addAuthorization(
                namespace,
                false,
                createAuthorizationRequest
        )).thenReturn(Mono.just(expectedAuthInfo));


        // Act
        Mono<AuthorizationInfo> result = authorizationsController.addOauthAuthorization(authHeader, namespace, createAuthorizationRequest);

        // Assert
        StepVerifier.create(result)
                .expectNext(expectedAuthInfo)
                .verifyComplete();

    }

    @Test
    void addNonNotaryAuthorization__whenAuthorizationFails_shouldPropagateError() {

        // Arrange
        UnauthorizedException expectedException = new UnauthorizedException(Constants.UNAUTHORIZED);

        when(authChecker.authOperatorOrTenant(
                eq(namespace),
                eq(authHeader),
                aryEq(new String[]{AuthScope.ESS_OPERATOR}),
                aryEq(new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
        )).thenReturn(Mono.error(expectedException));

        // Act

        // Assert
        StepVerifier.create(authorizationsController.addOauthAuthorization(authHeader, namespace, createAuthorizationRequest))
                .expectErrorMatches(throwable -> throwable instanceof UnauthorizedException
                        && throwable.getMessage().contains(Constants.UNAUTHORIZED))
                .verify();
    }

    @Test
    void removeNonNotaryAuthorization__whenAuthorized_shouldReturnAuthorizationInfo() {
        // Mock the authorization response
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope), eq(clientId)))
                .thenReturn(Mono.just(true));

        // Mock the addAuthorization response
        when(authorizationFacade.removeAuthorization(namespace, false, clientId))
                .thenReturn(Mono.empty());

        // Call the method and verify the result
        Mono<Void> result = authorizationsController.removeOauthAuthorization(
                authHeader, namespace, clientId);

        StepVerifier.create(result)
                .verifyComplete();
    }

    @Test
    void removeNonNotaryAuthorization_whenAuthorizationFails_shouldReturnException() {

        // Mock the authorization response
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope), eq(clientId)))
                .thenReturn(Mono.error(new UnauthorizedException(Constants.UNAUTHORIZED)));

        StepVerifier.create(authorizationsController.removeOauthAuthorization(
                        authHeader, namespace, clientId))
                .expectErrorMatches(throwable -> throwable instanceof UnauthorizedException &&
                        throwable.getMessage().contains(Constants.UNAUTHORIZED))
                .verify();
    }

    @Test
    void removeNonNotaryAuthorizationWithSelfRemoval_whenAuthorizationFails_shouldReturnError() {

        // Mock the authorization response
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope), eq(clientId)))
                .thenReturn(Mono.error(new ForbiddenException(Constants.MSG_CAN_NOT_REMOVE_SELF)));

        StepVerifier.create(authorizationsController.removeOauthAuthorization(
                        authHeader, namespace, clientId))
                .expectErrorMatches(throwable -> throwable instanceof ForbiddenException &&
                        throwable.getMessage().contains(Constants.MSG_CAN_NOT_REMOVE_SELF))
                .verify();

    }

    @Test
    void getNonNotaryAuthorization_whenAuthorized_shouldReturnAuthorizationInfo() {
        // Arrange

        when(authChecker.authOperatorOrTenant(
                namespace,
                authHeader,
                new String[]{AuthScope.ESS_OPERATOR},
                new String[]{AuthScope.ESS_NAMESPACE_ADMIN}
        )).thenReturn(Mono.just(true));

        when(authorizationFacade.getAuthorization(
                namespace,
                false,
                clientId
        )).thenReturn(Mono.just(expectedAuthInfo));

        // Act
        Mono<AuthorizationInfo> result = authorizationsController.getOauthAuthorization(authHeader, namespace, clientId);

        // Assert
        StepVerifier.create(result)
                .expectNext(expectedAuthInfo)
                .verifyComplete();
    }

    @Test
    void getNonNotaryAuthorization_whenAuthorizationFails_shouldReturnError() {
        // Arrange
        UnauthorizedException expectedException = new UnauthorizedException(Constants.UNAUTHORIZED);

        when(authChecker.authOperatorOrTenant(
                namespace,
                authHeader,
                new String[]{AuthScope.ESS_OPERATOR},
                new String[]{AuthScope.ESS_NAMESPACE_ADMIN}
        )).thenReturn(Mono.error(expectedException));

        // Act
        Mono<AuthorizationInfo> result = authorizationsController.getOauthAuthorization(authHeader, namespace, clientId);

        // Assert
        StepVerifier.create(result)
                .expectErrorMatches(throwable -> throwable instanceof UnauthorizedException
                        && throwable.getMessage().contains(Constants.UNAUTHORIZED))
                .verify();
    }

    @Test
    void listNonNotaryAuthorizations_whenAuthorized_shouldReturnAuthorizationInfo() {

        when(authChecker.authOperatorOrTenant(
                namespace,
                authHeader,
                new String[]{AuthScope.ESS_OPERATOR},
                new String[]{AuthScope.ESS_NAMESPACE_ADMIN}
        )).thenReturn(Mono.just(true));

        List<AuthorizationInfo> listAuthInfo = new ArrayList<>();
        listAuthInfo.add(expectedAuthInfo);
        listAuthInfo.add(expectedAuthInfo);

        ListAuthorizationsResponse listAuthResponse = ListAuthorizationsResponse.builder().authorizations(
                listAuthInfo).build();


        when(authorizationFacade.listAuthorizations(
                namespace,
                false
        )).thenReturn(Mono.just(listAuthResponse));


        // Act
        Mono<ListAuthorizationsResponse> result = authorizationsController.listOauthAuthorizations(authHeader, namespace);

        // Assert
        StepVerifier.create(result)
                .expectNext(listAuthResponse)
                .verifyComplete();
    }

    @Test
    void listNonNotaryAuthorizations_whenAuthorizationFails_shouldReturnError() {
        // Arrange
        UnauthorizedException expectedException = new UnauthorizedException(Constants.UNAUTHORIZED);

        when(authChecker.authOperatorOrTenant(
                namespace,
                authHeader,
                new String[]{AuthScope.ESS_OPERATOR},
                new String[]{AuthScope.ESS_NAMESPACE_ADMIN}
        )).thenReturn(Mono.error(expectedException));

        // Act
        Mono<ListAuthorizationsResponse> result = authorizationsController.listOauthAuthorizations(authHeader, namespace);

        // Assert
        StepVerifier.create(result)
                .expectErrorMatches(throwable -> throwable instanceof UnauthorizedException
                        && throwable.getMessage().contains(Constants.UNAUTHORIZED))
                .verify();


    }

    @Test
    void addNotaryAuthorization_whenAuthorized_shouldReturnAuthorizationInfo() {

        // Arrange

        when(authChecker.authTenant(
                namespace,
                authHeader,
                new String[]{AuthScope.ESS_NAMESPACE_ADMIN}
        )).thenReturn(Mono.just(true));

        when(authorizationFacade.addAuthorization(
                namespace,
                true,
                createAuthorizationRequest
        )).thenReturn(Mono.just(expectedAuthInfo));


        // Act
        Mono<AuthorizationInfo> result = authorizationsController.addNotaryAuthorization(authHeader, namespace, createAuthorizationRequest);

        // Assert
        StepVerifier.create(result)
                .expectNext(expectedAuthInfo)
                .verifyComplete();
    }

    @Test
    void addNotaryAuthorization_shouldPropagateError_whenAuthorizationFails()  {

        // Arrange
        UnauthorizedException expectedException = new UnauthorizedException(Constants.UNAUTHORIZED);

        when(authChecker.authTenant(
                eq(namespace),
                eq(authHeader),
                aryEq(new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
        )).thenReturn(Mono.error(expectedException));

        // Act
        Mono<AuthorizationInfo> result = authorizationsController.addNotaryAuthorization(authHeader, namespace, createAuthorizationRequest);

        // Assert
        StepVerifier.create(result)
                .expectErrorMatches(throwable -> throwable instanceof UnauthorizedException
                        && throwable.getMessage().contains(Constants.UNAUTHORIZED))
                .verify();
    }

    @Test
    void removeNotaryAuthorization_shouldReturnAuthorizationInfo_whenAuthorized() {

        // Mock the authorization response
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope)))
                .thenReturn(Mono.just(true));

        // Mock the addAuthorization response
        when(authorizationFacade.removeAuthorization(namespace, true, clientId))
                .thenReturn(Mono.empty());

        // Call the method and verify the result
        Mono<Void> result = authorizationsController.removeNotaryAuthorization(
                authHeader, namespace, clientId);

        StepVerifier.create(result)
                .verifyComplete();
    }


    @Test
    void removeNotaryAuthorization_shouldPropagateError_whenAuthorizationFails() {
        // Mock the authorization response
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope)))
                .thenReturn(Mono.error(new UnauthorizedException(Constants.UNAUTHORIZED)));

        StepVerifier.create(authorizationsController.removeNotaryAuthorization(
                        authHeader, namespace, clientId))
                .expectErrorMatches(throwable -> throwable instanceof UnauthorizedException &&
                        throwable.getMessage().contains(Constants.UNAUTHORIZED))
                .verify();
    }

    @Test
    void getNotaryAuthorization_shouldReturnAuthorizationInfo_whenAuthorized() {
        // Arrange
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope)))
                .thenReturn(Mono.just(true));

        when(authorizationFacade.getAuthorization(
                namespace,
                true,
                clientId
        )).thenReturn(Mono.just(expectedAuthInfo));

        // Act
        Mono<AuthorizationInfo> result = authorizationsController.getNotaryAuthorization(authHeader, namespace, clientId);

        // Assert
        StepVerifier.create(result)
                .expectNext(expectedAuthInfo)
                .verifyComplete();
    }

    @Test
    void getNotaryAuthorization_shouldPropagateError_whenAuthorizationFails() {
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope)))
                .thenReturn(Mono.error(new UnauthorizedException(Constants.UNAUTHORIZED)));


        // Act
        Mono<AuthorizationInfo> result = authorizationsController.getNotaryAuthorization(authHeader, namespace, clientId);

        // Assert
        StepVerifier.create(result)
                .expectErrorMatches(throwable -> throwable instanceof UnauthorizedException
                        && throwable.getMessage().contains(Constants.UNAUTHORIZED))
                .verify();
    }


    @Test
    void listNotaryAuthorization_shouldReturnListAuthorizationResponse_whenAuthorized() {

        // Arrange
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope)))
                .thenReturn(Mono.just(true));

        List<AuthorizationInfo> listAuthInfo = new ArrayList<>();
        listAuthInfo.add(expectedAuthInfo);
        listAuthInfo.add(expectedAuthInfo);

        ListAuthorizationsResponse listAuthResponse = ListAuthorizationsResponse.builder().authorizations(
                listAuthInfo).build();

        when(authorizationFacade.listAuthorizations(
                namespace,
                true
        )).thenReturn(Mono.just(listAuthResponse));


        // Act
        Mono<ListAuthorizationsResponse> result = authorizationsController.listNotaryAuthorizations(authHeader, namespace);

        // Assert
        StepVerifier.create(result)
                .expectNext(listAuthResponse)
                .verifyComplete();
    }

    @Test
    void listNotaryAuthorization_shouldPropagateError_whenAuthorizationFails() {
        String[] essTenantScope = new String[]{AuthScope.ESS_NAMESPACE_ADMIN};
        when(authChecker.authTenant(eq(namespace), eq(authHeader), aryEq(essTenantScope)))
                .thenReturn(Mono.error(new UnauthorizedException(Constants.UNAUTHORIZED)));


        // Act
        Mono<ListAuthorizationsResponse> result = authorizationsController.listNotaryAuthorizations(authHeader, namespace);

        // Assert
        StepVerifier.create(result)
                .expectErrorMatches(throwable -> throwable instanceof UnauthorizedException
                        && throwable.getMessage().contains(Constants.UNAUTHORIZED))
                .verify();
    }
}