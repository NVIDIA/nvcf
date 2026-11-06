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
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientEventLedgerProperties;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import io.cloudevents.CloudEvent;
import io.cloudevents.core.builder.CloudEventBuilder;
import io.cloudevents.core.format.EventFormat;
import io.cloudevents.core.provider.EventFormatProvider;
import jakarta.annotation.PreDestroy;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;
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
    private final boolean enabled;
    private final EventLedgerStubService service;
    private final JsonMapper jsonMapper;
    private final EventFormat eventFormat;
    private final ThreadPoolExecutor executor;
    private final EventLedgerMetricsService metrics;

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
            Optional<StaticClientEventLedgerProperties> staticClientEventLedgerProperties,
            WebClient.Builder webClientBuilder,
            JsonMapper jsonMapper,
            EventLedgerMetricsService metrics) {
        this(enabled, timeout, enabled
                      ? authenticatedWebClient(
                              baseUrl, clientId, clientSecret, scope, tokenUri,
                              staticClientEventLedgerProperties, webClientBuilder)
                      : webClientBuilder.baseUrl(baseUrl).build(), jsonMapper, metrics);
    }

    EventLedgerClient(
            boolean enabled,
            Duration timeout,
            WebClient webClient,
            JsonMapper jsonMapper,
            EventLedgerMetricsService metrics) {
        this.enabled = enabled;
        this.metrics = metrics;
        var adapter = WebClientAdapter.create(webClient);
        adapter.setBlockTimeout(timeout);
        this.service = HttpServiceProxyFactory.builderFor(adapter)
                .build()
                .createClient(EventLedgerStubService.class);
        this.jsonMapper = jsonMapper;
        this.eventFormat = EventFormatProvider.getInstance().resolveFormat(CONTENT_TYPE);
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
            metrics.recordRejected();
            logPublishFailure(ncaId, transition, ex);
        } catch (RuntimeException ex) {
            metrics.recordEnqueueFailure();
            logPublishFailure(ncaId, transition, ex);
        }
    }

    private void send(
            String ncaId, FunctionStatusTransition transition, String eventName) {
        try {
            service.publish(eventFormat.serialize(buildCloudEvent(ncaId, transition, eventName)));
        } catch (Exception ex) {
            metrics.recordPublicationFailure(ex);
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
            Optional<StaticClientEventLedgerProperties> staticClientEventLedgerProperties,
            WebClient.Builder builder) {
        ExchangeFilterFunction authFilter = staticClientEventLedgerProperties
                .map(p -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvcfOAuth2ClientUtils.getOAuth2ExchangeFilter(
                        builder, CLIENT_REGISTRATION_ID, tokenUri, clientId, clientSecret, scope));
        return builder.baseUrl(baseUrl)
                .filter(NvcfOAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(authFilter)
                .filter(NvcfOAuth2ClientUtils.getResponseFilterProcessor("Event Ledger"))
                .build();
    }
}
