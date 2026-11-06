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

import static com.github.tomakehurst.wiremock.client.WireMock.aResponse;
import static com.github.tomakehurst.wiremock.client.WireMock.containing;
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.postRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static com.github.tomakehurst.wiremock.core.WireMockConfiguration.options;
import static io.cloudevents.jackson.JsonFormat.CONTENT_TYPE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.awaitility.Awaitility.await;
import static org.junit.jupiter.params.provider.Arguments.arguments;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientEventLedgerProperties;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import io.cloudevents.core.provider.EventFormatProvider;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicReference;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.convert.ApplicationConversionService;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.cloud.autoconfigure.RefreshAutoConfiguration;
import org.springframework.web.reactive.function.client.WebClient;
import tools.jackson.databind.json.JsonMapper;

class EventLedgerClientTest {

    private WireMockServer server;
    private EventLedgerClient client;
    private SimpleMeterRegistry meterRegistry;

    @BeforeEach
    void setUp() {
        server = new WireMockServer(options().dynamicPort());
        server.start();
        meterRegistry = new SimpleMeterRegistry();
        client = newClient(Duration.ofSeconds(1));
    }

    @AfterEach
    void tearDown() {
        client.close();
        meterRegistry.close();
        server.stop();
    }

    @ParameterizedTest
    @MethodSource("statusMappings")
    void publishesStructuredCloudEvent(FunctionStatus status, String eventName) throws Exception {
        server.stubFor(post(urlEqualTo(EventLedgerClient.CLOUD_EVENTS_PATH))
                               .willReturn(aResponse().withStatus(202)));
        var previousStatus = status == FunctionStatus.INACTIVE
                ? FunctionStatus.ERROR
                : FunctionStatus.INACTIVE;
        var functionId = UUID.randomUUID();
        var functionVersionId = UUID.randomUUID();
        var deploymentId = UUID.randomUUID();
        var persistedAt = Instant.parse("2026-09-08T12:34:56Z");

        client.publish(
                "account-1", functionId, functionVersionId, deploymentId,
                previousStatus, status, persistedAt);

        await().atMost(Duration.ofSeconds(3))
                .untilAsserted(() -> assertThat(server.getAllServeEvents()).hasSize(1));
        var request = server.getAllServeEvents().getFirst().getRequest();
        var cloudEvent = EventFormatProvider.getInstance()
                .resolveFormat(CONTENT_TYPE)
                .deserialize(request.getBody());
        assertThat(request.getHeader("Content-Type"))
                .isEqualTo(EventLedgerClient.CLOUD_EVENTS_CONTENT_TYPE);
        assertThat(cloudEvent.getSpecVersion().toString()).isEqualTo("1.0");
        assertThat(cloudEvent.getId()).isNotBlank();
        assertThat(cloudEvent.getSource().toString())
                .isEqualTo(EventLedgerClient.CLOUD_EVENT_SOURCE);
        assertThat(cloudEvent.getType()).isEqualTo(eventName);
        assertThat(cloudEvent.getTime()).isEqualTo(persistedAt.atOffset(ZoneOffset.UTC));
        assertThat(cloudEvent.getExtension("namespace")).isEqualTo(functionVersionId.toString());
        assertThat(cloudEvent.getExtension("functionid")).isEqualTo(functionId.toString());
        assertThat(cloudEvent.getExtension("functionversionid"))
                .isEqualTo(functionVersionId.toString());
        assertThat(cloudEvent.getExtension("deploymentid")).isEqualTo(deploymentId.toString());
        assertThat(cloudEvent.getExtension("ncaid")).isEqualTo("account-1");
        var data = new JsonMapper().readTree(cloudEvent.getData().toBytes());
        assertThat(data.get("previousStatus").asText())
                .isEqualTo(previousStatus.toString());
        assertThat(data.get("currentStatus").asText())
                .isEqualTo(status.toString());
    }

