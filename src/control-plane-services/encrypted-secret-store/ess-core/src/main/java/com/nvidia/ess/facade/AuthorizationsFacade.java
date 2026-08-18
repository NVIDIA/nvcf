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

import static com.nvidia.ess.constants.Constants.JWKS_URI;

import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.constants.ErrorSubType;
import com.nvidia.ess.controller.request.CreateAuthorizationRequest;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import com.nvidia.ess.controller.response.ListAuthorizationsResponse;
import com.nvidia.ess.persistence.models.AuthorizationUdt;
import com.nvidia.ess.persistence.models.NamespaceModel;
import com.nvidia.ess.persistence.models.NamespaceWithoutEntityTypesModel;
import com.nvidia.ess.persistence.services.AuthorizationService;
import com.nvidia.ess.persistence.services.NamespaceService;
import com.nvidia.ess.utils.ExceptionUtils;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.stream.Collectors;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Mono;

@Service
@Slf4j
public class AuthorizationsFacade {

    @Setter(onMethod_ = {@Autowired})
    private AuthorizationService authorizationService;

    @Setter(onMethod_ = {@Autowired})
    private NamespaceService namespaceService;

    // TODO[ESSP-539]: No need to use the entire `NamespaceModel` here. Optimize this.
    private Map<String, AuthorizationUdt> getAuthorizationsFromNamespace(NamespaceModel namespaceModel, boolean isNotary) {
        return isNotary ? namespaceModel.getNotaryAuthorizations() : namespaceModel.getOauthAuthorizations();
    }

    private Map<String, AuthorizationUdt> getAuthorizationsFromNamespaceWithoutEntityTypes(
            NamespaceWithoutEntityTypesModel model, boolean isNotary) {
        return isNotary ? model.getNotaryAuthorizations() : model.getOauthAuthorizations();
    }

    public Mono<AuthorizationInfo> addAuthorization(String namespace, boolean isNotary, CreateAuthorizationRequest req) {

        // TODO[ESSP-539]: No need to pull the entire `NamespaceModel` here. Optimize this.
        return namespaceService.getNamespace(namespace)
                .flatMap(namespaceModel -> {
                    // Get auths
                    Map<String, AuthorizationUdt> auths;
                    String clientID = req.getSub();

                    try {
                        auths = getAuthorizationsFromNamespace(namespaceModel, isNotary);
                        // Check ID in the map
                        if (auths != null && auths.containsKey(clientID)) {
                            return Mono.error(() -> new ConflictException(String.format(Constants.MSG_CLIENT_ID_ALREADY_REGISTERED, clientID)));
                        }
                    } catch (Exception e) {
                        log.error("namespace = {} isNotary = {} clientID = {}",
                                namespace,
                                isNotary,
                                clientID,
                                e);
                        return Mono.error(e);
                    }
                    // Authorizations are distinguished by their column (notary vs non-notary),
                    // resolved from the isNotary flag; there is no per-record type field.
                    AuthorizationUdt udt = AuthorizationUdt.builder()
                            .name(req.getName())
                            .id(req.getSub())
                            .issuer(req.getIss())
                            .jwksUrl(req.getIss() + JWKS_URI)
                            .build();
                    // Invoke Add auth
                    return authorizationService.addAuthorization(namespaceModel.getNamespace(), udt, isNotary)
                            .thenReturn(AuthorizationInfo.builder()
                                    .name(udt.getName())
                                    .id(udt.getId())
                                    .iss(udt.getIssuer())
                                    .jwks(udt.getJwksUrl())
                                    .build());
                });
    }

    public Mono<Void> removeAuthorization(String namespace, boolean isNotary, String clientID) {

        // TODO[ESSP-539]: No need to pull the entire `NamespaceModel` here. Optimize this.
        return namespaceService.getNamespace(namespace)
                .flatMap(namespaceModel -> {
                    // Get auths
                    Map<String, AuthorizationUdt> auths;

                    try {
                        auths = getAuthorizationsFromNamespace(namespaceModel, isNotary);
                        // Check ID in the map
                        if (auths == null || !auths.containsKey(clientID)) {
                            return Mono.empty();
                        }
                    } catch (Exception e) {
                        log.error("namespace = {} isNotary = {} clientID = {}",
                                namespace,
                                isNotary,
                                clientID,
                                e);
                        return Mono.error(e);
                    }

                    return authorizationService.removeAuthorization(namespaceModel.getNamespace(),
                            clientID, isNotary).then();
                });

    }

    public Mono<AuthorizationInfo> getAuthorization(String namespace, boolean isNotary, String id) {

        // Optimized: Use getNamespaceWithoutEntityTypes() to avoid deserializing entity_types map
        return namespaceService.getNamespaceWithoutEntityTypes(namespace)
                .flatMap(model -> {
                    Map<String, AuthorizationUdt> auths;
                    try {
                        auths = getAuthorizationsFromNamespaceWithoutEntityTypes(model, isNotary);
                        // Check ID in the map
                        if (auths == null || !auths.containsKey(id)) {
                            return Mono.error(() -> ExceptionUtils.constructErrorResponseException(
                                    NotFoundException.class,
                                    String.format(Constants.MSG_CLIENT_ID_NOT_REGISTERED, id),
                                    ErrorSubType.AUTHORIZATION_NOT_FOUND));
                        }
                    } catch (Exception e) {
                        return Mono.error(e);
                    }
                    AuthorizationUdt udt = auths.get(id);
                    return Mono.just(AuthorizationInfo.builder()
                            .iss(udt.getIssuer())
                            .id(udt.getId())
                            .name(udt.getName())
                            .jwks(udt.getJwksUrl())
                            .build());
                });

    }

    public Mono<ListAuthorizationsResponse> listAuthorizations(String namespace, boolean isNotary) {

        // TODO[ESSP-539]: No need to pull the entire `NamespaceModel` here. Optimize this.
        return namespaceService.getNamespace(namespace)
                .flatMap(namespaceModel -> {
                    Map<String, AuthorizationUdt> authMaps;

                    try {
                        authMaps = getAuthorizationsFromNamespace(namespaceModel, isNotary);
                        if (authMaps == null) {
                            return Mono.just(ListAuthorizationsResponse
                                    .builder()
                                    .authorizations(new ArrayList<>())
                                    .build());
                        }
                    } catch (Exception e) {
                        return Mono.error(e);
                    }

                    List<AuthorizationInfo> listAuthInfo = authMaps.values().stream()
                            .map(value -> AuthorizationInfo.builder()
                                    .name(value.getName())
                                    .id(value.getId())
                                    .iss(value.getIssuer())
                                    .jwks(value.getJwksUrl())
                                    .build())
                            .collect(Collectors.toList());

                    return Mono.just(ListAuthorizationsResponse.builder().authorizations(
                            listAuthInfo).build());
                });
    }
}
