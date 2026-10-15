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
package com.nvidia.nvct.util;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import com.github.tomakehurst.wiremock.extension.ResponseTransformerV2;
import com.github.tomakehurst.wiremock.http.Request;
import com.github.tomakehurst.wiremock.http.Response;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvct.service.ess.EssStubService.SaveSecretsRequest;
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

@Slf4j
public class EssResponseTransformer implements ResponseTransformerV2 {
    public static final String NAME = "ess-response-transformer";

    private final JsonMapper jsonMapper = JsonMapper.builder().build();
    private final Map<UUID, Map<String, JsonNode>> secretsByTaskKey = new HashMap<>();

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
        secretsByTaskKey.clear();
    }

    @SneakyThrows
    private Response saveOrUpdateSecrets(Request request, Response response) {
        var taskId = getTaskId(request);
        log.debug("Task id '{}': Saving / Updating Secrets", taskId);

        var rawBody = request.getBodyAsString();
        var body = jsonMapper.readValue(rawBody, SaveSecretsRequest.class);
        var newSecrets = body.getData();
        secretsByTaskKey.put(taskId, newSecrets);
        return response;
    }

    @SneakyThrows
    private Response fetchSecrets(Request request, Response response) {
        var taskId = getTaskId(request);
        log.debug("Task id '{}': Fetching Secrets", taskId);

        if (!request.getAbsoluteUrl().contains("query_type=fetch_secret")) {
            throw new BadRequestException("Only supports query_type=fetch_secret");
        }

        var existingSecrets = secretsByTaskKey.getOrDefault(taskId, Collections.emptyMap());
        if (!existingSecrets.isEmpty()) {
            var payload = new FetchSecretsResponse(
                    new FetchSecretsResponse.FetchSecretData(existingSecrets));
            var serialized = jsonMapper.writeValueAsString(payload);
            return Response.Builder.like(response).body(serialized).build();
        }
        return Response.response().status(404).build();
    }

    private Response deleteSecrets(Request request, Response response) {
        var taskId = getTaskId(request);

        // Delete secrets.
        log.debug("Task id '{}': Deleting Secrets", taskId);
        secretsByTaskKey.remove(taskId);

        return response;
    }

    private UUID getTaskId(Request request) {
        var url = request.getAbsoluteUrl();
        var indexOfSlashAfterTasks = url.indexOf('/', url.indexOf("tasks"));
        var rawTaskId = !url.contains("secrets") ?
                url.substring(indexOfSlashAfterTasks + 1) :
                url.substring(indexOfSlashAfterTasks + 1, url.indexOf("secrets") - 1);
        return UUID.fromString(rawTaskId);
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
            Map<String, JsonNode> data;   // Object will be a Map when response is deserialized.
        }
    }

}
