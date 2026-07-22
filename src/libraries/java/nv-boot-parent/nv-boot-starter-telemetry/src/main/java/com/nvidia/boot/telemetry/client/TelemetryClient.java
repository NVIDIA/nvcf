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

package com.nvidia.boot.telemetry.client;

import static io.cloudevents.jackson.JsonFormat.CONTENT_TYPE;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import io.cloudevents.CloudEvent;
import io.cloudevents.core.format.EventFormat;
import io.cloudevents.core.provider.EventFormatProvider;
import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.http.MediaType;
import org.springframework.util.CollectionUtils;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientResponseException;

/**
 * Sends CloudEvents to a Telemetry server. Uses WebClient with OAuth2 bearer token
 * from ExchangeFilterFunction (configured via TelemetryProperties).
 */
@Slf4j
@RequiredArgsConstructor
public class TelemetryClient {

    private static final String ERR_MISSING_CLOUD_EVENTS = "List of CloudEvents is empty";
    private static final String ERR_MISSING_RESOURCE_NAME = "No Resource Name found";
    private static final String BATCH_CONTENT_TYPE =
            "application/cloudevents-batch+json; charset=UTF-8";

    private final TelemetryProperties properties;
    private final JsonMapper jsonMapper;
    private final WebClient webClient;

    private final EventFormat eventFormat =
            EventFormatProvider.getInstance().resolveFormat(CONTENT_TYPE);

    /**
     * Send CloudEvents synchronously to the Telemetry server.
     * Returns a generic response with status code and body (Map for JSON, null for empty).
     */
    public TelemetryResponse<Map<String, Object>> send(
            String resourceName,
            List<CloudEvent> cloudEvents) throws IOException {
        validateParameters(resourceName, cloudEvents);
        var eventList = serializeCloudEvents(cloudEvents);
        var pathPrefix = StringUtils.stripEnd(properties.getPathPrefix(), "/");
        var path = (pathPrefix.startsWith("/") ? pathPrefix : "/" + pathPrefix)
                + "/" + resourceName;

        try {
            var response = webClient.post()
                    .uri(path)
                    .contentType(MediaType.parseMediaType(BATCH_CONTENT_TYPE))
                    .bodyValue(eventList)
                    .retrieve()
                    .toEntity(new ParameterizedTypeReference<Map<String, Object>>() {})
                    .block();

            return TelemetryResponse.<Map<String, Object>>builder()
                    .statusCode(response.getStatusCode().value())
                    .body(response.getBody())
                    .build();
        } catch (WebClientResponseException e) {
            var msg = "Telemetry request failed (status: %d) - %s".formatted(
                    e.getStatusCode().value(),
                    e.getResponseBodyAsString());
            throw new IllegalStateException(msg, e);
        }
    }

    /**
     * Send CloudEvents asynchronously to the Telemetry server.
     */
    public CompletableFuture<TelemetryResponse<Map<String, Object>>> sendAsync(
            String resourceName,
            List<CloudEvent> cloudEvents) {
        return CompletableFuture.supplyAsync(() -> {
            try {
                return send(resourceName, cloudEvents);
            } catch (IOException e) {
                throw new CompletionException(e);
            }
        });
    }

    private List<JsonNode> serializeCloudEvents(List<CloudEvent> cloudEvents) throws IOException {
        var eventList = new ArrayList<JsonNode>();
        for (CloudEvent cloudEvent : cloudEvents) {
            var serialized = eventFormat.serialize(cloudEvent);
            eventList.add(jsonMapper.readTree(serialized));
        }
        return eventList;
    }

    private void validateParameters(String resourceName, List<CloudEvent> cloudEvents) {
        if (CollectionUtils.isEmpty(cloudEvents)) {
            throw new IllegalStateException(ERR_MISSING_CLOUD_EVENTS);
        }
        if (StringUtils.isBlank(resourceName)) {
            throw new IllegalStateException(ERR_MISSING_RESOURCE_NAME);
        }
    }
}