    @Test
    void springCreatesEnabledClientUsingProductionConstructor() {
        new ApplicationContextRunner()
                .withConfiguration(AutoConfigurations.of(RefreshAutoConfiguration.class))
                .withInitializer(context -> context.getBeanFactory()
                        .setConversionService(ApplicationConversionService.getSharedInstance()))
                .withPropertyValues(
                        "nvcf.event-ledger.enabled=true",
                        "spring.security.oauth2.client.registration.event-ledger.client-id=test",
                        "spring.security.oauth2.client.registration.event-ledger.client-secret=test",
                        "spring.security.oauth2.client.registration.event-ledger.scope=test",
                        "spring.security.oauth2.client.provider.event-ledger.token-uri=http://token")
                .withBean(WebClient.Builder.class, WebClient::builder)
                .withBean(JsonMapper.class, JsonMapper::new)
                .withBean(SimpleMeterRegistry.class, SimpleMeterRegistry::new)
                .withUserConfiguration(EventLedgerMetricsService.class, EventLedgerClient.class)
                .run(context -> {
                    assertThat(context).hasNotFailed();
                    assertThat(context.getBean("scopedTarget.eventLedgerClient"))
                            .isInstanceOf(EventLedgerClient.class);
                });
    }

    @Test
    void backgroundPublicationFetchesOAuthTokenWithClientCredentials() {
        var publishingThread = new AtomicReference<String>();
        server.stubFor(post(urlEqualTo("/oauth/token"))
                               .withHeader("Authorization", equalTo("Basic dGVzdC1jbGllbnQ6dGVzdC1zZWNyZXQ="))
                               .withRequestBody(containing("grant_type=client_credentials"))
                               .withRequestBody(containing("scope=fnds%3AcreateEvent"))
                               .willReturn(aResponse().withStatus(200)
                                       .withHeader("Content-Type", "application/json")
                                       .withBody("{\"access_token\":\"oauth-test-token\",\"token_type\":\"Bearer\",\"expires_in\":3600}")));
        server.stubFor(post(urlEqualTo(EventLedgerClient.CLOUD_EVENTS_PATH))
                               .withHeader("Authorization", equalTo("Bearer oauth-test-token"))
                               .willReturn(aResponse().withStatus(202)));

        new ApplicationContextRunner()
                .withConfiguration(AutoConfigurations.of(RefreshAutoConfiguration.class))
                .withInitializer(context -> context.getBeanFactory()
                        .setConversionService(ApplicationConversionService.getSharedInstance()))
                .withPropertyValues(
                        "nvcf.event-ledger.enabled=true",
                        "nvcf.event-ledger.base-url=" + server.baseUrl(),
                        "spring.security.oauth2.client.registration.event-ledger.client-id=test-client",
                        "spring.security.oauth2.client.registration.event-ledger.client-secret=test-secret",
                        "spring.security.oauth2.client.registration.event-ledger.scope=fnds:createEvent",
                        "spring.security.oauth2.client.provider.event-ledger.token-uri="
                                + server.baseUrl() + "/oauth/token")
                .withBean(WebClient.Builder.class, () -> WebClient.builder().filter((request, next) -> {
                    if (request.url().getPath().equals(EventLedgerClient.CLOUD_EVENTS_PATH)) {
                        publishingThread.set(Thread.currentThread().getName());
                    }
                    return next.exchange(request);
                }))
                .withBean(JsonMapper.class, JsonMapper::new)
                .withBean(SimpleMeterRegistry.class, SimpleMeterRegistry::new)
                .withUserConfiguration(EventLedgerMetricsService.class, EventLedgerClient.class)
                .run(context -> {
                    assertThat(context).hasNotFailed();
                    context.getBean(EventLedgerClient.class).publish(
                            "account-1", UUID.randomUUID(), UUID.randomUUID(), UUID.randomUUID(),
                            FunctionStatus.INACTIVE, FunctionStatus.DEPLOYING, Instant.now());

                    await().atMost(Duration.ofSeconds(5)).untilAsserted(() -> {
                        server.verify(1, postRequestedFor(urlEqualTo("/oauth/token")));
                        server.verify(1, postRequestedFor(urlEqualTo(EventLedgerClient.CLOUD_EVENTS_PATH))
                                .withHeader("Authorization", equalTo("Bearer oauth-test-token")));
                    });
                    assertThat(publishingThread.get()).isEqualTo("event-ledger-publisher");
                    assertThat(context.getBean(SimpleMeterRegistry.class)
                                       .get("nvcf_cloud_functions_event_ledger_publish_failures_total")
                                       .counter().count()).isZero();
                });
    }

