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
package com.nvidia.nvcf.util;

import com.github.tomakehurst.wiremock.extension.ResponseTransformerV2;
import com.github.tomakehurst.wiremock.http.Request;
import com.github.tomakehurst.wiremock.http.Response;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.rest.function.management.dto.BasicFunctionDto;
import com.nvidia.nvcf.service.token.client.NotaryService.FunctionMetadataAssertion;
import com.nvidia.nvcf.service.token.client.NotaryService.InstanceCredentialsAssertions;
import com.nvidia.nvcf.service.token.client.NotaryService.InvocationAssertion;
import com.nvidia.nvcf.service.token.client.NotaryService.SecretPathsAssertion;
import com.nvidia.nvcf.service.token.client.NotaryService.SignFunctionInvocationRequest;
import com.nvidia.nvcf.service.token.client.NotaryService.SignFunctionMetadataRequest;
import com.nvidia.nvcf.service.token.client.NotaryService.SignInstanceCredentialsRequest;
import com.nvidia.nvcf.service.token.client.NotaryService.SignResponse;
import com.nvidia.nvcf.service.token.client.NotaryService.SignSecretPathsRequest;
import java.net.URI;
import java.time.Instant;
import java.util.Date;
import java.util.List;
import java.util.UUID;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
public class NotaryServiceResponseTransformer implements ResponseTransformerV2 {

    public static final String NAME = "notary-service-response-transformer";

    private final JsonMapper jsonMapper = new JsonMapper();

    private String notaryBaseUrl;
    private String notaryClientId;
    private String nvcfAudience;

    public NotaryServiceResponseTransformer(String notaryBaseUrl,
                                            String notaryClientId,
                                            String nvcfAudience) {
        this.notaryBaseUrl = notaryBaseUrl;
        this.notaryClientId = notaryClientId;
        this.nvcfAudience = nvcfAudience;
    }

    @Override
    public boolean applyGlobally() {
        return false;
    }

    @Override
    public String getName() {
        return NAME;
    }

    @SneakyThrows
    @Override
    public Response transform(Response response, ServeEvent serveEvent) {
        var request = serveEvent.getRequest();
        var url = request.getAbsoluteUrl();
        log.info("Transformer '{}': Request - {} {}", this.hashCode(), request.getMethod(), url);

        return switch (request.getMethod().getName()) {
            case "POST" -> sign(request, response);
            case "GET" -> publicKey(request, response);

            default -> throw new BadRequestException("Unexpected HTTP Method");
        };
    }

    @SneakyThrows
    private Response publicKey(Request request, Response response) {
        var jwk = NotaryTokenUtils.getJwks().toString();
        return Response.Builder.like(response).body(jwk).build();
    }

    @SneakyThrows
    private Response sign(Request request, Response response) {
        if (request.getBodyAsString().contains("secretPaths")) {
            // The request is secretPath request. Issue a secrets token
            var assertion = jsonMapper.readValue(request.getBody(), SignSecretPathsRequest.class).data();
            var namespace = assertion.namespace();
            var secretPaths = assertion.secretPaths();
            log.info("Namespace '{}'. Secret paths '{}'", namespace, secretPaths);
            var payload = new SignResponse(generateJwtForSecretsAssertion(notaryBaseUrl,
                                                                          notaryClientId,
                                                                          nvcfAudience,
                                                                          namespace,
                                                                          secretPaths,
                                                                          jsonMapper));
            var serialized = jsonMapper.writeValueAsString(payload);
            return Response.Builder.like(response).body(serialized).build();
        }

        if (request.getBodyAsString().contains("instanceId")) {
            var assertion = jsonMapper.readValue(request.getBody(), SignInstanceCredentialsRequest.class).data();
            var functionId = assertion.functionId();
            var verionId = assertion.functionVersionId();
            var instanceId = assertion.instanceId();
            var ips = assertion.instanceIps();
            log.info("Instance id '{}', Function id '{}', Version id '{}', ips '{}': assertion token",
                     instanceId, functionId, verionId, ips);
            var payload = new SignResponse(generateJwtForInstanceAssertion(notaryBaseUrl,
                                                                           notaryClientId,
                                                                           nvcfAudience,
                                                                           instanceId,
                                                                           functionId,
                                                                           verionId,
                                                                           jsonMapper));
            var serialized = jsonMapper.writeValueAsString(payload);
            return Response.Builder.like(response).body(serialized).build();
        }

        // issue an assertion token that contains basic function info to invocation use later
        if (request.getBodyAsString().contains("clientId")) {
            var assertion = jsonMapper.readValue(request.getBody(),
                                                 SignFunctionInvocationRequest.class).data();
            var functions = assertion.intendedFunctions();
            var functionId = assertion.functionId();
            var versionId = assertion.functionVersionId();
            var ncaId = assertion.ncaId();
            var clientId = assertion.clientId();
            if (functions == null || functions.isEmpty()) {
                log.info("NCA id '{}', Function id '{}', Version id '{}', Client id '{}': assertion token",
                         ncaId, functionId, versionId, clientId);
            } else {
                log.info("NCA id '{}', Functions '{}', Client id '{}': assertion token",
                         ncaId, functions, clientId);
            }
            var payload = new SignResponse(generateJwtForInvocationAssertion(notaryBaseUrl,
                                                                             notaryClientId,
                                                                             nvcfAudience,
                                                                             ncaId,
                                                                             functionId,
                                                                             versionId,
                                                                             functions,
                                                                             clientId,
                                                                             jsonMapper));
            var serialized = jsonMapper.writeValueAsString(payload);
            return Response.Builder.like(response).body(serialized).build();
        }

        // Otherwise, issue an assertion token for function metadata
        var assertion = jsonMapper.readValue(request.getBody(), SignFunctionMetadataRequest.class).data();
        var functionId = assertion.functionId();
        var versionId = assertion.functionVersionId();
        var ncaId = assertion.ncaId();
        log.info("NCA id '{}', Function id '{}', Version id '{}', Client id '{}': assertion token",
                 ncaId, functionId, versionId);
        var payload = new SignResponse(generateJwtForFunctionMetadataAssertion(notaryBaseUrl,
                                                                         notaryClientId,
                                                                         nvcfAudience,
                                                                         ncaId,
                                                                         functionId,
                                                                         versionId,
                                                                         jsonMapper));
        var serialized = jsonMapper.writeValueAsString(payload);
        return Response.Builder.like(response).body(serialized).build();
    }

