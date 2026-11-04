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
package com.nvidia.ess.filter;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.within;
import static org.awaitility.Awaitility.await;
import static org.mockito.Mockito.clearInvocations;
import static org.mockito.Mockito.timeout;
import static org.mockito.Mockito.verify;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.core.read.ListAppender;
import com.nvidia.boot.audit.AuditAutoConfiguration;
import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.audit.event.BootAuditEvent;
import com.nvidia.boot.audit.listener.BootAuditEventListener;
import com.nvidia.ess.config.HmacConfig;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Base64;
import java.util.Map;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Nested;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.SpringBootConfiguration;
import org.springframework.boot.autoconfigure.ImportAutoConfiguration;
import org.springframework.boot.autoconfigure.context.PropertyPlaceholderAutoConfiguration;
import org.springframework.boot.http.codec.autoconfigure.CodecsAutoConfiguration;
import org.springframework.boot.jackson.autoconfigure.JacksonAutoConfiguration;
import org.springframework.boot.reactor.netty.autoconfigure.NettyReactiveWebServerAutoConfiguration;
import org.springframework.boot.security.autoconfigure.web.reactive.ReactiveWebSecurityAutoConfiguration;
import org.springframework.boot.webflux.autoconfigure.HttpHandlerAutoConfiguration;
import org.springframework.boot.webflux.autoconfigure.WebFluxAutoConfiguration;
import org.springframework.boot.webflux.autoconfigure.error.ErrorWebFluxAutoConfiguration;
import org.springframework.boot.webtestclient.autoconfigure.AutoConfigureWebTestClient;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.SpringBootTest.WebEnvironment;
import org.springframework.cloud.autoconfigure.RefreshAutoConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Import;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.security.config.web.server.ServerHttpSecurity;
import org.springframework.security.web.server.SecurityWebFilterChain;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RestController;
import reactor.core.publisher.Mono;

/**
 * Boots a minimal WebFlux test app with {@link AuditFilter} wired in, drives requests through it
 * with {@link WebTestClient}, and verifies the {@link BootAuditEvent} payload + listener log line
 * for both signed & unsigned audit-event code paths:
 */
class AuditFilterIT {

    private static final String TEST_KID = "audit-filter-it-kid";

    private static final String TEST_HMAC_KEYS_B64 = generateBase64KeyStore(TEST_KID);

    /**
     * Literal jsonBefore / jsonAfter hard-coded for every audited request.
     */
    private static final String JSON_BEFORE_VALUE = "started";
    private static final String JSON_AFTER_VALUE = "completed";

    abstract static class AbstractAuditFilterTests {

        @Autowired
        protected WebTestClient webTestClient;

        @MockitoSpyBean
        protected BootAuditEventListener auditEventListener;

        protected Logger contextLogger;
        protected ListAppender<ILoggingEvent> contextLogAppender;

        @BeforeEach
        void attachContextLogAppender() {
            contextLogger = (Logger) LoggerFactory.getLogger(
                    org.slf4j.Logger.ROOT_LOGGER_NAME + "."
                            + AuditService.class.getCanonicalName());
            contextLogAppender = new ListAppender<>();
            contextLogAppender.start();
            contextLogger.addAppender(contextLogAppender);
            clearInvocations(auditEventListener);
        }

        @AfterEach
        void detachContextLogAppender() {
            contextLogger.detachAppender(contextLogAppender);
            contextLogAppender.stop();
        }

        protected BootAuditEvent awaitCapturedAuditEvent() {
            ArgumentCaptor<BootAuditEvent> captor = ArgumentCaptor.forClass(BootAuditEvent.class);
            // BootAuditEventListener is @Async; wait up to 5s for the executor thread to dispatch.
            verify(auditEventListener, timeout(5_000)).onBootAuditEvent(captor.capture());
            return captor.getValue();
        }

