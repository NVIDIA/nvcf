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

import static io.cloudevents.jackson.JsonFormat.CONTENT_TYPE;

import com.nvidia.nvcf.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import io.cloudevents.CloudEvent;
import io.cloudevents.core.builder.CloudEventBuilder;
import io.cloudevents.core.format.EventFormat;
import io.cloudevents.core.provider.EventFormatProvider;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import jakarta.annotation.PreDestroy;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.http.MediaType;
import org.springframework.http.HttpStatusCode;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RefreshScope
public class EventLedgerClient {

    static final String CLIENT_REGISTRATION_ID = "event-ledger";
    static final String CLOUD_EVENTS_PATH = "/v3/ledger/cloudevents";
    static final String CLOUD_EVENTS_CONTENT_TYPE = CONTENT_TYPE;
    static final String CLOUD_EVENT_SOURCE = "nvidia-cloud-functions";

    private static final String MESG_UNKNOWN_FUNCTION_STATUS =
            "Function id '{}', version '{}', deployment '{}': Unknown status '{}', "
                    + "skipping Event Ledger publication";
    private static final String MESG_FAILED_TO_PUBLISH_FUNCTION_STATUS =
            "Function id '{}', version '{}', deployment '{}': Failed to publish status '{}' "
                    + "to Event Ledger for account '{}'";
    private static final Map<FunctionStatus, String> EVENT_NAMES = Map.of(
            FunctionStatus.DEPLOYING, "Function.Deploying",
            FunctionStatus.ACTIVE, "Function.Ready",
            FunctionStatus.DEGRADING, "Function.Degrading",
            FunctionStatus.DEGRADED, "Function.Degraded",
            FunctionStatus.ERROR, "Function.Error",
            FunctionStatus.INACTIVE, "Function.Inactive");
    private final Duration timeout;
    private final boolean enabled;
    private final WebClient webClient;
    private final JsonMapper jsonMapper;
    private final EventFormat eventFormat;
    private final ThreadPoolExecutor executor;
    private final Counter rejectedCounter;
    private final Counter publishFailureCounter;
    private final Counter clientErrorCounter;
    private final Counter serverErrorCounter;

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
            @Value("${spring.security.oauth2.client.registration.event-ledger.client-id:}")
                    String clientId,
            @Value("${spring.security.oauth2.client.registration.event-ledger.client-secret:}")
                    String clientSecret,
            @Value("${spring.security.oauth2.client.registration.event-ledger.scope:}") String scope,
            @Value("${spring.security.oauth2.client.provider.event-ledger.token-uri:}") String tokenUri,
            @Value("${NVCF_EVENT_LEDGER_STATIC_TOKEN:}") String staticToken,
            WebClient.Builder webClientBuilder,
            JsonMapper jsonMapper,
            MeterRegistry meterRegistry) {
        this(enabled, timeout, enabled
                      ? authenticatedWebClient(
                              baseUrl, clientId, clientSecret, scope, tokenUri,
                              staticToken, webClientBuilder)
                      : webClientBuilder.baseUrl(baseUrl).build(), jsonMapper, meterRegistry);
    }

    EventLedgerClient(
            boolean enabled,
            Duration timeout,
            WebClient webClient,
            JsonMapper jsonMapper,
            MeterRegistry meterRegistry) {
        this.enabled = enabled;
        this.timeout = timeout;
        this.webClient = webClient;
        this.jsonMapper = jsonMapper;
        this.eventFormat = EventFormatProvider.getInstance().resolveFormat(CONTENT_TYPE);
        this.rejectedCounter = meterRegistry.counter(
                "nvcf_cloud_functions_event_ledger_rejected_total");
        this.publishFailureCounter = meterRegistry.counter(
                "nvcf_cloud_functions_event_ledger_publish_failures_total");
        this.clientErrorCounter = meterRegistry.counter(
                "nvcf_cloud_functions_event_ledger_client_errors_total");
        this.serverErrorCounter = meterRegistry.counter(
                "nvcf_cloud_functions_event_ledger_server_errors_total");
        this.executor = new ThreadPoolExecutor(1, 1, 0, TimeUnit.MILLISECONDS,
                new ArrayBlockingQueue<>(256), task -> {
                    var thread = new Thread(task, "event-ledger-publisher");
                    thread.setDaemon(true);
                    return thread;
                }, new ThreadPoolExecutor.AbortPolicy());
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
            log.warn(MESG_UNKNOWN_FUNCTION_STATUS, transition.functionId(),
                     transition.functionVersionId(), transition.deploymentId(),
                     transition.currentStatus());
            return;
        }

        try {
            executor.execute(() -> send(ncaId, transition, eventName));
        } catch (RejectedExecutionException ex) {
            rejectedCounter.increment();
            logPublishFailure(ncaId, transition, ex);
        } catch (RuntimeException ex) {
            publishFailureCounter.increment();
            logPublishFailure(ncaId, transition, ex);
        }
    }

    private void send(
            String ncaId, FunctionStatusTransition transition, String eventName) {
        try {
            webClient.post()
                    .uri(CLOUD_EVENTS_PATH)
                    .contentType(MediaType.parseMediaType(CLOUD_EVENTS_CONTENT_TYPE))
                    .bodyValue(eventFormat.serialize(buildCloudEvent(ncaId, transition, eventName)))
                    .retrieve()
                    .onStatus(HttpStatusCode::is4xxClientError, response -> {
                        clientErrorCounter.increment();
                        return response.createException();
                    })
                    .onStatus(HttpStatusCode::is5xxServerError, response -> {
                        serverErrorCounter.increment();
                        return response.createException();
                    })
                    .toBodilessEntity()
                    .block(timeout);
        } catch (Exception ex) {
            publishFailureCounter.increment();
            logPublishFailure(ncaId, transition, ex);
        }
    }

    private void logPublishFailure(
            String ncaId, FunctionStatusTransition transition, Exception ex) {
        log.warn(MESG_FAILED_TO_PUBLISH_FUNCTION_STATUS,
                 transition.functionId(), transition.functionVersionId(),
                 transition.deploymentId(), transition.currentStatus(), ncaId, ex);
    }

    @PreDestroy
    public void close() {
        executor.shutdown();
        try {
            if (!executor.awaitTermination(5, TimeUnit.SECONDS)) {
                executor.shutdownNow();
            }
        } catch (InterruptedException ex) {
            executor.shutdownNow();
            Thread.currentThread().interrupt();
        }
    }

    private CloudEvent buildCloudEvent(
            String ncaId, FunctionStatusTransition transition, String eventName) throws Exception {
        var details = Map.of(
                "previousStatus", transition.previousStatus().toString(),
                "currentStatus", transition.currentStatus().toString());
        return CloudEventBuilder.v1()
                .withId(UUID.randomUUID().toString())
                .withSource(URI.create(CLOUD_EVENT_SOURCE))
                .withTime(transition.persistedAt().atOffset(ZoneOffset.UTC))
                .withType(eventName)
                .withExtension("namespace", transition.functionVersionId().toString())
                .withExtension("functionid", transition.functionId().toString())
                .withExtension("functionversionid", transition.functionVersionId().toString())
                .withExtension("deploymentid", transition.deploymentId().toString())
                .withExtension("ncaid", ncaId)
                .withData(jsonMapper.writeValueAsString(details).getBytes(StandardCharsets.UTF_8))
                .build();
    }

    private static WebClient authenticatedWebClient(
            String baseUrl,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri,
            String staticToken,
            WebClient.Builder builder) {
        ExchangeFilterFunction authFilter = !staticToken.isBlank()
                ? new FixedBearerExchangeFilterFunction(() -> staticToken)
                : NvcfOAuth2ClientUtils.getOAuth2ExchangeFilter(
                        builder, CLIENT_REGISTRATION_ID, tokenUri, clientId, clientSecret, scope);
        return builder.baseUrl(baseUrl)
                .filter(authFilter)
                .build();
    }
}
