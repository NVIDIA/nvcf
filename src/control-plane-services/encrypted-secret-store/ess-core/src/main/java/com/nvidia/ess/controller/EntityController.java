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
import com.nvidia.ess.facade.EntityFacade;
import com.nvidia.ess.filter.ReactiveRequestContextHolder;
import com.nvidia.ess.validator.NotBlankAndUriSafe;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.constraints.NotBlank;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestMethod;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;
import reactor.core.publisher.Mono;

@Slf4j
@RestController
@RequestMapping(produces = APPLICATION_JSON_VALUE, value = BaseController.API_PATH)
@Tag(name = "Entity", description = "Operations on entities. Secret and Entity Admin level APIs")
public class EntityController extends BaseController {

    @Setter(onMethod_ = {@Autowired})
    private EntityFacade entityFacade;

    @Setter(onMethod_ = {@Autowired})
    private AuthChecker authChecker;


    @Operation(summary = "Delete entity",
            description = "This endpoint deletes an entity. Will delete all secrets within the entity")
    @DeleteMapping("/{entityType}/{entityId}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public Mono<Void> deleteEntity(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("entityType") String entityType,
            @PathVariable("entityId") String entityId) {

        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            telemetryComponents.setSpanAttribute(exchange, ENTITY_TYPE_KEY, entityType);

            return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_ENTITIES_ADMIN})
                    .flatMap(result ->
                            entityFacade.deleteEntity(namespace, entityType, entityId)
                    );
        });
    }

    @Operation(summary = "Entity exists",
            description = "This endpoint checks if an entity exists")
    @RequestMapping(value = "/{entityType}/{entityId}", method = RequestMethod.HEAD)
    @ResponseStatus(HttpStatus.OK)
    public Mono<Void> entityExists(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("entityType") String entityType,
            @PathVariable("entityId") String entityId) {

        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            telemetryComponents.setSpanAttribute(exchange, ENTITY_TYPE_KEY, entityType);

            return authChecker.authTenant(namespace, authHeader, new String[]{
                    AuthScope.ESS_SECRETS_CONSUMER,
                    AuthScope.ESS_SECRETS_ADMIN,
                    AuthScope.ESS_ENTITIES_ADMIN})
                    .flatMap(result ->
                            // add business logic
                            entityFacade.entityExists(namespace, entityType, entityId)
                    );
        });
    }
}