        protected void assertPayloadMatches(
                BootAuditEvent event,
                String expectedOperation,
                String expectedPathSuffix,
                String expectedStatusCodeSummary,
                boolean signed) {
            AuditEventPayload payload = event.getPayload();

            assertThat(payload.getId()).as("payload.id (random UUID)").isNotNull();
            // Timestamp checked for recency. ±10s window to generously account for any clock-drift.
            assertThat(payload.getTimestamp())
                    .as("payload.timestamp set to Instant.now() at build time")
                    .isNotNull()
                    .isCloseTo(Instant.now(), within(10L, ChronoUnit.SECONDS));
            assertThat(payload.getMachineId())
                    .as("payload.machineId resolved lazily from the host's MAC address (or "
                            + "'unknown')")
                    .isNotBlank();

            // ---- set explicitly by AuditFilter#logAuthResult ----
            assertThat(payload.getOperation()).isEqualTo(expectedOperation);
            assertThat(payload.getType()).isEqualTo("API");
            assertThat(payload.getObjectId()).isEqualTo("N/A");
            assertThat(payload.getState()).isEqualTo("N/A");
            assertThat(payload.getSummary())
                    .as("AuditFilter sets summary to "
                            + "exchange.getResponse().getStatusCode().toString()")
                    .isEqualTo(expectedStatusCodeSummary);
            assertThat(payload.getObjectLocation())
                    .as("objectLocation is exchange.getRequest().getURI().toString() - "
                            + "Reactor-Netty under WebTestClient surfaces this as the "
                            + "request-target (path only); other servers may prepend "
                            + "scheme://host:port, so we only anchor on the trailing path.")
                    .isNotNull()
                    .endsWith(expectedPathSuffix);

            // AuditFilter falls back to "unknown" for both actor and subject when the
            // exchange has no AuthorizationInfo.
            assertThat(payload.getActorId()).isEqualTo("unknown");
            assertThat(payload.getSubjectId()).isEqualTo("unknown");

            // AuditFilter derives both location fields from the same
            // request.getRemoteAddress().getHostName() value (with a "" fallback when the remote
            // address is null). Under WebTestClient the in-process transport may surface the loopback
            // remote address (e.g. "localhost") or none at all, depending on the runtime; this is a
            // test-transport detail only - in production the remote address is always populated, so
            // the audit payload is unaffected. We therefore assert the invariant that both fields
            // share the single remoteAddress-derived value rather than a transport-specific literal.
            assertThat(payload.getActorLocation()).as("actorLocation").isNotNull();
            assertThat(payload.getSubjectLocation())
                    .as("subjectLocation mirrors actorLocation (same remoteAddress source)")
                    .isEqualTo(payload.getActorLocation());

            assertThat(payload.getGroupType()).as("AuditFilter never sets groupType").isNull();
            assertThat(payload.getData())
                    .as("AuditFilter never calls builder.custom(...)")
                    .isEmpty();

            assertThat(payload.getStateSummary())
                    .as("RFC 6902 JSON-Patch from jsonBefore to jsonAfter")
                    .isNotNull()
                    .contains("\"op\":\"replace\"")
                    .contains("\"path\":\"/request\"")
                    .contains("\"value\":\"" + JSON_AFTER_VALUE + "\"");
            assertThat(payload.getHistorySummary())
                    .as("RFC 6902 JSON-Patch from jsonAfter to jsonBefore (reverse of "
                            + "stateSummary)")
                    .isNotNull()
                    .contains("\"op\":\"replace\"")
                    .contains("\"path\":\"/request\"")
                    .contains("\"value\":\"" + JSON_BEFORE_VALUE + "\"");

            // ---- HMAC fields: only populated in the signed code path ----
            if (signed) {
                assertThat(payload.getHmacBefore())
                        .as("hmacBefore covers jsonBefore={\"request\":\"%s\"}", JSON_BEFORE_VALUE)
                        .isNotNull()
                        .matches(AuditEventPayload.FORMATTED_HMAC_PATTERN)
                        .startsWith("HMac-SHA3-512:" + TEST_KID + ":");
                assertThat(payload.getHmacAfter())
                        .as("hmacAfter covers jsonAfter={\"request\":\"%s\"}", JSON_AFTER_VALUE)
                        .isNotNull()
                        .matches(AuditEventPayload.FORMATTED_HMAC_PATTERN)
                        .startsWith("HMac-SHA3-512:" + TEST_KID + ":");
                assertThat(event.getHmac())
                        .as("event-level hmac over the entire serialized payload")
                        .isNotNull()
                        .matches(AuditEventPayload.FORMATTED_HMAC_PATTERN)
                        .startsWith("HMac-SHA3-512:" + TEST_KID + ":");
            } else {
                assertThat(payload.getHmacBefore())
                        .as("hmacBefore is null when no signing context is resolved")
                        .isNull();
                assertThat(payload.getHmacAfter())
                        .as("hmacAfter is null when no signing context is resolved")
                        .isNull();
                assertThat(event.getHmac())
                        .as("event-level hmac is null when no signing context is resolved")
                        .isNull();
            }

            assertThat(event.getSourceClassName())
                    .as("getSourceClassName drives BootAuditEventListener's context-logger name")
                    .isEqualTo(AuditService.class.getCanonicalName());
        }

