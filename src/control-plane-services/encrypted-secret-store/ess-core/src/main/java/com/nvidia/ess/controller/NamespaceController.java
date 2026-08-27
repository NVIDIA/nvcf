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


import static com.nvidia.ess.constants.OpenTelemetryAttributes.NAMESPACE_KEY;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.controller.request.CreateNamespaceRequest;
import com.nvidia.ess.controller.response.ListNamespacesResponse;
import com.nvidia.ess.controller.response.NamespaceInfo;
import com.nvidia.ess.facade.NamespaceFacade;
import com.nvidia.ess.filter.ReactiveRequestContextHolder;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import lombok.Setter;
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
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;

@Slf4j
@RestController
@RequestMapping(produces = APPLICATION_JSON_VALUE, value = BaseController.API_PATH)
@Tag(name = "Namespaces", description = "Operations on namespaces. Operator level APIs")
public class NamespaceController extends BaseController {

    @Setter(onMethod_ = {@Autowired})
    private NamespaceFacade namespaceFacade;

    @Setter(onMethod_ = {@Autowired})
    private AuthChecker authChecker;


    @Operation(summary = "Create namespace",
            description = "This endpoint creates a namespace")
    @PostMapping("/sys/namespaces")
    @ResponseStatus(HttpStatus.OK)
    public Mono<NamespaceInfo> createNamespace(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestBody @Valid CreateNamespaceRequest body) {
        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            attachNamespaceAttribute(exchange, body.getNamespace());

            return authChecker.authOperator(authHeader, new String[]{AuthScope.ESS_OPERATOR})
                    .flatMap(result -> namespaceFacade.createNamespace(body));
        });
    }


    @Operation(summary = "Get a namespace",
            description = "This endpoint fetches a namespace. Only Operator level metadata is returned")
    @GetMapping("/sys/namespaces/{namespace}")
    @ResponseStatus(HttpStatus.OK)
    public Mono<NamespaceInfo> getNamespace(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @PathVariable("namespace") String namespace) {
        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            attachNamespaceAttribute(exchange, namespace);

            return authChecker.authOperator(authHeader, new String[]{AuthScope.ESS_OPERATOR})
                    .flatMap(result -> namespaceFacade.getNamespace(namespace));
        });
    }


    @Operation(summary = "List namespaces",
            description = "This endpoint fetches list of all namespace")
    @GetMapping("/sys/namespaces")
    @ResponseStatus(HttpStatus.OK)
    public Mono<ListNamespacesResponse> getNamespaces(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader) {

        return authChecker.authOperator(authHeader, new String[]{AuthScope.ESS_OPERATOR})
                .flatMap(result -> namespaceFacade.getNamespaces());
    }


    @Operation(summary = "Delete namespace",
            description = "This endpoint deletes a namespace. Will delete all of the following"
                    + " within a namespace: Entity Types, Entities, Secrets")
    @DeleteMapping("/sys/namespaces/{namespace}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public Mono<Void> deleteNamespace(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @PathVariable("namespace") String namespace) {
        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            attachNamespaceAttribute(exchange, namespace);

            return authChecker.authOperator(authHeader, new String[]{AuthScope.ESS_OPERATOR})
                    .flatMap(result -> namespaceFacade.removeNamespace(namespace));
        });
    }

    public void attachNamespaceAttribute(ServerWebExchange exchange, String namespace) {
        telemetryComponents.setSpanAttribute(exchange, NAMESPACE_KEY, namespace);
    }
}
