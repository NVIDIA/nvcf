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
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.persistence.function.entity.FunctionKey;
import com.nvidia.nvcf.service.ess.EssStubService.SaveSecretsRequest;
import com.nvidia.nvcf.util.EssResponseTransformer.FetchSecretsResponse.FetchSecretData;
import java.net.URI;
import java.util.Collections;
import java.util.HashMap;
import java.util.Map;
import java.util.UUID;
import lombok.Builder;
import lombok.NonNull;
import lombok.SneakyThrows;
import lombok.Value;
import lombok.extern.jackson.Jacksonized;
import lombok.extern.slf4j.Slf4j;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
public class EssResponseTransformer implements ResponseTransformerV2 {
    public static final String NAME = "ess-response-transformer";

    private static final UUID WILDCARD = new UUID(0, 0);

    private final JsonMapper jsonMapper = JsonMapper.builder().build();
    private final Map<FunctionKey, Map<String, JsonNode>> secretsByFunctionKey = new HashMap<>();
    private final Map<String, Map<String, JsonNode>> secretsByTelemetryId = new HashMap<>();
    private final Map<String, Map<String, JsonNode>> secretsByRegistryPath = new HashMap<>();

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
        log.debug("Transformer '{}': Request - {} {}", this.hashCode(), request.getMethod(), url);