        protected void assertContextLoggerEmittedAuditLine(BootAuditEvent capturedEvent) {
            String expectedLoggerName = org.slf4j.Logger.ROOT_LOGGER_NAME + "."
                    + AuditService.class.getCanonicalName();

            String expectedMessage = "[AUDIT] " + capturedEvent.toJson();

            await().atMost(Duration.ofSeconds(5)).untilAsserted(() -> {
                assertThat(contextLogAppender.list)
                        .as("context-logger '%s' should have received the listener's audit line",
                                expectedLoggerName)
                        .isNotEmpty();
                ILoggingEvent loggedEvent = contextLogAppender.list.get(0);
                assertThat(loggedEvent.getLoggerName()).isEqualTo(expectedLoggerName);
                assertThat(loggedEvent.getLevel()).isEqualTo(Level.INFO);
                assertThat(loggedEvent.getFormattedMessage()).isEqualTo(expectedMessage);
            });
        }
    }

    /**
     * Exercises the signed audit-event code path.
     */
    @Nested
    @DisplayName("audit.hmac.kid + audit.hmac.keys configured -> signed events")
    @SpringBootTest(
            classes = AuditFilterIT.SignedTestApp.class,
            webEnvironment = WebEnvironment.RANDOM_PORT,
            properties = {
                "spring.application.name=audit-filter-it",
                "spring.application.version=0.0.0-it",
                "spring.profiles.active=test",
                "spring.cloud.bootstrap.enabled=false",
                "spring.cloud.config.enabled=false",
                "spring.main.allow-bean-definition-overriding=true",
                "spring.webflux.problemdetails.enabled=true",
                "nv-boot.reloadable-properties.enabled=false",
                "management.tracing.enabled=false",
                "management.otlp.tracing.export.enabled=false",
                "metrics.server.enabled=false",
            })
    @AutoConfigureWebTestClient(timeout = "10s")
    class Signed extends AbstractAuditFilterTests {

        /**
         * {@link DynamicPropertySource} registers HMAC properties here.
         */
        @DynamicPropertySource
        static void hmacProperties(DynamicPropertyRegistry registry) {
            registry.add("audit.hmac.kid", () -> TEST_KID);
            registry.add("audit.hmac.keys", () -> TEST_HMAC_KEYS_B64);
        }

        @Test
        void getRequestUnderV1_publishesSignedAuditEvent_andContextLoggerEmitsAuditLine() {
            webTestClient.get()
                    .uri("/v1/get-path")
                    .exchange()
                    .expectStatus().isOk()
                    .expectHeader().contentType(MediaType.APPLICATION_JSON)
                    .expectBody().json("{\"hello\":\"world\"}");

            BootAuditEvent event = awaitCapturedAuditEvent();
            assertPayloadMatches(event, "GET", "/v1/get-path", "200 OK", true);
            assertContextLoggerEmittedAuditLine(event);
        }

        @Test
        void postRequestUnderV1_publishesSignedAuditEvent_andContextLoggerEmitsAuditLine() {
            webTestClient.post()
                    .uri("/v1/post-path")
                    .exchange()
                    .expectStatus().isOk()
                    .expectHeader().contentType(MediaType.APPLICATION_JSON)
                    .expectBody().json("{\"hello\":\"world\"}");

            BootAuditEvent event = awaitCapturedAuditEvent();
            assertPayloadMatches(event, "POST", "/v1/post-path", "200 OK", true);
            assertContextLoggerEmittedAuditLine(event);
        }