    @SneakyThrows
    private static String generateJwtForInvocationAssertion(String notaryBaseUrl,
                                                            String notaryClientId,
                                                            String aud,
                                                            String ncaId,
                                                            UUID functionId,
                                                            UUID versionId,
                                                            List<BasicFunctionDto> functions,
                                                            String clientId,
                                                            JsonMapper jsonMapper) {
        // multi function token
        var assertion = new InvocationAssertion(ncaId,
                                                null,
                                                null,
                                                functions,
                                                clientId);
        // single function token
        if (functions == null || functions.isEmpty()) {
            assertion = new InvocationAssertion(ncaId,
                                                functionId,
                                                versionId,
                                                null,
                                                clientId);
        }
        return NotaryTokenUtils.getJwt(notaryClientId,
                                       jsonMapper.writeValueAsString(assertion),
                                       URI.create(notaryBaseUrl).toURL(),
                                       aud,
                                       Date.from(Instant.now()));
    }

    @SneakyThrows
    private static String generateJwtForFunctionMetadataAssertion(String notaryBaseUrl,
                                                                  String notaryClientId,
                                                                  String aud,
                                                                  String ncaId,
                                                                  UUID functionId,
                                                                  UUID functionVersionId,
                                                                  JsonMapper jsonMapper) {
        var assertion = new FunctionMetadataAssertion(ncaId, functionId, functionVersionId);
        return NotaryTokenUtils.getJwt(notaryClientId,
                                       jsonMapper.writeValueAsString(assertion),
                                       URI.create(notaryBaseUrl).toURL(),
                                       aud,
                                       Date.from(Instant.now()));
    }

    @SneakyThrows
    private static String generateJwtForSecretsAssertion(String notaryBaseUrl,
                                                         String notaryClientId,
                                                         String aud,
                                                         String namespace,
                                                         List<String> secretPaths,
                                                         JsonMapper jsonMapper) {
        var assertion = new SecretPathsAssertion(namespace, secretPaths);
        return NotaryTokenUtils.getJwt(notaryClientId,
                                       jsonMapper.writeValueAsString(assertion),
                                       URI.create(notaryBaseUrl).toURL(),
                                       aud,
                                       Date.from(Instant.now()));
    }

    @SneakyThrows
    private static String generateJwtForInstanceAssertion(String notaryBaseUrl,
                                                          String notaryClientId,
                                                          String aud,
                                                          String instanceId,
                                                          UUID functionId,
                                                          UUID versionId,
                                                          JsonMapper jsonMapper) {
        var assertion = new InstanceCredentialsAssertions(functionId, versionId, instanceId, List.of());
        return NotaryTokenUtils.getJwt(notaryClientId,
                                       jsonMapper.writeValueAsString(assertion),
                                       URI.create(notaryBaseUrl).toURL(),
                                       aud,
                                       Date.from(Instant.now()));
    }

}
