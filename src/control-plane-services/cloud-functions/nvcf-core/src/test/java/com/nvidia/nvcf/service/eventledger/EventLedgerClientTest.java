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
import static com.github.tomakehurst.wiremock.client.WireMock.equalTo;
import static com.github.tomakehurst.wiremock.client.WireMock.post;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static com.github.tomakehurst.wiremock.core.WireMockConfiguration.options;
import static org.assertj.core.api.Assertions.assertThat;
import static org.awaitility.Awaitility.await;
import static org.junit.jupiter.params.provider.Arguments.arguments;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import java.time.Duration;
import java.time.Instant;
import java.util.UUID;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
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

    @BeforeEach
    void setUp() {
        server = new WireMockServer(options().dynamicPort());
        server.start();
        client = newClient(Duration.ofSeconds(1));
    }

    @AfterEach
    void tearDown() {
        client.close();
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

        await().untilAsserted(() -> assertThat(server.getAllServeEvents()).hasSize(1));
        var request = server.getAllServeEvents().getFirst().getRequest();
        var payload = new JsonMapper().readTree(request.getBody());
        assertThat(request.getHeader("Content-Type"))
                .isEqualTo(EventLedgerClient.CLOUD_EVENTS_CONTENT_TYPE);
        assertThat(payload.get("specversion").asText()).isEqualTo("1.0");
        assertThat(payload.get("id").asText()).isNotBlank();
        assertThat(payload.get("source").asText()).isEqualTo("cloud-functions");
        assertThat(payload.get("type").asText()).isEqualTo(eventName);
        assertThat(payload.get("time").asText()).isEqualTo(persistedAt.toString());
        assertThat(payload.get("namespace").asText()).isEqualTo("account-1");
        assertThat(payload.get("deploymentId").asText())
                .isEqualTo(deploymentId.toString());
        assertThat(payload.get("data").get("functionId").asText())
                .isEqualTo(functionId.toString());
        assertThat(payload.get("data").get("functionVersionId").asText())
                .isEqualTo(functionVersionId.toString());
        assertThat(payload.get("data").get("deploymentId").asText())
                .isEqualTo(deploymentId.toString());
        assertThat(payload.get("data").get("previousStatus").asText())
                .isEqualTo(previousStatus.toString());
        assertThat(payload.get("data").get("currentStatus").asText())
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
                .withUserConfiguration(EventLedgerClient.class)
                .run(context -> {
                    assertThat(context).hasNotFailed();
                    assertThat(context.getBean("scopedTarget.eventLedgerClient"))
                            .isInstanceOf(EventLedgerClient.class);
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
                .withUserConfiguration(EventLedgerClient.class)
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

        await().untilAsserted(() -> assertThat(server.getAllServeEvents()).hasSize(1));
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
        var executor = new ThreadPoolExecutor(
                1, 1, 0, TimeUnit.MILLISECONDS, new ArrayBlockingQueue<>(10));
        return new EventLedgerClient(
                enabled,
                timeout,
                WebClient.builder().baseUrl(server.baseUrl()).build(),
                new JsonMapper(),
                executor);
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