        @Test
        void deleteRequestUnderV1_publishesSignedAuditEvent_andContextLoggerEmitsAuditLine() {
            webTestClient.delete()
                    .uri("/v1/delete-path")
                    .exchange()
                    .expectStatus().isNoContent()
                    .expectBody().isEmpty();

            BootAuditEvent event = awaitCapturedAuditEvent();
            assertPayloadMatches(event, "DELETE", "/v1/delete-path", "204 NO_CONTENT", true);
            assertContextLoggerEmittedAuditLine(event);
        }

        @Test
        void unmappedV1Path_publishesSignedAuditEventWith404_andContextLoggerEmitsAuditLine() {
            webTestClient.get()
                    .uri("/v1/wrong-path")
                    .exchange()
                    .expectStatus().isNotFound();

            BootAuditEvent event = awaitCapturedAuditEvent();
            assertPayloadMatches(event, "GET", "/v1/wrong-path", "404 NOT_FOUND", true);
            assertContextLoggerEmittedAuditLine(event);
        }
    }

    /**
     * Exercises the unsigned audit-event code path.
     */
    @Nested
    @DisplayName("audit.hmac.* absent -> unsigned events with null hmac fields")
    @SpringBootTest(
            classes = AuditFilterIT.UnsignedTestApp.class,
            webEnvironment = WebEnvironment.RANDOM_PORT,
            properties = {
                "spring.application.name=audit-filter-it",
                "spring.application.version=0.0.0-it",
                "spring.profiles.active=test",
                "spring.cloud.bootstrap.enabled=false",
                "spring.cloud.config.enabled=false",
                "spring.main.allow-bean-definition-overriding=true",
                "spring.webflux.problemdetails.enabled=true",
                "nv-boot.reloadable-properties.enabled=false",
                "management.tracing.enabled=false",
                "management.otlp.tracing.export.enabled=false",
                "metrics.server.enabled=false",
            })
    @AutoConfigureWebTestClient(timeout = "10s")
    class Unsigned extends AbstractAuditFilterTests {

        @Test
        void getRequestUnderV1_publishesUnsignedAuditEvent_andContextLoggerEmitsAuditLine() {
            webTestClient.get()
                    .uri("/v1/get-path")
                    .exchange()
                    .expectStatus().isOk()
                    .expectHeader().contentType(MediaType.APPLICATION_JSON)
                    .expectBody().json("{\"hello\":\"world\"}");

            BootAuditEvent event = awaitCapturedAuditEvent();
            assertPayloadMatches(event, "GET", "/v1/get-path", "200 OK", false);
            assertContextLoggerEmittedAuditLine(event);
        }

        @Test
        void postRequestUnderV1_publishesUnsignedAuditEvent_andContextLoggerEmitsAuditLine() {
            webTestClient.post()
                    .uri("/v1/post-path")
                    .exchange()
                    .expectStatus().isOk()
                    .expectHeader().contentType(MediaType.APPLICATION_JSON)
                    .expectBody().json("{\"hello\":\"world\"}");

            BootAuditEvent event = awaitCapturedAuditEvent();
            assertPayloadMatches(event, "POST", "/v1/post-path", "200 OK", false);
            assertContextLoggerEmittedAuditLine(event);
        }

        @Test
        void deleteRequestUnderV1_publishesUnsignedAuditEvent_andContextLoggerEmitsAuditLine() {
            webTestClient.delete()
                    .uri("/v1/delete-path")
                    .exchange()
                    .expectStatus().isNoContent()
                    .expectBody().isEmpty();

            BootAuditEvent event = awaitCapturedAuditEvent();
            assertPayloadMatches(event, "DELETE", "/v1/delete-path", "204 NO_CONTENT", false);
            assertContextLoggerEmittedAuditLine(event);
        }

        @Test
        void unmappedV1Path_publishesUnsignedAuditEventWith404_andContextLoggerEmitsAuditLine() {
            webTestClient.get()
                    .uri("/v1/wrong-path")
                    .exchange()
                    .expectStatus().isNotFound();

            BootAuditEvent event = awaitCapturedAuditEvent();
            assertPayloadMatches(event, "GET", "/v1/wrong-path", "404 NOT_FOUND", false);
            assertContextLoggerEmittedAuditLine(event);
        }
    }