        return switch (request.getMethod().getName()) {
            case "PUT" -> saveOrUpdateSecrets(request, response);
            case "DELETE" -> deleteSecrets(request, response);
            case "GET" -> fetchSecrets(request, response);
            default -> throw new BadRequestException("Unexpected HTTP Method");
        };
    }

    public void clearSecrets() {
        secretsByFunctionKey.clear();
        secretsByTelemetryId.clear();

        // Do not clear registry related secrets. Their lifecycle is tied to the Wiremock server.
        // A lot of tests create accounts just once in the beginning in the @BeforeAll method.
        // However, these tests also clear the secrets(function, telemetry) in their @AfterEach
        // method. Such tests will fail if we clear registry specific secrets here.
    }

    private boolean isSidecarRegistrySecretRequest(Request request) {
        return request.getAbsoluteUrl().matches(".*/accounts/nvcf/registries/sidecar/[^/]+.*");
    }

    private boolean isRegistrySecretRequest(Request request) {
        return request.getAbsoluteUrl().matches(".*/accounts/[^/]+/registries/[^/]+.*")
                || request.getAbsoluteUrl().matches(".*/accounts/[^/]+/registry-credentials/[^/]+.*") ;
    }

    private boolean isTelemetrySecretRequest(Request request) {
        return request.getAbsoluteUrl().matches(".*/accounts/[^/]+/telemetries/[^/]+.*");
    }

    private UUID getRegistryCredentialId(Request request) {
        var url = request.getAbsoluteUrl();

        var registriesIndex = url.indexOf("/registries/");
        if (registriesIndex == -1) {
            registriesIndex = url.indexOf("/registry-credentials/");
            if (registriesIndex == -1) {
                return null;
            }
        }
        var indexOfLastSlash = url.lastIndexOf('/');
        var registryIdWithQueryParams = url.substring(indexOfLastSlash + 1);
        var str = registryIdWithQueryParams.split("[?]", 2)[0]; // Ensure query params are stripped
        return UUID.fromString(str);
    }

    private String getTelemetryId(Request request) {
        var url = request.getAbsoluteUrl();

        var telemetryIndex = url.indexOf("/telemetries/");
        if (telemetryIndex == -1) {
            return null;
        }
        var telemetryIdStart = telemetryIndex + "/telemetries/".length();
        var telemetryIdEnd = url.indexOf('/', telemetryIdStart);

        var telemetryId = (telemetryIdEnd == -1) ?
                url.substring(telemetryIdStart) :
                url.substring(telemetryIdStart, telemetryIdEnd);

        return telemetryId.split("[?]", 2)[0]; // Ensure query params are stripped
    }

    private UUID getFunctionId(Request request) {
        var url = request.getAbsoluteUrl();
        var indexOfSlashAfterFunctions = url.indexOf('/', url.indexOf("functions"));
        var rawFunctionId = !url.contains("versions") ?
                url.substring(indexOfSlashAfterFunctions + 1) :
                url.substring(indexOfSlashAfterFunctions + 1, url.indexOf("/versions"));
        return UUID.fromString(rawFunctionId);
    }

    private UUID getVersionId(Request request) {
        var url = request.getAbsoluteUrl();
        if (url.endsWith("versions") || !url.contains("versions")) {
            return WILDCARD;
        }
        var indexOfSlashAfterVersions = url.indexOf('/', url.indexOf("versions"));
        var rawVersionId = !url.contains("?") ?
                url.substring(indexOfSlashAfterVersions + 1) :
                url.substring(indexOfSlashAfterVersions + 1, url.indexOf('?'));
        return UUID.fromString(rawVersionId);
    }

    // ----------------------- Save / Update Secrets -------------------------------

    @SneakyThrows
    private Response saveOrUpdateSecrets(Request request, Response response) {
        if (isRegistrySecretRequest(request)) {
            return saveOrUpdateRegistrySecret(request, response);
        } else if (isTelemetrySecretRequest(request)) {
            return saveOrUpdateTelemetrySecret(request, response);
        }
        return saveOrUpdateFunctionSecrets(request, response);
    }

    @SneakyThrows
    private Response saveOrUpdateFunctionSecrets(Request request, Response response) {
        var functionId = getFunctionId(request);
        var versionId = getVersionId(request);
        log.debug("Function id '{}', version '{}': Saving / Updating Secrets", functionId, versionId);

        var key = new FunctionKey(functionId, versionId);
        var rawBody = request.getBodyAsString();
        var body = jsonMapper.readValue(rawBody, SaveSecretsRequest.class);
        var newSecrets = body.getData();

        // Wipes away existing secrets with new secrets.
        secretsByFunctionKey.put(key, newSecrets);
        return response;
    }

    @SneakyThrows
    private Response saveOrUpdateRegistrySecret(Request request, Response response) {
        var registryCredentialId = getRegistryCredentialId(request);
        log.debug("Registry Credential id '{}': Saving / Updating Secret", registryCredentialId);

        var rawBody = request.getBodyAsString();
        var body = jsonMapper.readValue(rawBody, SaveSecretsRequest.class);
        var newSecrets = body.getData();

        // Wipes away existing secrets with new secrets.
        var path = URI.create(request.getAbsoluteUrl()).toURL().getPath();
        secretsByRegistryPath.put(path, newSecrets);
        return response;
    }

    @SneakyThrows
    private Response saveOrUpdateTelemetrySecret(Request request, Response response) {
        var telemetryId = getTelemetryId(request);
        log.debug("Telemetry id '{}': Saving / Updating Secret", telemetryId);

        var rawBody = request.getBodyAsString();
        var body = jsonMapper.readValue(rawBody, SaveSecretsRequest.class);
        var newSecrets = body.getData();

        // Wipes away existing secrets with new secrets.
        secretsByTelemetryId.put(telemetryId, newSecrets);
        return response;
    }

    // --------------------------- Fetch Secrets ----------------------------------

    @SneakyThrows
    private Response fetchSecrets(Request request, Response response) {
        if (isRegistrySecretRequest(request)) {
            return fetchRegistryCredentialSecret(request, response);
        } else if (isTelemetrySecretRequest(request)) {
            return fetchTelemetrySecret(request, response);
        }
        return fetchFunctionSecrets(request, response);
    }

    @SneakyThrows
    private Response fetchFunctionSecrets(Request request, Response response) {
        var functionId = getFunctionId(request);
        var versionId = getVersionId(request);
        log.debug("Function id '{}', version '{}': Fetching Secrets", functionId, versionId);

        if (!request.getAbsoluteUrl().contains("query_type=fetch_secret")) {
            throw new BadRequestException("Only supports query_type=fetch_secret");
        }

        var key = new FunctionKey(functionId, versionId);
        var existingSecrets = secretsByFunctionKey.getOrDefault(key, Collections.emptyMap());
        if (!existingSecrets.isEmpty()) {
            var payload = new FetchSecretsResponse(new FetchSecretData(existingSecrets));
            var serialized = jsonMapper.writeValueAsString(payload);
            return Response.Builder.like(response).body(serialized).build();
        }
        return Response.response().status(404).build();
    }

    @SneakyThrows
    private Response fetchRegistryCredentialSecret(Request request, Response response) {
        var registryCredentialId = getRegistryCredentialId(request);
        log.debug("Registry Credential id '{}': Fetching Secret", registryCredentialId);

        if (!request.getAbsoluteUrl().contains("query_type=fetch_secret")) {
            throw new BadRequestException("Only supports query_type=fetch_secret");
        }
        var path = URI.create(request.getAbsoluteUrl()).toURL().getPath();
        var existingSecrets = secretsByRegistryPath.getOrDefault(path, Collections.emptyMap());
        if (!existingSecrets.isEmpty()) {
            var payload = new FetchSecretsResponse(new FetchSecretData(existingSecrets));
            var serialized = jsonMapper.writeValueAsString(payload);
            return Response.Builder.like(response).body(serialized).build();
        }
        return Response.response().status(404).build();
    }

    @SneakyThrows
    private Response fetchTelemetrySecret(Request request, Response response) {
        var telemetryId = getTelemetryId(request);
        log.debug("Telemetry id '{}': Fetching Secret", telemetryId);

        if (!request.getAbsoluteUrl().contains("query_type=fetch_secret")) {
            throw new BadRequestException("Only supports query_type=fetch_secret");
        }
        var existingSecrets = secretsByTelemetryId.getOrDefault(telemetryId, Collections.emptyMap());
        if (!existingSecrets.isEmpty()) {
            var payload = new FetchSecretsResponse(new FetchSecretData(existingSecrets));
            var serialized = jsonMapper.writeValueAsString(payload);
            return Response.Builder.like(response).body(serialized).build();
        }
        return Response.response().status(404).build();
    }

    // --------------------------- Delete Secrets ----------------------------------

    private Response deleteSecrets(Request request, Response response) {
        if (isRegistrySecretRequest(request)) {
            return deleteRegistryCredentialSecret(request, response);
        } else if (isTelemetrySecretRequest(request)) {
            return deleteTelemetrySecret(request, response);
        }
        return deleteFunctionSecrets(request, response);
    }

    private Response deleteFunctionSecrets(Request request, Response response) {
        var url = request.getAbsoluteUrl();
        var functionId = getFunctionId(request);
        if (!url.contains("versions")) {
            // Delete secrets for all the versions of the function.
            return deleteFunctionSecretsPath(request, response);
        }

        // Delete secrets for the version.
        var versionId = getVersionId(request);
        log.debug("Function id '{}', version '{}': Deleting Secrets", functionId, versionId);
        var key = new FunctionKey(functionId, versionId);
        secretsByFunctionKey.remove(key);

        return response;
    }

    private Response deleteFunctionSecretsPath(Request request, Response response) {
        var functionId = getFunctionId(request);
        log.debug("Function id '{}': Deleting secrets of all the versions", functionId);

        // Delete the secrets for all the versions of the function by deleting the
        // function secrets path.
        var entries = secretsByFunctionKey.entrySet().stream()
                .filter(entry -> entry.getKey().getFunctionId().equals(functionId))
                .toList();
        entries.forEach(entry -> {
            secretsByFunctionKey.remove(entry.getKey());
        });

        return response;
    }

    @SneakyThrows
    private Response deleteRegistryCredentialSecret(Request request, Response response) {
        var registryCredentialId = getRegistryCredentialId(request);

        log.debug("Registry Credential id '{}': Deleting Secret", registryCredentialId);
        var path = URI.create(request.getAbsoluteUrl()).toURL().getPath();
        secretsByRegistryPath.remove(path);
        return response;
    }

    private Response deleteTelemetrySecret(Request request, Response response) {
        var telemetryId = getTelemetryId(request);

        log.info("Telemetry id '{}': Deleting Secret", telemetryId);
        secretsByTelemetryId.remove(telemetryId);
        return response;
    }


    @Value
    @Jacksonized
    @Builder
    @VisibleForTesting
    static class FetchSecretsResponse {
        @NonNull
        FetchSecretData data;

        @Value
        @Jacksonized
        @Builder
        public static class FetchSecretData {
            @NonNull
            Map<String, JsonNode> data;  // Object will be a Map when response is deserialized.
        }
    }

}
