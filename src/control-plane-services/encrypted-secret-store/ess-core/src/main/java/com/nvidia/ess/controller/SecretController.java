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
import static com.nvidia.ess.constants.Constants.X_ESS_TOKEN_HEADER;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.ENTITY_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_QUERY_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_READ_AUTH_TYPE_KEY;
import static com.nvidia.ess.controller.request.SecretQueryType.QUERY_TYPE_PARAM;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.config.properties.SecretSizeProperties;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.constants.AuthorizationType;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.controller.request.CreateSecretRequest;
import com.nvidia.ess.controller.request.SecretQueryType;
import com.nvidia.ess.controller.response.kv2.CreateSecretResponse;
import com.nvidia.ess.controller.response.kv2.SecretResponse;
import com.nvidia.ess.controller.retries.RetryConfig;
import com.nvidia.ess.controller.retries.RetryHandler;
import com.nvidia.ess.exceptions.AnomalyException;
import com.nvidia.ess.facade.SecretFacade;
import com.nvidia.ess.filter.ReactiveRequestContextHolder;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.utils.HeaderUtils;
import com.nvidia.ess.validator.NotBlankAndUriSafe;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotBlank;
import java.net.URLDecoder;
import java.nio.charset.Charset;
import java.util.UUID;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.server.reactive.ServerHttpRequest;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;

@Slf4j
@RestController
@RequestMapping(produces = APPLICATION_JSON_VALUE, value = BaseController.API_PATH)
@Tag(name = "Secrets", description = "Operations on secrets. Secret Admin and Consumer level APIs")
public class SecretController extends BaseController {

    @Setter(onMethod_ = {@Autowired})
    private SecretFacade secretFacade;

    @Setter(onMethod_ = {@Autowired})
    private AuthChecker authChecker;

    @Setter(onMethod_ = {@Autowired})
    private ObjectMapper objectMapper;

    @Setter(onMethod_ = {@Autowired})
    private CustomMetricsRegistry customMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    private RetryHandler retryHandler;

    @Setter(onMethod_ = {@Autowired})
    private RetryConfig retryConfig;

    @Setter(onMethod_ = {@Autowired})
    private SecretSizeProperties secretSizeProperties;