    private static String generateBase64KeyStore(String kid) {
        byte[] keyBytes = new byte[64];
        new SecureRandom().nextBytes(keyBytes);
        String keyB64 = Base64.getEncoder().encodeToString(keyBytes);

        String keyStoreJson =
                "{\"keys\":[{\"kid\":\"" + kid + "\",\"key\":\"" + keyB64 + "\"}]}";
        return Base64.getEncoder().encodeToString(keyStoreJson.getBytes(StandardCharsets.UTF_8));
    }

    /**
     * Test app for the signed audit-event code path
     */
    @SpringBootConfiguration
    @ImportAutoConfiguration({
        PropertyPlaceholderAutoConfiguration.class,
        JacksonAutoConfiguration.class,
        CodecsAutoConfiguration.class,
        HttpHandlerAutoConfiguration.class,
        NettyReactiveWebServerAutoConfiguration.class,
        WebFluxAutoConfiguration.class,
        ErrorWebFluxAutoConfiguration.class,
        // Registers the "refresh" scope used by HmacConfig.auditProperties().
        RefreshAutoConfiguration.class,
        AuditAutoConfiguration.class,
        // Supplies the ServerHttpSecurity builder that permitAllSecurityWebFilterChain consumes.
        // (The chain below is permitAll, so it does not intercept/short-circuit requests.)
        ReactiveWebSecurityAutoConfiguration.class,
    })
    @Import({AuditFilter.class, HmacConfig.class, AuditFilterIT.TestController.class})
    static class SignedTestApp {

        /**
         * Permit-all SecurityWebFilterChain so the audit pipeline can be exercised E2E without
         * standing up an OAuth2 identity provider.
         */
        @Bean
        SecurityWebFilterChain permitAllSecurityWebFilterChain(ServerHttpSecurity http) {
            // Test-only permit-all chain. CSRF is disabled because this scaffolding has no
            // authentication at all; it only lets the audit filter run end to end.
            return http.csrf(ServerHttpSecurity.CsrfSpec::disable)
                    .authorizeExchange(ex -> ex.anyExchange().permitAll())
                    .build();
        }
    }

    /**
     * Test app for the unsigned audit-event code path. Identical to {@link SignedTestApp}.
     */
    @SpringBootConfiguration
    @ImportAutoConfiguration({
        PropertyPlaceholderAutoConfiguration.class,
        JacksonAutoConfiguration.class,
        CodecsAutoConfiguration.class,
        HttpHandlerAutoConfiguration.class,
        NettyReactiveWebServerAutoConfiguration.class,
        WebFluxAutoConfiguration.class,
        ErrorWebFluxAutoConfiguration.class,
        RefreshAutoConfiguration.class,
        AuditAutoConfiguration.class,
        // Supplies the ServerHttpSecurity builder that permitAllSecurityWebFilterChain consumes.
        // (The chain below is permitAll, so it does not intercept/short-circuit requests.)
        ReactiveWebSecurityAutoConfiguration.class,
    })
    @Import({AuditFilter.class, HmacConfig.class, AuditFilterIT.TestController.class})
    static class UnsignedTestApp {

        @Bean
        SecurityWebFilterChain permitAllSecurityWebFilterChain(ServerHttpSecurity http) {
            // Test-only permit-all chain. CSRF is disabled because this scaffolding has no
            // authentication at all; it only lets the audit filter run end to end.
            return http.csrf(ServerHttpSecurity.CsrfSpec::disable)
                    .authorizeExchange(ex -> ex.anyExchange().permitAll())
                    .build();
        }
    }

    @RestController
    static class TestController {

        @GetMapping(value = "/v1/get-path", produces = MediaType.APPLICATION_JSON_VALUE)
        Mono<ResponseEntity<Map<String, String>>> get() {
            return Mono.just(ResponseEntity.ok(Map.of("hello", "world")));
        }

        @PostMapping(value = "/v1/post-path", produces = MediaType.APPLICATION_JSON_VALUE)
        Mono<ResponseEntity<Map<String, String>>> post() {
            return Mono.just(ResponseEntity.ok(Map.of("hello", "world")));
        }

        @DeleteMapping("/v1/delete-path")
        Mono<ResponseEntity<Void>> delete() {
            return Mono.just(ResponseEntity.noContent().build());
        }
    }
}