    @Test
    void springCreatesClientWithStaticTokenWithoutOAuthConfiguration() {
        server.stubFor(post(urlEqualTo(EventLedgerClient.CLOUD_EVENTS_PATH))
                               .withHeader("Authorization", equalTo("Bearer test-token"))
                               .willReturn(aResponse().withStatus(202)));
        new ApplicationContextRunner()
                .withConfiguration(AutoConfigurations.of(RefreshAutoConfiguration.class))
                .withInitializer(context -> context.getBeanFactory()
                        .setConversionService(ApplicationConversionService.getSharedInstance()))
                .withPropertyValues("nvcf.event-ledger.enabled=true",
                                    "nvcf.event-ledger.base-url=" + server.baseUrl(),
                                    "nvcf.event-ledger.static.token=test-token")
                .withBean(WebClient.Builder.class, WebClient::builder)
                .withBean(JsonMapper.class, JsonMapper::new)
                .withBean(SimpleMeterRegistry.class, SimpleMeterRegistry::new)
                .withUserConfiguration(StaticClientEventLedgerProperties.class,
                                       EventLedgerMetricsService.class, EventLedgerClient.class)
                .run(context -> {
                    assertThat(context).hasNotFailed();
                    var eventLedgerClient = context.getBean(EventLedgerClient.class);
                    eventLedgerClient.publish("account-1", UUID.randomUUID(), UUID.randomUUID(),
                                              UUID.randomUUID(), FunctionStatus.INACTIVE,
                                              FunctionStatus.DEPLOYING, Instant.now());
                    await().atMost(Duration.ofSeconds(3))
                            .untilAsserted(() -> assertThat(server.getAllServeEvents()).hasSize(1));
                    assertThat(server.getAllServeEvents().getFirst().getRequest()
                                       .getHeader("Authorization")).isEqualTo("Bearer test-token");
                });
    }

    @Test
    void springCreatesDisabledClientWithoutOAuthConfiguration() {
        new ApplicationContextRunner()
                .withConfiguration(AutoConfigurations.of(RefreshAutoConfiguration.class))
                .withInitializer(context -> context.getBeanFactory()
                        .setConversionService(ApplicationConversionService.getSharedInstance()))
                .withBean(WebClient.Builder.class, WebClient::builder)
                .withBean(JsonMapper.class, JsonMapper::new)
                .withBean(SimpleMeterRegistry.class, SimpleMeterRegistry::new)
                .withUserConfiguration(EventLedgerMetricsService.class, EventLedgerClient.class)
                .run(context -> {
                    assertThat(context).hasNotFailed();
                    assertThat(context.getBean("scopedTarget.eventLedgerClient"))
                            .isInstanceOf(EventLedgerClient.class);
                });
    }

    @Test
    void handlesPublishFailureWithoutThrowing() {
        server.stubFor(post(urlEqualTo(EventLedgerClient.CLOUD_EVENTS_PATH))
                               .withHeader("Content-Type",
                                           equalTo(EventLedgerClient.CLOUD_EVENTS_CONTENT_TYPE))
                               .willReturn(aResponse().withStatus(500)));

        publish(FunctionStatus.DEPLOYING, FunctionStatus.ERROR);

        await().atMost(Duration.ofSeconds(3))
                .untilAsserted(() -> assertThat(meterRegistry.get(
                        "nvcf_cloud_functions_event_ledger_publish_failures_total")
                        .counter().count()).isEqualTo(1));
        assertThat(meterRegistry.get("nvcf_cloud_functions_event_ledger_server_errors_total")
                           .counter().count()).isEqualTo(1);
        assertThat(server.getAllServeEvents()).hasSize(1);
    }

