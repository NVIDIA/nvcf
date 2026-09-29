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
import static com.nvidia.ess.constants.OpenTelemetryAttributes.ENTITY_TYPE_KEY;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.controller.request.CreateEntityTypeRequest;
import com.nvidia.ess.controller.response.EntityTypeInfo;
import com.nvidia.ess.controller.response.ListEntityTypesResponse;
import com.nvidia.ess.facade.NamespaceFacade;
import com.nvidia.ess.filter.ReactiveRequestContextHolder;
import com.nvidia.ess.validator.NotBlankAndUriSafe;
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
import reactor.core.publisher.Mono;

@Slf4j
@RestController
@RequestMapping(produces = APPLICATION_JSON_VALUE, value = BaseController.API_PATH)
@Tag(name = "Entity Types", description = "Operations on entity types. Operator and Namespace Admin level APIs")
public class EntityTypeController extends BaseController {

    @Setter(onMethod_ = {@Autowired})
    private AuthChecker authChecker;

    @Setter(onMethod_ = {@Autowired})
    private NamespaceFacade namespaceFacade;


    @Operation(summary = "Create entity type",
            description = "This endpoint creates an entity type")
    @PostMapping("/sys/entity-types")
    @ResponseStatus(HttpStatus.OK)
    public Mono<EntityTypeInfo> addEntityType(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @RequestBody @Valid CreateEntityTypeRequest body) {

        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            telemetryComponents.setSpanAttribute(exchange, ENTITY_TYPE_KEY, body.getName());

            return authChecker.authOperatorOrTenant(namespace, authHeader, new String[]{AuthScope.ESS_OPERATOR}, new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                    .flatMap(result -> namespaceFacade.createEntityType(namespace, body.getName())
                    );
        });
    }


    @Operation(summary = "Delete entity type",
            description = "This endpoint deletes an entity type. Will delete all of the following:"
                    + " Entities and Secrets")
    @DeleteMapping("/sys/entity-types/{entityType}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public Mono<Void> removeEntityType(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("entityType") String entityType) {

        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            telemetryComponents.setSpanAttribute(exchange, ENTITY_TYPE_KEY, entityType);

            return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                    .flatMap(result -> namespaceFacade.removeEntityType(namespace, entityType)
                    );
        });

    }


    @Operation(summary = "Get entity type",
            description = "This endpoint fetches entity type metadata")
    @GetMapping("/sys/entity-types/{entityType}")
    @ResponseStatus(HttpStatus.OK)
    public Mono<EntityTypeInfo> getEntityType(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("entityType") String entityType) {

        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            telemetryComponents.setSpanAttribute(exchange, ENTITY_TYPE_KEY, entityType);

            return authChecker.authOperatorOrTenant(namespace, authHeader, new String[]{AuthScope.ESS_OPERATOR}, new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                    .flatMap(result -> namespaceFacade.getEntityType(namespace, entityType));
        });
    }


    @Operation(summary = "List entity types",
            description = "List all entity types in a namespace")
    @GetMapping("/sys/entity-types")
    @ResponseStatus(HttpStatus.OK)
    public Mono<ListEntityTypesResponse> listEntityTypes(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace) {

        return authChecker.authOperatorOrTenant(namespace, authHeader, new String[]{AuthScope.ESS_OPERATOR}, new String[]{AuthScope.ESS_NAMESPACE_ADMIN})
                .flatMap(result -> namespaceFacade.listEntityTypes(namespace));
    }
}