    @Operation(summary = "Create secret",
            description = "This endpoint creates a secret")
    @PutMapping("/{entityType}/{entityId}/**")
    @ResponseStatus(HttpStatus.OK)
    public Mono<CreateSecretResponse> createSecret(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("entityType") String entityType,
            @PathVariable("entityId") String entityId,
            @RequestBody @Valid CreateSecretRequest body, ServerHttpRequest httpRequest) {

        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            telemetryComponents.setSpanAttribute(exchange, ENTITY_TYPE_KEY, entityType);

            String path = getScrtPath(httpRequest.getURI().getRawPath(), entityType, entityId);
            if (StringUtils.isEmpty(path)) {
                return Mono.error(() -> new BadRequestException("secret path is empty"));
            }

            int payloadSize;
            try {
                payloadSize = objectMapper.writeValueAsBytes(body.getData()).length;
            } catch (JacksonException _) {
                return Mono.error(() -> new AnomalyException("Unable to serialize secret payload"));
            }

            long maxPayloadSize = secretSizeProperties.getMax().toBytes();
            if (payloadSize > maxPayloadSize) {
                return Mono.error(() -> new BadRequestException(String.format("Secret payload is %d bytes, it cannot exceed %d bytes", payloadSize,
                        maxPayloadSize)));
            }

            var requestId = HeaderUtils.getRequestIdOrDefault(httpRequest, "[not set]");

            return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_SECRETS_ADMIN})
                    .flatMap(result ->
                            retryHandler.handleRetries(requestId,
                                    retryConfig.getSecretEndpointsRetryConfig().getCreateSecretRetryCount(),
                                    retryConfig.getSecretEndpointsRetryConfig().getMinBackoffBetweenRetriesMillis(),
                                    retryConfig.getSecretEndpointsRetryConfig().getMaxBackoffBetweenRetriesMillis(),
                                    () -> secretFacade.createSecret(namespace, entityType, entityId, path, body)
                            )
                    )
                    .doOnSuccess(ignored -> {
                        customMetricsRegistry.recordSecretCreate(namespace, true);
                        // only on success record payload size
                        customMetricsRegistry.recordSecretPayloadSize(namespace, payloadSize);
                    })
                    .doOnError(ignored -> customMetricsRegistry.recordSecretCreate(namespace, false));
        });

    }

    @Operation(summary = "Delete secret",
            description = "This endpoint deletes a secret")
    @DeleteMapping("/{entityType}/{entityId}/**")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public Mono<Void> deleteSecret(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("entityType") String entityType,
            @PathVariable("entityId") String entityId, ServerHttpRequest httpRequest) {

        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            telemetryComponents.setSpanAttribute(exchange, ENTITY_TYPE_KEY, entityType);

            String path = getScrtPath(httpRequest.getURI().getRawPath(), entityType, entityId);
            if (StringUtils.isEmpty(path)) {
                return Mono.error(() -> new BadRequestException("secret path is empty"));
            }

            return authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_SECRETS_ADMIN})
                    .flatMap(result -> secretFacade.deleteSecret(namespace, entityType, entityId, path))
                    .doOnSuccess(ignored -> customMetricsRegistry.recordSecretDelete(namespace, true))
                    .doOnError(ignored -> customMetricsRegistry.recordSecretDelete(namespace, false));
        });

    }


    @Operation(summary = "Get secret / List secret paths / List secret versions",
            description = "This endpoint will perform one of the following depending on"
                    + " the query parameters: Get Secret / List Secret Paths / List Secret Versions")
    @GetMapping("/{entityType}/{entityId}/**")
    @ResponseStatus(HttpStatus.OK)
    public Mono<SecretResponse> getSecret(
            @RequestHeader(value = HttpHeaders.AUTHORIZATION, required = false) String authHeader,
            @RequestHeader(value = X_ESS_TOKEN_HEADER, required = false) String essToken,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("entityType") String entityType,
            @PathVariable("entityId") String entityId,
            @RequestParam(value = "version", required = false) UUID version,
            @RequestParam(value = QUERY_TYPE_PARAM, required = false, defaultValue = SecretQueryType.DEFAULT_VALUE)
                    SecretQueryType queryType, ServerHttpRequest request) {
        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            telemetryComponents.setSpanAttribute(exchange, ENTITY_TYPE_KEY, entityType);
            telemetryComponents.setSpanAttribute(exchange, SECRET_QUERY_TYPE_KEY, queryType.name());

            String path = getScrtPath(request.getURI().getRawPath(), entityType, entityId);

            if (StringUtils.isEmpty(path) && queryType != SecretQueryType.LIST_SECRETS) {
                if (queryType == SecretQueryType.FETCH_SECRET) {
                    // If query_type is FETCH_SECRET, ensure to record the auth-type in telemetry
                    // before returning a 400 response due to this malformed path-URI.
                    secretReadAuthObservability(exchange, namespace, authHeader, essToken, false);
                }
                return Mono.error(
                        () -> new BadRequestException("Empty secret path is only allowed for"
                                + " listing root-level (entity) query_type=LIST_SECRETS"));
            }
            // perform auth

            /* When API call is invoked using Notary Token then X-ESS-TOKEN will be set instead of Authorization */
            /* This is designed to be compatible with vault as it also sends X-VAULT-TOKEN when vault agent fetches the secrets */
            Mono<Boolean> authResult;
            if (StringUtils.isNotBlank(authHeader)) {
                authResult = authChecker.authTenant(namespace, authHeader, new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN});
            } else if (StringUtils.isNotBlank(essToken)) {
                // For ESS TOKEN we allow only fetch secret query type
                if (queryType.equals(SecretQueryType.FETCH_SECRET)) {
                    authResult = authChecker.authNotaryClient(namespace, essToken, entityType + "/" + entityId + "/" + path);
                } else {
                    // query_type is not FETCH_SECRET. No need to record auth-type in telemetry (only oauth auth
                    // allowed).
                    return Mono.error(() -> new ForbiddenException(Constants.MSG_ONLY_FETCH_SECRET_ALLOWED));
                }
            } else {
                // Both Authorization: and X-ESS-Token: headers are blank. Auth-type is undetermined.
                return Mono.error(() -> new ForbiddenException(Constants.MSG_NO_AUTH_HEADER));
            }

            return authResult
                    .doOnError(ignored -> {
                        if (queryType == SecretQueryType.FETCH_SECRET) {
                            // Auth failed. If query_type is FETCH_SECRET, ensure to record the auth-type in
                            // telemetry before returning a 4xx / 5xx response.
                            secretReadAuthObservability(exchange, namespace, authHeader, essToken, false);
                        }
                    })
                    .flatMap(result -> {
                        if (queryType == SecretQueryType.FETCH_SECRET) {
                            return secretFacade.getSecret(namespace, entityType, entityId, path,
                                            version)
                                    .doOnSuccess(ignored -> secretReadAuthObservability(exchange, namespace, authHeader, essToken, true))
                                    .doOnError(ignored -> secretReadAuthObservability(exchange, namespace, authHeader, essToken, false));
                        } else if (queryType == SecretQueryType.LIST_SECRETS) {
                            return secretFacade.getSecretPaths(namespace, entityType, entityId,
                                            path)
                                    .doOnSuccess(ignored -> customMetricsRegistry.recordSecretPathsList(namespace, true))
                                    .doOnError(ignored -> customMetricsRegistry.recordSecretPathsList(namespace, false));
                        } else if (queryType == SecretQueryType.LIST_VERSIONS) {
                            return secretFacade.getSecretVersions(namespace, entityType, entityId,
                                            path)
                                    .doOnSuccess(ignored -> customMetricsRegistry.recordSecretVersionsList(namespace, true))
                                    .doOnError(ignored -> customMetricsRegistry.recordSecretVersionsList(namespace, false));
                        } else {
                            return Mono.error(() -> new BadRequestException(
                                    String.format("%s query_type not supported", queryType.name())));
                        }
                    });
        });
    }

    private void secretReadAuthObservability(ServerWebExchange exchange, String namespace, String authHeader, String essToken, boolean isSuccessful) {
        if (StringUtils.isNotBlank(authHeader)) {
            telemetryComponents.setSpanAttribute(exchange, SECRET_READ_AUTH_TYPE_KEY, AuthorizationType.OAUTH.name());
            customMetricsRegistry.recordSecretRead(namespace, AuthorizationType.OAUTH, isSuccessful);
        } else if (StringUtils.isNotBlank(essToken)) {
            telemetryComponents.setSpanAttribute(exchange, SECRET_READ_AUTH_TYPE_KEY, AuthorizationType.NOTARY.name());
            customMetricsRegistry.recordSecretRead(namespace, AuthorizationType.NOTARY, isSuccessful);
        }
    }

    @Operation(summary = "List secret paths at root of entity",
            description = "This endpoint fetches list of secrets at the root of entity. "
                    + "Appropriate query parameter needs to be set to be able to List secret paths")
    // TODO root level List Secret Paths has the same URL signature as future Entity Get.
    //  If Entity Get is added, needs to be moved to Entity Controller with combined API schema
    @GetMapping("/{entityType}/{entityId}")
    @ResponseStatus(HttpStatus.OK)
    public Mono<SecretResponse> listRootSecretPaths(
            @RequestHeader(HttpHeaders.AUTHORIZATION) @NotBlank String authHeader,
            @RequestHeader(X_ESS_NAMESPACE_HEADER) @NotBlankAndUriSafe String namespace,
            @PathVariable("entityType") String entityType,
            @PathVariable("entityId") String entityId,
            // defaulting to FETCH_SECRET to keep the contract the same. In case a client has a bug
            // and ends up doing a GET on the root of entity, it should not automatically list secret paths
            @RequestParam(value = "query_type", required = false, defaultValue = SecretQueryType.DEFAULT_VALUE)
                    SecretQueryType queryType, ServerHttpRequest request) {
        return getSecret(authHeader, null, namespace, entityType, entityId, null,
                queryType, request);
    }

    // change method name for checkmarx
    private String getScrtPath(String fullUrl, String entityType, String entityId) {
        String decodedUrl = URLDecoder.decode(fullUrl, Charset.defaultCharset());
        String prefix = String.format("%s/%s/%s/", BaseController.API_PATH, entityType, entityId);
        if (prefix.length() > decodedUrl.length()) {
            // URI: "/v1/{entityType}/{entityID}"
            return "";
        }
        // `prefix` should necessarily be a prefix of `decodedUrl`.
        return StringUtils.stripEnd(decodedUrl.substring(prefix.length()), "/");
    }
}