    @Test
    void countsClientErrorsWithoutFailingTransition() {
        server.stubFor(post(urlEqualTo(EventLedgerClient.CLOUD_EVENTS_PATH))
                               .willReturn(aResponse().withStatus(403)));

        publish(FunctionStatus.INACTIVE, FunctionStatus.DEPLOYING);

        await().atMost(Duration.ofSeconds(3))
                .untilAsserted(() -> assertThat(meterRegistry.get(
                        "nvcf_cloud_functions_event_ledger_client_errors_total")
                        .counter().count()).isEqualTo(1));
        assertThat(meterRegistry.get("nvcf_cloud_functions_event_ledger_publish_failures_total")
                           .counter().count()).isEqualTo(1);
    }

    @Test
    void countsRejectedEventsWhenQueueIsFull() {
        server.stubFor(post(urlEqualTo(EventLedgerClient.CLOUD_EVENTS_PATH))
                               .willReturn(aResponse().withStatus(202).withFixedDelay(1000)));

        for (int i = 0; i < 258; i++) {
            publish(FunctionStatus.INACTIVE, FunctionStatus.DEPLOYING);
        }

        assertThat(meterRegistry.get("nvcf_cloud_functions_event_ledger_rejected_total")
                           .counter().count()).isGreaterThanOrEqualTo(1);
    }

    @Test
    void doesNotWaitForEventLedgerResponse() {
        server.stubFor(post(urlEqualTo(EventLedgerClient.CLOUD_EVENTS_PATH))
                               .willReturn(aResponse().withStatus(202).withFixedDelay(250)));

        var startedAt = Instant.now();
        publish(FunctionStatus.DEPLOYING, FunctionStatus.ACTIVE);

        assertThat(Duration.between(startedAt, Instant.now()))
                .isLessThan(Duration.ofMillis(200));
        await().atMost(Duration.ofSeconds(3))
                .untilAsserted(() -> assertThat(server.getAllServeEvents()).hasSize(1));
    }

    @Test
    void skipsPublishingWhenDisabled() {
        client.close();
        client = newClient(Duration.ofSeconds(1), false);

        publish(FunctionStatus.DEPLOYING, FunctionStatus.ACTIVE);

        assertThat(server.getAllServeEvents()).isEmpty();
    }

    private EventLedgerClient newClient(Duration timeout) {
        return newClient(timeout, true);
    }

    private EventLedgerClient newClient(Duration timeout, boolean enabled) {
        return new EventLedgerClient(
                enabled,
                timeout,
                WebClient.builder().baseUrl(server.baseUrl())
                        .filter(NvcfOAuth2ClientUtils.getResponseFilterProcessor("Event Ledger"))
                        .build(),
                new JsonMapper(), new EventLedgerMetricsService(meterRegistry));
    }

    private void publish(
            FunctionStatus previousStatus, FunctionStatus currentStatus) {
        client.publish(
                "account-1", UUID.randomUUID(), UUID.randomUUID(), UUID.randomUUID(),
                previousStatus, currentStatus, Instant.parse("2026-09-08T12:34:56Z"));
    }

    private static Stream<Arguments> statusMappings() {
        return Stream.of(
                arguments(FunctionStatus.DEPLOYING, "Function.Deploying"),
                arguments(FunctionStatus.ACTIVE, "Function.Ready"),
                arguments(FunctionStatus.DEGRADING, "Function.Degrading"),
                arguments(FunctionStatus.DEGRADED, "Function.Degraded"),
                arguments(FunctionStatus.ERROR, "Function.Error"),
                arguments(FunctionStatus.INACTIVE, "Function.Inactive"));
    }
}
