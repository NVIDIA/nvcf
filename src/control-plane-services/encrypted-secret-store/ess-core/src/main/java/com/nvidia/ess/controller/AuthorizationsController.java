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


import static com.nvidia.ess.constants.Constants.X_ESS_NAMESPACE_HEADER;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.controller.request.CreateAuthorizationRequest;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import com.nvidia.ess.controller.response.ListAuthorizationsResponse;
import com.nvidia.ess.facade.AuthorizationsFacade;
import com.nvidia.ess.validator.NotBlankAndUriSafe;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;
import reactor.core.publisher.Mono;

@Slf4j
@RestController
@RequestMapping(produces = APPLICATION_JSON_VALUE, value = BaseController.API_PATH)
@Tag(name = "Authorizations", description = "Operations on authorizations. Operator and Namespace Admin level APIs")
public class AuthorizationsController extends BaseController {

    @Autowired
    private AuthorizationsFacade authorizationFacade;

    @Autowired
    private AuthChecker authChecker;


    @Operation(summary = "Add OAuth authorization client",
            description = "This endpoint authorizes an OAuth client for a namespace")
    @PostMapping("/sys/authorizations/oauth/clients")
    @ResponseStatus(HttpStatus.OK)
    public Mono<AuthorizationInfo> addOauthAuthorization(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @RequestBody @Valid CreateAuthorizationRequest body) {

        return authChecker.authOperatorOrTenant(namespace, authHeader,
                    new String[]{AuthScope.ESS_OPERATOR},
                    new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                .flatMap(result -> authorizationFacade.addAuthorization(namespace, false, body));
    }

    @Operation(summary = "Remove OAuth authorization client",
            description = "This endpoint removes authorization of an OAuth client for a namespace")
    @DeleteMapping("/sys/authorizations/oauth/clients/{id}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public Mono<Void> removeOauthAuthorization(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("id") String clientId) {

        return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_NAMESPACE_ADMIN}, clientId)
                .flatMap(result -> authorizationFacade.removeAuthorization(namespace, false, clientId));

    }

    @Operation(summary = "Get OAuth authorization client",
            description = "This endpoint fetches OAuth authorization client metadata")
    @GetMapping("/sys/authorizations/oauth/clients/{id}")
    @ResponseStatus(HttpStatus.OK)
    public Mono<AuthorizationInfo> getOauthAuthorization(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("id") String clientId) {

        return authChecker.authOperatorOrTenant(namespace, authHeader,
                        new String[]{AuthScope.ESS_OPERATOR},
                        new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                .flatMap(result -> authorizationFacade.getAuthorization(namespace, false, clientId));
    }

    @Operation(summary = "List OAuth authorization clients",
            description = "This endpoint lists all OAuth authorization clients in the namespace")
    @GetMapping("/sys/authorizations/oauth/clients")
    @ResponseStatus(HttpStatus.OK)
    public Mono<ListAuthorizationsResponse> listOauthAuthorizations(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace) {

        return authChecker.authOperatorOrTenant(namespace, authHeader,
                        new String[]{AuthScope.ESS_OPERATOR},
                        new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                .flatMap(result -> authorizationFacade.listAuthorizations(namespace, false));

    }

    @Operation(summary = "Add Notary authorization client",
            description = "This endpoint authorizes a Notary client for Secret consumption"
                    + " in a namespace")
    @PostMapping("/sys/authorizations/notary/clients")
    @ResponseStatus(HttpStatus.OK)
    public Mono<AuthorizationInfo> addNotaryAuthorization(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @RequestBody @Valid CreateAuthorizationRequest body) {

        return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                .flatMap(result ->authorizationFacade.addAuthorization(namespace, true, body));
    }

    @Operation(summary = "Remove Notary authorization client",
            description = "This endpoint removes authorization of a Notary client")
    @DeleteMapping("/sys/authorizations/notary/clients/{id}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public Mono<Void> removeNotaryAuthorization(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("id") String clientId) {

        return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                .flatMap(result -> authorizationFacade.removeAuthorization(namespace, true, clientId));
    }

    @Operation(summary = "Get Notary authorization client",
            description = "This endpoint fetches Notary authorization client metadata")
    @GetMapping("/sys/authorizations/notary/clients/{id}")
    @ResponseStatus(HttpStatus.OK)
    public Mono<AuthorizationInfo> getNotaryAuthorization(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("id") String clientId) {

        return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                .flatMap(result -> authorizationFacade.getAuthorization(namespace, true, clientId));
    }

    @Operation(summary = "List Notary authorization clients",
            description = "This endpoint lists all Notary authorization clients in the namespace")
    @GetMapping("/sys/authorizations/notary/clients")
    @ResponseStatus(HttpStatus.OK)
    public Mono<ListAuthorizationsResponse> listNotaryAuthorizations(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace) {

        return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                .flatMap(result -> authorizationFacade.listAuthorizations(namespace, true));
    }
}