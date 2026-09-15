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
package com.nvidia.nvcf.service.eventledger;

import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import io.micrometer.context.ContextSnapshot;
import io.micrometer.context.ContextSnapshotFactory;
import jakarta.annotation.PreDestroy;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.http.MediaType;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClient;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RefreshScope
public class EventLedgerClient {

    static final String CLIENT_REGISTRATION_ID = "event-ledger";
    static final String CLOUD_EVENTS_PATH = "/v3/ledger/cloudevents";
    static final String CLOUD_EVENTS_CONTENT_TYPE = "application/cloudevents+json";

    private static final String MESG_UNKNOWN_FUNCTION_STATUS =
            "Event Ledger unknown function status: {}, skip publishing.";
    private static final String MESG_QUEUE_FULL =
            "Event Ledger queue is full: accountId={}, functionId={}, deploymentId={}, status={}";
    private static final String MESG_FAILED_TO_ENQUEUE_EVENT =
            "Failed to enqueue Event Ledger event: accountId={}, functionId={}, deploymentId={}, "
                    + "status={}";
    private static final String MESG_FAILED_TO_PUBLISH_FUNCTION_STATUS =
            "Failed to publish function status to Event Ledger: accountId={}, functionId={}, "
                    + "functionVersionId={}, deploymentId={}, status={}";
    private static final Map<FunctionStatus, String> EVENT_NAMES = Map.of(
            FunctionStatus.DEPLOYING, "Function.Deploying",
            FunctionStatus.ACTIVE, "Function.Ready",
            FunctionStatus.DEGRADING, "Function.Degrading",
            FunctionStatus.DEGRADED, "Function.Degraded",
            FunctionStatus.ERROR, "Function.Error",
            FunctionStatus.INACTIVE, "Function.Inactive");
    private static final ContextSnapshotFactory CONTEXT_SNAPSHOT_FACTORY =
            ContextSnapshotFactory.builder().build();

    private final Duration timeout;
    private final boolean enabled;
    private final WebClient webClient;
    private final JsonMapper jsonMapper;
    private final ExecutorService executor;

    private record FunctionStatusTransition(
            UUID functionId,
            UUID functionVersionId,
            UUID deploymentId,
            FunctionStatus previousStatus,
            FunctionStatus currentStatus,
            Instant persistedAt) {
    }

    @Autowired
    public EventLedgerClient(
            @Value("${nvcf.event-ledger.enabled:false}") boolean enabled,
            @Value("${nvcf.event-ledger.base-url:http://event-ledger.nvcf.svc.cluster.local:8080}")
                    String baseUrl,
            @Value("${nvcf.event-ledger.timeout:2s}") Duration timeout,
            @Value("${nvcf.event-ledger.publisher-threads:2}") int publisherThreads,
            @Value("${nvcf.event-ledger.queue-capacity:1000}") int queueCapacity,
            @Value("${spring.security.oauth2.client.registration.event-ledger.client-id:}")
                    String clientId,
            @Value("${spring.security.oauth2.client.registration.event-ledger.client-secret:}")
                    String clientSecret,
            @Value("${spring.security.oauth2.client.registration.event-ledger.scope:}") String scope,
            @Value("${spring.security.oauth2.client.provider.event-ledger.token-uri:}") String tokenUri,
            WebClient.Builder webClientBuilder,
            JsonMapper jsonMapper) {
        this(enabled, timeout, enabled
                     ? authenticatedWebClient(
                             baseUrl, clientId, clientSecret, scope, tokenUri, webClientBuilder)
                     : webClientBuilder.baseUrl(baseUrl).build(), jsonMapper,
             new ThreadPoolExecutor(publisherThreads, publisherThreads, 0L, TimeUnit.MILLISECONDS,
                                    new ArrayBlockingQueue<>(queueCapacity),
                                    Thread.ofPlatform().name("event-ledger-publisher-", 0)
                                            .factory(),
                                    new ThreadPoolExecutor.AbortPolicy()));
    }

    EventLedgerClient(
            boolean enabled,
            Duration timeout,
            WebClient webClient,
            JsonMapper jsonMapper,
            ExecutorService executor) {
        this.enabled = enabled;
        this.timeout = timeout;
        this.webClient = webClient;
        this.jsonMapper = jsonMapper;
        this.executor = executor;
    }

    public void publish(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            UUID deploymentId,
            FunctionStatus previousStatus,
            FunctionStatus currentStatus,
            Instant persistedAt) {
        if (!enabled) {
            return;
        }

        var transition = new FunctionStatusTransition(
                functionId,
                functionVersionId,
                deploymentId,
                previousStatus,
                currentStatus,
                persistedAt);
        var eventName = EVENT_NAMES.get(transition.currentStatus());
        if (eventName == null) {
            log.warn(MESG_UNKNOWN_FUNCTION_STATUS, transition.currentStatus());
            return;
        }

        try {
            // Preserve tracing and logging context when publishing on the executor thread.
            ContextSnapshot contextSnapshot = CONTEXT_SNAPSHOT_FACTORY.captureAll();
            executor.execute(contextSnapshot.wrap(() -> send(ncaId, transition, eventName)));
        } catch (RejectedExecutionException ex) {
            log.warn(MESG_QUEUE_FULL,
                     ncaId, transition.functionId(), transition.deploymentId(),
                     transition.currentStatus());
        } catch (RuntimeException ex) {
            log.warn(MESG_FAILED_TO_ENQUEUE_EVENT,
                     ncaId, transition.functionId(), transition.deploymentId(),
                     transition.currentStatus(), ex);
        }
    }

    private void send(
            String ncaId, FunctionStatusTransition transition, String eventName) {
        try {
            var payload = Map.of(
                    "specversion", "1.0",
                    "id", UUID.randomUUID().toString(),
                    "source", "cloud-functions",
                    "type", eventName,
                    "time", transition.persistedAt().toString(),
                    "namespace", ncaId,
                    "deploymentId", transition.deploymentId().toString(),
                    "data", Map.of(
                            "functionId", transition.functionId().toString(),
                            "functionVersionId", transition.functionVersionId().toString(),
                            "deploymentId", transition.deploymentId().toString(),
                            "previousStatus", transition.previousStatus().toString(),
                            "currentStatus", transition.currentStatus().toString()));

            webClient.post()
                    .uri(CLOUD_EVENTS_PATH)
                    .contentType(MediaType.parseMediaType(CLOUD_EVENTS_CONTENT_TYPE))
                    .bodyValue(jsonMapper.writeValueAsBytes(payload))
                    .retrieve()
                    .toBodilessEntity()
                    .block(timeout);
        } catch (Exception ex) {
            log.warn(MESG_FAILED_TO_PUBLISH_FUNCTION_STATUS,
                     ncaId, transition.functionId(), transition.functionVersionId(),
                     transition.deploymentId(), transition.currentStatus(), ex);
        }
    }

    private static WebClient authenticatedWebClient(
            String baseUrl,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri,
            WebClient.Builder builder) {
        return builder.baseUrl(baseUrl)
                .filter(NvcfOAuth2ClientUtils.getOAuth2ExchangeFilter(
                        builder, CLIENT_REGISTRATION_ID, tokenUri, clientId, clientSecret, scope))
                .build();
    }

    @PreDestroy
    void close() {
        executor.shutdownNow();
    }
}
