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
package com.nvidia.ess.controller;

import static com.nvidia.ess.constants.Constants.X_ESS_AGENT_ID_HEADER;
import static com.nvidia.ess.constants.Constants.X_ESS_NAMESPACE_HEADER;
import static com.nvidia.ess.constants.Constants.X_ESS_REQUEST_ID_HEADER;
import static com.nvidia.ess.constants.Constants.X_ESS_TOKEN_HEADER;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.AGENT_ID_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.ENTITY_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.EXHAUSTED_RETRIES_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.MAX_RETRY_ATTEMPTS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.MAX_RETRY_BACKOFF_TIME_MILLIS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.MIN_RETRY_BACKOFF_TIME_MILLIS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.NAMESPACE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.REQUEST_ID_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.RETRY_NUM_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_QUERY_TYPE_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.SECRET_READ_AUTH_TYPE_KEY;
import static com.nvidia.ess.util.ControllerTestCases.buildUrl;
import static com.nvidia.ess.util.ControllerTestCases.invalidHeadersArguments;
import static com.nvidia.ess.util.ControllerTestCases.invalidNamespaceOrEntityTypeOrEntityNameArguments;
import static com.nvidia.ess.util.ControllerTestCases.invalidSecretPathArguments;
import static com.nvidia.ess.util.TestConstants.DELETION_TEST_SECRET_PATH;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY_ID;
import static com.nvidia.ess.util.TestConstants.TEST_ENTITY_TYPE;
import static com.nvidia.ess.util.TestConstants.TEST_ESS_AGENT_ID;
import static com.nvidia.ess.util.TestConstants.TEST_NAMESPACE;
import static com.nvidia.ess.util.TestConstants.TEST_PROBLEM_SUMMARY;
import static com.nvidia.ess.util.TestConstants.TEST_SECRET_PATH;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.ess.auth.AuthChecker;
import com.nvidia.ess.config.BeanConfig;
import com.nvidia.ess.config.SecurityConfiguration;
import com.nvidia.ess.config.WebConfiguration;
import com.nvidia.ess.config.properties.LoggingProperties;
import com.nvidia.ess.config.properties.SecretSizeProperties;
import com.nvidia.ess.constants.AuthScope;
import com.nvidia.ess.constants.AuthorizationType;
import com.nvidia.ess.controller.request.CreateSecretRequest;
import com.nvidia.ess.controller.request.CreateSecretRequest.Options;
import com.nvidia.ess.controller.request.SecretQueryType;
import com.nvidia.ess.controller.response.kv2.CreateSecretResponse;
import com.nvidia.ess.controller.response.kv2.SecretResponse;
import com.nvidia.ess.controller.response.kv2.SecretVersionMetadata;
import com.nvidia.ess.controller.retries.RetryConfig;
import com.nvidia.ess.controller.retries.RetryConfig.SecretEndpointsRetryConfig;
import com.nvidia.ess.controller.retries.RetryHandler;
import com.nvidia.ess.exceptions.CustomExceptionHandler;
import com.nvidia.ess.exceptions.InternalErrorException;
import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetriesExhaustedTooManyRequestsException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.facade.SecretFacade;
import com.nvidia.ess.filter.EssAgentIdWebFilter;
import com.nvidia.ess.filter.NamespaceHeaderWebFilter;
import com.nvidia.ess.filter.RequestIdWebFilter;
import com.nvidia.ess.filter.UriPathValidationWebFilter;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.util.FieldUtils;
import com.nvidia.ess.validator.NotBlankAndUriSafeValidationHelper;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotNull;
import java.net.URLEncoder;
import java.nio.charset.Charset;
import java.time.Instant;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.stream.Stream;
import lombok.Builder;
import lombok.Data;
import lombok.SneakyThrows;
import org.apache.commons.lang3.RandomStringUtils;
import org.apache.commons.lang3.StringUtils;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.junit.jupiter.MockitoExtension;
import org.mockito.junit.jupiter.MockitoSettings;
import org.mockito.quality.Strictness;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webclient.autoconfigure.WebClientAutoConfiguration;
import org.springframework.boot.webtestclient.autoconfigure.AutoConfigureWebTestClient;
import org.springframework.boot.webflux.test.autoconfigure.WebFluxTest;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ProblemDetail;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.util.unit.DataSize;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;
import reactor.util.context.ContextView;

@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.STRICT_STUBS)
@ContextConfiguration(classes = {SecretController.class, CustomExceptionHandler.class,
        SecurityConfiguration.class,
        NamespaceHeaderWebFilter.class, UriPathValidationWebFilter.class,
        NotBlankAndUriSafeValidationHelper.class, CustomExceptionHandler.class,
        BeanConfig.class, RequestIdWebFilter.class, EssAgentIdWebFilter.class,
        WebClientAutoConfiguration.class, LoggingProperties.class,
        RetryHandler.class, RetryConfig.class,
        WebConfiguration.class})
@WebFluxTest(value = SecretController.class, properties = {
        "management.tracing.enabled=false",
        "nv-boot.reloadable-properties.enabled=false",
        "ess.logging.errors[0].httpStatusCode=400",
        "ess.logging.errors[0].logLevel=WARN",
        "spring.profiles.active=dummy",
        "spring.application.name=ess-core-test",
        "spring.application.version=0.0.1-test"})
@AutoConfigureWebTestClient(timeout = "60000")
class SecretControllerTest {

    @Autowired
    private WebTestClient webTestClient;

    @MockitoBean
    private CustomMetricsRegistry customMetricsRegistry;

    @MockitoBean
    private SecretFacade secretFacade;

    @MockitoBean
    private AuthChecker authChecker;

    @MockitoBean
    private SecretSizeProperties secretSizeProperties;

    @MockitoSpyBean
    private ObjectMapper objectMapper;

    @MockitoSpyBean
    private RetryConfig retryConfig;

    @MockitoBean(name = TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    @BeforeEach
    void setUp() {
        lenient().when(secretSizeProperties.getMax()).thenReturn(DataSize.ofKilobytes(8));
    }

    private static Stream<Arguments> invalidCreateSecretArguments() {

        var validRequestBody = buildCreateRequest(Map.of("secret", 5), UUID.randomUUID());

        // Empty or null authorization header, empty or null namespace header.
        var emptyOrNullHeadersTestCases = invalidHeadersArguments().map(
                arguments -> Arguments.of(arguments.get()[0], arguments.get()[1],
                        null, null,
                        "entityType1/entityId1/path/secret",
                        validRequestBody)
        );

        // Auth-header with non-printable characters.
        var nonPrintableAuthHeaderCharsTestCases = Stream.of(
                Arguments.of("auth-header-with-lf-\n", "namespace", null, null,
                        "entityType1/entityId1/path/secret", validRequestBody),
                Arguments.of("auth-header-with-cr-\r", "namespace", null, null,
                        "entityType1/entityId1/path/secret", validRequestBody),
                Arguments.of("auth-header-with-null-\0", "namespace", null, null,
                        "entityType1/entityId1/path/secret", validRequestBody)
        );

        // Request-Id / Agent-Id header with non-printable characters.
        var nonPrintableCharsInOtherHeadersTestCases = Stream.of(
                Arguments.of("auth", "namespace", "request-id-header-with-lf-\n", null,
                        "entityType1/entityId1/path/secret", validRequestBody),
                Arguments.of("auth", "namespace", null, "agent-id-header-with-null-\0",
                        "entityType1/entityId1/path/secret", validRequestBody)
        );

        // Invalid namespace in header (examples containing URI-unsafe characters, including cases
        // of non-printable characters).
        var invalidNamespaceHeaderTestCases = invalidNamespaceOrEntityTypeOrEntityNameArguments()
                .flatMap(invalidNsName -> Stream.of(null, "request-id", "request-id-nonprintable-\n")
                                .flatMap(reqId -> Stream.of(null, "agent-id", "agent-id-nonprintable-\n")
                                        .map(agentId  ->
                                                Arguments.of("auth", invalidNsName, reqId, agentId,
                                                        "entityType1/entityId1/path/secret", validRequestBody)
                                        )
                                )
                );

        // Invalid entity-type test-cases:
        var invalidEntityTypeTestCases = invalidNamespaceOrEntityTypeOrEntityNameArguments()
                .filter(invalidEntityTypeName -> !StringUtils.isBlank(invalidEntityTypeName))
                .map(invalidEntityTypeName ->
                        Arguments.of("auth", "namespace", null, null,
                                URLEncoder.encode(invalidEntityTypeName, Charset.defaultCharset()) +
                                "/entityId1/path/secret", validRequestBody)
                );

        // Invalid entity-ID test-cases:
        var invalidEntityIdTestCases = invalidNamespaceOrEntityTypeOrEntityNameArguments()
                .filter(invalidEntityId -> !StringUtils.isBlank(invalidEntityId))
                .map(invalidEntityId ->
                        Arguments.of("auth", "namespace", null, null,
                                "entityType1/" + URLEncoder.encode(invalidEntityId, Charset.defaultCharset()) +
                                "/path/secret", validRequestBody)
                );

        // Invalid secret-path test-cases:
        var invalidSecretPathTestCases = invalidSecretPathArguments()
                .map(invalidSecretPath -> Arguments.of("auth", "namespace", null, null,
                        "entityType1/entityId1/" + invalidSecretPath, validRequestBody));

        // Invalid request body.
        var invalidRequestBodyTestCases = Stream.of(
            // `data` field not set.
            Arguments.of("auth header", "namespace", null, null,
                    "entityType1/entityId1/path/secret",
                    buildCreateRequest(null, UUID.randomUUID())),
            // `options` field set but is empty.
            Arguments.of("auth header", "namespace", null, null,
                    "entityType1/entityId1/path/secret",
                    buildCreateRequest(Map.of("secret", 5), UUID.randomUUID())
                            .toBuilder()
                            .options(Options.builder().build())
                            .build())
            // Note: cannot add UUID.randomUUID(), error will happen only in Integration Tests
        );

        var res = Stream.concat(emptyOrNullHeadersTestCases, nonPrintableAuthHeaderCharsTestCases);
        res = Stream.concat(res, invalidNamespaceHeaderTestCases);
        res = Stream.concat(res, nonPrintableCharsInOtherHeadersTestCases);
        res = Stream.concat(res, invalidEntityTypeTestCases);
        res = Stream.concat(res, invalidEntityIdTestCases);
        res = Stream.concat(res, invalidSecretPathTestCases);
        res = Stream.concat(res, invalidRequestBodyTestCases);

        return res;
    }

    private static Stream<Arguments> validSecretControllerURIs() {
        // Stream(Tuple<validURI, entityType, entityId, secretPath>)
        return Stream.of(
                Arguments.of("/v1/" + TEST_ENTITY_TYPE + "/" + TEST_ENTITY_ID + "/" + TEST_SECRET_PATH,
                        TEST_ENTITY_TYPE,
                        TEST_ENTITY_ID,
                        TEST_SECRET_PATH),
                Arguments.of("/v1/functions/123/functions/123/functions/123", "functions", "123",
                        "functions/123/functions/123"),
                Arguments.of("/v1/(funct*ions)/(123,456.789)/" +
                        "(funct*ions)/(123,456.789)/(funct*ions)/(123,456.789)/'dir1'/dir2/dir3",
                        "(funct*ions)",
                        "(123,456.789)",
                        "(funct*ions)/(123,456.789)/(funct*ions)/(123,456.789)/'dir1'/dir2/dir3")
        );
    }


    private static Stream<Arguments> validSecretGetParams() {
        return Stream.of(Uuids.timeBased().toString(), null, "")
                .flatMap(cas ->
                        validSecretControllerURIs()
                                .map(args -> Arguments.of(args.get()[0], args.get()[1],
                                        args.get()[2], args.get()[3],
                                        cas)));
    }

    static CreateSecretRequest buildCreateRequest(Map<String, Object> data, UUID cas) {
        var builder = CreateSecretRequest.builder()
                .data(data);
        if (cas != null) {
            builder.options(Options.builder()
                    .cas(cas)
                    .build());
        }
        return builder.build();
    }

    @ParameterizedTest
    @MethodSource("invalidCreateSecretArguments")
    void createSecret_onInvalidInput_400(String authorization, String namespace, String requestId, String agentId,
            String uri, CreateSecretRequest request) {

        var invalidUri = invalidNamespaceOrEntityTypeOrEntityNameArguments()
                        .anyMatch(s -> !StringUtils.isBlank(s) &&
                                        uri.contains(URLEncoder.encode(s, Charset.defaultCharset()))
                        ) ||
                invalidSecretPathArguments().anyMatch(s -> !StringUtils.isBlank(s) && uri.contains(s));

        var requestBuilder = webTestClient.put()
                .uri(buildUrl("/v1/" + uri))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace);

        if (!Objects.isNull(requestId)) {
            requestBuilder = requestBuilder.header(X_ESS_REQUEST_ID_HEADER, requestId);
        }
        if (!Objects.isNull(agentId)) {
            requestBuilder = requestBuilder.header(X_ESS_AGENT_ID_HEADER, agentId);
        }

        requestBuilder
                .bodyValue(request)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectBody(ProblemDetail.class)
                .returnResult();

        if (invalidUri) {
            verify(telemetryComponents, never()).setSpanAttribute(
                    any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), anyString());
        } else {

            if (FieldUtils.containsOnlyPrintableChars(namespace)) {

                verify(telemetryComponents).setSpanAttribute(
                        any(ServerWebExchange.class), eq(NAMESPACE_KEY),
                        eq(StringUtils.isBlank(namespace) ? "UNKNOWN" : namespace));

                // RequestIdWebFilter is currently skipped when `NamespaceHeaderWebFilter` execution is aborted
                // due to an illegal `X-ESS-Namespace:` header-value. This may need to be fixed.
                if (FieldUtils.containsOnlyPrintableChars(requestId)) {
                    if (!Objects.isNull(requestId)) {
                        verify(telemetryComponents).setSpanAttribute(
                                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), eq(requestId));
                    } else {
                        verify(telemetryComponents).setSpanAttribute(
                                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
                    }

                    // EssAgentIdWebFilter is currently skipped when `RequestIdWebFilter` execution is skipped or
                    // aborted. This may need to be fixed.
                    if (!Objects.isNull(agentId) && FieldUtils.containsOnlyPrintableChars(agentId)) {
                        verify(telemetryComponents).setSpanAttribute(
                                any(ServerWebExchange.class), eq(AGENT_ID_KEY), eq(agentId));
                    }
                }
            }
            verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                    any(ServerWebExchange.class), any(Throwable.class));
        }
    }

    @Data
    @Builder
    private static class MalformedCreateSecretRequest {
        // if specified, must be valid
        @Valid
        private Options options;

        @NotNull
        private Map<String, Object> data;

        @Data
        @Builder
        public static class Options {
            private String cas;
        }
    }

    private static Stream<Arguments> invalidCasArguments() {
        return Stream.of(
                Arguments.of(""),
                Arguments.of("randomString")
        );
    }

    @ParameterizedTest
    @MethodSource("invalidCasArguments")
    void createSecret_onInvalidCas_400(String cas) {
        var request = MalformedCreateSecretRequest.builder()
                .data(Map.of("key", "value"))
                .options(MalformedCreateSecretRequest.Options.builder()
                        .cas(cas)
                        .build()
                )
                .build();
        webTestClient.put()
                .uri(buildUrl("/v1/" + TEST_ENTITY_TYPE + "/" + TEST_ENTITY_ID + "/" + TEST_SECRET_PATH))
                .header(HttpHeaders.AUTHORIZATION, "some authorization")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .bodyValue(request)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                any(ServerWebExchange.class), any(Throwable.class));
    }

    // Regression guard for FAIL_ON_UNKNOWN_PROPERTIES: BeanConfig enables it, so an unknown /
    // misspelled top-level request-body property must be rejected, not silently ignored. Here the
    // body wraps the CAS under "option" (the singular misspelling of the model's "options"), which
    // must produce a 400 rather than being dropped (which would create the secret and return 200).
    @Test
    void createSecret_onUnknownBodyProperty_400() {
        var body = Map.of(
                "option", Map.of("cas", UUID.randomUUID().toString()),
                "data", Map.of("field1", "1234", "field2", "4567"));
        webTestClient.put()
                .uri(buildUrl("/v1/" + TEST_ENTITY_TYPE + "/" + TEST_ENTITY_ID + "/" + TEST_SECRET_PATH))
                .header(HttpHeaders.AUTHORIZATION, "some authorization")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .bodyValue(body)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                any(ServerWebExchange.class), any(Throwable.class));
    }

    private static Stream<Arguments> createSecretPayloadArguments() {
        return Stream.of(
                Arguments.of(DataSize.ofKilobytes(8), Map.of(
                        "key1", RandomStringUtils.secure().nextAlphabetic(2000),
                        "key2", RandomStringUtils.secure().nextAlphabetic(2000),
                        "key3", RandomStringUtils.secure().nextAlphabetic(2000),
                        "key4", RandomStringUtils.secure().nextAlphabetic(2000),
                        "key5", RandomStringUtils.secure().nextAlphabetic(2000)

                ), HttpStatus.BAD_REQUEST),

                Arguments.of(DataSize.ofKilobytes(8), Map.of(
                        "key1", RandomStringUtils.secure().nextAlphabetic(9000)
                ), HttpStatus.BAD_REQUEST),

                // put less than 8196 since other parts of the payload might occupy some space
                Arguments.of(DataSize.ofKilobytes(8), Map.of(
                        "key1", RandomStringUtils.secure().nextAlphabetic(2000),
                        "key2", RandomStringUtils.secure().nextAlphabetic(2000),
                        "key3", RandomStringUtils.secure().nextAlphabetic(2000),
                        "key4", RandomStringUtils.secure().nextAlphabetic(1800)

                ), HttpStatus.OK),

                Arguments.of(DataSize.ofKilobytes(8), Map.of(
                        "key1", RandomStringUtils.secure().nextAlphabetic(7800)
                ), HttpStatus.OK),

                Arguments.of(DataSize.ofKilobytes(16), Map.of(
                        "key1", RandomStringUtils.secure().nextAlphabetic(15000)
                ), HttpStatus.OK),
                Arguments.of(DataSize.ofKilobytes(16), Map.of(
                        "key1", RandomStringUtils.secure().nextAlphabetic(18000)
                ), HttpStatus.BAD_REQUEST),

                Arguments.of(DataSize.ofKilobytes(64), Map.of(
                        "key1", RandomStringUtils.secure().nextAlphabetic(63000)
                ), HttpStatus.OK),
                Arguments.of(DataSize.ofKilobytes(64), Map.of(
                        "key1", RandomStringUtils.secure().nextAlphabetic(66000)
                ), HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("createSecretPayloadArguments")
    void createSecret_onPayloadSizes_returnExpectedStatusCode(DataSize maxSize, Map<String, Object> data, HttpStatus expectedStatus) {
        when(secretSizeProperties.getMax())
                .thenReturn(maxSize);
        var request = CreateSecretRequest.builder()
                .data(data)
                .build();
        var authHeader = "some auth";
        if (expectedStatus == HttpStatus.OK) {
            when(authChecker.authTenant(TEST_NAMESPACE, authHeader, new String[]{
                    AuthScope.ESS_SECRETS_ADMIN})).thenReturn(Mono.just(true));

            when(secretFacade.createSecret(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID, TEST_SECRET_PATH, request))
                    .thenReturn(Mono.just(mock(CreateSecretResponse.class)));
        }


        var expectHeader = webTestClient.put()
                .uri(buildUrl("/v1/" + TEST_ENTITY_TYPE + "/" + TEST_ENTITY_ID + "/" + TEST_SECRET_PATH))
                .header(HttpHeaders.AUTHORIZATION, authHeader)
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .bodyValue(request)
                .exchange()
                .expectStatus()
                .isEqualTo(expectedStatus)
                .expectHeader();

        if (expectedStatus == HttpStatus.OK) {
            expectHeader
                    .contentType(MediaType.APPLICATION_JSON)
                    .expectBody(CreateSecretResponse.class)
                    .returnResult();

            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(TEST_ENTITY_TYPE));

        } else {
            expectHeader.contentType(MediaType.APPLICATION_PROBLEM_JSON)
                    .expectBody(ProblemDetail.class)
                    .returnResult();

            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(TEST_ENTITY_TYPE));
            verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                    any(ServerWebExchange.class), any(Throwable.class));
        }

    }

    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @SneakyThrows
    @Test
    void createSecret_onObjectMapperFailure_500() {
        var request = buildCreateRequest(Map.of("key", "value"), null);

        when(objectMapper.writeValueAsBytes(request))
                .thenThrow(new JacksonException("failed"){});

        webTestClient.put()
                .uri(buildUrl("/v1/" + TEST_ENTITY_TYPE + "/" + TEST_ENTITY_ID + "/" + TEST_SECRET_PATH))
                .header(HttpHeaders.AUTHORIZATION, "some auth")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .bodyValue(request)
                .exchange()
                .expectStatus()
                .is5xxServerError()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(TEST_ENTITY_TYPE));
        verify(telemetryComponents).recordException(
                any(ServerWebExchange.class), any(Throwable.class));
    }

    private static Stream<Arguments> validSecretControllerURIsAndRetryTestArgs() {
        return validSecretControllerURIs().flatMap(args ->
                Stream.of(false, true).flatMap(nonRetryableError ->
                        Stream.of(false, true).filter(retryableErrorTooManyRequests ->
                                !nonRetryableError || !retryableErrorTooManyRequests
                        )
                        .flatMap(retryableErrorTooManyRequests -> Stream.of(0, 1, 2, 3, 4)
                                .flatMap(numRetries -> Stream.of(0, 1, 2)
                                                .map(numFailedAttempts ->
                                                        Arguments.of(args.get()[0], args.get()[1], args.get()[2],
                                                                args.get()[3], numRetries, numFailedAttempts,
                                                                nonRetryableError, retryableErrorTooManyRequests)
                                                )
                                )
                        )
                )
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @MethodSource("validSecretControllerURIsAndRetryTestArgs")
    @SuppressWarnings("java:S5961") // number of assertions
    void createSecret_validInput_200IfRetryCountSufficient(String uri, String entityType, String entityId,
            String secretPath, Integer testRetryLimit, Integer testNumFailedAttempts,
            Boolean nonRetryableError, Boolean retryableErrorTooManyRequests) {
        var request = buildCreateRequest(Map.of("key", "value"), null);

        var payloadJson = "{\"key\":\"value\"}".getBytes();
        doReturn(payloadJson).when(objectMapper).writeValueAsBytes(request);

        doReturn(Mono.just(true))
                .when(authChecker)
                .authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_ADMIN}));

        var response = CreateSecretResponse.builder()
                .data(SecretVersionMetadata.builder()
                        .createdTime(Instant.now())
                        .version(UUID.randomUUID())
                        .build()
                )
                .build();

        final var numAttemptsSoFar = new AtomicInteger();

        var secretCreationOp = Mono.defer(() -> {
            if (numAttemptsSoFar.getAndIncrement() < testNumFailedAttempts) {
                return nonRetryableError
                        ? Mono.error(new RuntimeException("test"))
                        : Mono.error(new RetryableException(
                                retryableErrorTooManyRequests
                                        ? new RetriesExhaustedTooManyRequestsException(TEST_PROBLEM_SUMMARY, "test")
                                        : new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test")));
            }
            return Mono.just(response);
        });
        
        doReturn(secretCreationOp)
                .when(secretFacade)
                .createSecret(TEST_NAMESPACE, entityType, entityId, secretPath, request);

        var secretControllerRetryConfig = mock(SecretEndpointsRetryConfig.class);
        doReturn(testRetryLimit).when(secretControllerRetryConfig).getCreateSecretRetryCount();
        doReturn(1L).when(secretControllerRetryConfig).getMinBackoffBetweenRetriesMillis();
        doReturn(1L).when(secretControllerRetryConfig).getMaxBackoffBetweenRetriesMillis();
        doReturn(secretControllerRetryConfig).when(retryConfig).getSecretEndpointsRetryConfig();

        if (testNumFailedAttempts == 0 || (!nonRetryableError && testRetryLimit >= testNumFailedAttempts)) {
            doNothing().when(customMetricsRegistry).recordSecretCreate(TEST_NAMESPACE, true);
            doNothing().when(customMetricsRegistry).recordSecretPayloadSize(TEST_NAMESPACE, payloadJson.length);

            webTestClient.put()
                    .uri(buildUrl(uri))
                    .header(HttpHeaders.AUTHORIZATION, "some auth")
                    .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                    .header(X_ESS_AGENT_ID_HEADER, TEST_ESS_AGENT_ID)
                    .bodyValue(request)
                    .exchange()
                    .expectStatus()
                    .isOk()
                    .expectHeader()
                    .contentType(MediaType.APPLICATION_JSON)
                    .expectBody(CreateSecretResponse.class)
                    .returnResult()
                    .getResponseBody()
                    .equals(response);

            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(AGENT_ID_KEY), eq(TEST_ESS_AGENT_ID));
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
            verify(telemetryComponents, times(testNumFailedAttempts)).setSpanStatusOk(
                    any(ContextView.class));
            verify(telemetryComponents, times(testNumFailedAttempts)).recordException(
                    any(ContextView.class), any(Throwable.class));
            for (long i = 0; i < testNumFailedAttempts; ++i) {
                verify(telemetryComponents).setSpanAttribute(
                        any(ContextView.class), eq(RETRY_NUM_KEY), eq(i + 1));
            }
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(REQUEST_ID_KEY), anyString());
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MAX_RETRY_ATTEMPTS_KEY), anyLong());
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MIN_RETRY_BACKOFF_TIME_MILLIS_KEY), anyLong());
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MAX_RETRY_BACKOFF_TIME_MILLIS_KEY), anyLong());
        
        } else {

            var testRequestClientBuilder = webTestClient.put()
                    .uri(buildUrl(uri))
                    .header(HttpHeaders.AUTHORIZATION, "some auth")
                    .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                    .header(X_ESS_AGENT_ID_HEADER, TEST_ESS_AGENT_ID)
                    .bodyValue(request)
                    .exchange()
                    .expectStatus();
            
            (retryableErrorTooManyRequests
                    ? testRequestClientBuilder.isEqualTo(HttpStatus.TOO_MANY_REQUESTS)
                    : testRequestClientBuilder.is5xxServerError()
            )
                    .expectHeader()
                    .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                    .expectBody(ProblemDetail.class)
                    .returnResult();

            var expectedNumRetries = Math.min(testRetryLimit, testNumFailedAttempts);

            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(AGENT_ID_KEY), eq(TEST_ESS_AGENT_ID));
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
            verify(telemetryComponents, times(nonRetryableError ? 0 : expectedNumRetries))
                    .setSpanStatusOk(any(ContextView.class));
            verify(telemetryComponents, times(nonRetryableError ? 0 : expectedNumRetries))
                    .recordException(any(ContextView.class), any(Throwable.class));
            if (retryableErrorTooManyRequests) {
                verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                        any(ServerWebExchange.class), any(Throwable.class));
            } else {
                verify(telemetryComponents).recordException(
                        any(ServerWebExchange.class), any(Throwable.class));
            }
            if (!nonRetryableError) {
                for (long i = 0; i < expectedNumRetries; ++i) {
                    verify(telemetryComponents).setSpanAttribute(
                            any(ContextView.class), eq(RETRY_NUM_KEY), eq(i + 1));
                }
                verify(telemetryComponents).setSpanAttribute(
                        any(ServerWebExchange.class), eq(EXHAUSTED_RETRIES_KEY), eq(true));
            }
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(REQUEST_ID_KEY), anyString());
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MAX_RETRY_ATTEMPTS_KEY), anyLong());
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MIN_RETRY_BACKOFF_TIME_MILLIS_KEY), anyLong());
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MAX_RETRY_BACKOFF_TIME_MILLIS_KEY), anyLong());
        }

        if (nonRetryableError && testNumFailedAttempts > 0) {
            Assertions.assertEquals(1, numAttemptsSoFar.get());
        } else {
            Assertions.assertEquals(Math.min(testRetryLimit, testNumFailedAttempts) + 1, numAttemptsSoFar.get());
        }
    }

    private static Stream<Arguments> invalidDeleteSecretArguments() {
        return invalidHeadersArguments().map(
                arguments -> Arguments.of(arguments.get()[0], arguments.get()[1],
                        "entityType1/entityId1/path/secret"));
    }


    @ParameterizedTest
    @MethodSource("invalidDeleteSecretArguments")
    void deleteSecret_onInvalidInput_400(String authorization, String namespace,
            String secretPath) {
        webTestClient.delete()
                .uri(buildUrl("/v1/" + secretPath))
                .header(HttpHeaders.AUTHORIZATION, authorization)
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isBadRequest()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY),
                eq(StringUtils.isBlank(namespace) ? "UNKNOWN" : namespace));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                any(ServerWebExchange.class), any(Throwable.class));
    }

    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @Test
    void deleteSecret_onAuthFailure_403() {
        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.error(() -> new ForbiddenException("auth failure")));
        webTestClient.delete()
                .uri(buildUrl("/v1/" + DELETION_TEST_SECRET_PATH))
                .header(HttpHeaders.AUTHORIZATION, "some auth")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isForbidden()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq("a"));
        verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                any(ServerWebExchange.class), any(Throwable.class));
    }

    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @Test
    void deleteSecret_onSuccess_204() {
        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.just(true));

        when(secretFacade.deleteSecret(eq(TEST_NAMESPACE), any(), any(), any()))
                .thenReturn(Mono.empty());
        webTestClient.delete()
                .uri(buildUrl("/v1/" + DELETION_TEST_SECRET_PATH))
                .header(HttpHeaders.AUTHORIZATION, "some auth")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isNoContent();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq("a"));
    }

    private static Stream<Arguments> invalidGetSecretArguments() {
        var validRequestAndAgentId = Stream.concat(
                Stream.concat(
                        invalidHeadersArguments().map(
                                arguments -> Arguments.of(arguments.get()[0], arguments.get()[0],
                                        arguments.get()[1],
                                        "entityType1/entityId1/path/secret", null, null,
                                        (arguments.get()[0] == null || (arguments.get()[0] instanceof String s &&
                                                        StringUtils.isBlank(s)))
                                                ? HttpStatus.FORBIDDEN : HttpStatus.BAD_REQUEST)
                        ),
                        Stream.of(
                                Arguments.of("auth-header-with-nonprintable-char-\n", null, "namespace",
                                        "entityType1/entityId1/path/secret", null, null, HttpStatus.BAD_REQUEST),
                                Arguments.of(null, "notary-header-with-nonprintable-char-\r", "namespace",
                                        "entityType1/entityId1/path/secret", null, null, HttpStatus.BAD_REQUEST),
                                Arguments.of("Bearer authTokenString", "notaryTokenString",
                                        "namespace-with-nonprintable-char-\0", "entityType1/entityId1/path/secret",
                                        null, null, HttpStatus.BAD_REQUEST)
                        )
                ),
                Stream.of(
                        // invalid queryType
                        Arguments.of("valid bearer token", null, "namespace",
                                "entityType1/entityId1/path/secret", null, "invalid_query_type",
                                HttpStatus.BAD_REQUEST),
                        // [1]: can only list secret paths at root of entity:
                        //
                        // [1.1]: Simulate FETCH_SECRET call on URI: `/v1/{entityType}/{entityId}/`
                        //      (no secret-path specified) with valid tokens of both types (Non-Notary and
                        //      Notary) specified in the request. Expect BAD_REQUEST response with
                        //      OAUTH auth-type recorded in telemetry.
                        Arguments.of("valid bearer token", "valid notary token", "namespace",
                                "entityType1/entityId1/", null, SecretQueryType.FETCH_SECRET.name(),
                                HttpStatus.BAD_REQUEST),
                        // [1.2]: Simulate FETCH_SECRET call on URI: `/v1/{entityType}/{entityId}/`
                        //      (no secret-path specified) with valid Non-Notary auth-token. Expect
                        //      BAD_REQUEST response with auth-type OAUTH recorded in telemetry.
                        Arguments.of("valid bearer token", null, "namespace",
                                "entityType1/entityId1/", null, SecretQueryType.FETCH_SECRET.name(),
                                HttpStatus.BAD_REQUEST),
                        // [1.3]: Simulate FETCH_SECRET call on URI: `/v1/{entityType}/{entityId}/`
                        //      (no secret-path specified) with valid notary token. Expect BAD_REQUEST
                        //      response but auth-type should NOT be recorded in telemetry (the controller
                        //      maps the request to the list-secrets-at-root endpoint which has a requirement
                        //      for a valid non-empty `Authorization:` header to be present).
                        Arguments.of(null, "valid notary token", "namespace",
                                "entityType1/entityId1/", null, SecretQueryType.FETCH_SECRET.name(),
                                HttpStatus.BAD_REQUEST),
                        // [1.4]: Simulate LIST_VERSIONS call on URI: `/v1/{entityType}/{entityId}/`
                        //      (no secret-path specified) with valid tokens of both types. Expect BAD_REQUEST
                        //      response with NO auth-type recorded in telemetry (notary tokens can only
                        //      be used in FETCH_SECRET API calls and will therefore be ignored in LIST_VERSIONS
                        //      calls if specified).
                        Arguments.of("valid bearer token", "valid notary token", "namespace",
                                "entityType1/entityId1/", null, SecretQueryType.LIST_VERSIONS.name(),
                                HttpStatus.BAD_REQUEST),
                        // [2]: notary only allowed to fetch single secret
                        Arguments.of(null, "valid notary token", "namespace",
                                "entityType1/entityId1/path/secret", null, SecretQueryType.LIST_VERSIONS.name(),
                                HttpStatus.FORBIDDEN),
                        Arguments.of(null, "valid notary token", "namespace",
                                "entityType1/entityId1/path/secret", null, SecretQueryType.LIST_SECRETS.name(),
                                HttpStatus.FORBIDDEN),
                        // [3]: Invalid `version` query-param in a FETCH_SECRET API call.
                        Arguments.of("valid bearer token", null, "namespace",
                                "entityType1/entityId1/path/secret", "randomString", SecretQueryType.FETCH_SECRET.name(),
                                HttpStatus.BAD_REQUEST)
                        // Note: cannot add UUID.randomUUID().toString(), error will happen only in Integration Tests
                )
        )
                .flatMap(args ->
                        Stream.of(null, "request-id")
                                .flatMap(reqId -> Stream.of(null, "agent-id")
                                                .map(agentId -> Arguments.of(
                                                        args.get()[0], // Authorization:
                                                        args.get()[1], // X-ESS-Token:
                                                        args.get()[2], // X-ESS-Namespace:
                                                        reqId,         // X-ESS-RequestId:
                                                        agentId,       // X-ESS-AgentId:
                                                        args.get()[3], // URI
                                                        args.get()[4], // version
                                                        args.get()[5], // query-type
                                                        args.get()[6]  // expected status-code
                                                ))
                                )
                );

        var invalidRequestOrAgentId = Stream.of(null, "valid-token", "nonprintable-token-\n")
                .flatMap(bearerToken -> Stream.of(null, "valid-token", "nonprintable-token-\n")
                        .flatMap(xEssToken -> Stream.of(null, "valid-ns", "nonprintable-ns-\n")
                                .flatMap(namespace -> Stream.of(
                                        // Bad request-ID , agent ID unspecified.
                                        Arguments.of(bearerToken, xEssToken, namespace, "nonprintable-req-id-\n",
                                                null, "entityType1/entityId1/path/secret", null, null,
                                                HttpStatus.BAD_REQUEST),
                                        // Bad request-ID , Valid agent ID.
                                        Arguments.of(bearerToken, xEssToken, namespace, "nonprintable-req-id-\n",
                                                "valid-agent-id", "entityType1/entityId1/path/secret", null, null,
                                                HttpStatus.BAD_REQUEST),
                                        // Bad request-ID , bad agent ID.
                                        Arguments.of(bearerToken, xEssToken, namespace, "nonprintable-req-id-\n",
                                                "nonprintable-agent-id-\n", "entityType1/entityId1/path/secret",
                                                null, null, HttpStatus.BAD_REQUEST),
                                        // Request-ID unspecified, Bad agent ID.
                                        Arguments.of(bearerToken, xEssToken, namespace, null,
                                                "nonprintable-agent-id-\n", "entityType1/entityId1/path/secret", null,
                                                null, HttpStatus.BAD_REQUEST),
                                        // Valid request-ID , bad agent ID.
                                        Arguments.of(bearerToken, xEssToken, namespace, "valid-request-id",
                                                "nonprintable-agent-id-\n", "entityType1/entityId1/path/secret", null,
                                                null, HttpStatus.BAD_REQUEST)
                                ))
                        )
                );

        return Stream.concat(validRequestAndAgentId, invalidRequestOrAgentId);
    }



    @ParameterizedTest
    @MethodSource("invalidGetSecretArguments")
    void getSecret_onInvalidInput_errorStatusCode(String bearerToken, String xEssToken, String namespace,
            String requestId, String agentId, String uri, String version, String queryType,
            HttpStatus httpStatus) {

        var versionIsNullOrValidTimeUUID = true;
        if (!Objects.isNull(version)) {
            try {
                versionIsNullOrValidTimeUUID = UUID.fromString(version).version() == 1;
            } catch (IllegalArgumentException ex) {
                versionIsNullOrValidTimeUUID = false;
            }
        }

        var queryTypeIsNullOrValid = true;
        if (!Objects.isNull(queryType)) {
            try {
                SecretQueryType.valueOf(queryType.toUpperCase());
            } catch (IllegalArgumentException ex) {
                queryTypeIsNullOrValid = false;
            }
        }

        // Blank and invalid (URI-unsafe) namespace headers are detected ahead of user-defined controller-body
        // execution and therefore, the controller body is not executed when namespace headers are blank
        // or URI-unsafe.
        //
        // Likewise, any specified query-params are validated ahead of execution of the controller-body. Invalid
        // query-params preempt execution of the controller-body as well.
        //
        // When the namespace header (and any query-params) is specified and valid however, user-defined validation
        // logic in the controller body is applied on the auth & notary tokens, as well as the
        // /{entityType}/{entityId}/{secretPath**} URI ahead of the actual auth and business-logic execution. This
        // validation logic includes setting of some OTEL attributes.
        //
        // NOTE: Inclusion of non-printable characters in any header causes Spring to reject the request before
        // any web-filters, user-defined validation, auth or business-logic is executed.
        //
        var headersHaveNonPrintableChars = !FieldUtils.containsOnlyPrintableChars(namespace) ||
                !FieldUtils.containsOnlyPrintableChars(bearerToken) ||
                !FieldUtils.containsOnlyPrintableChars(xEssToken) ||
                !FieldUtils.containsOnlyPrintableChars(requestId) ||
                !FieldUtils.containsOnlyPrintableChars(agentId);

        // If the URI is `/v1/{entityType}/{entityId}` then the API call is routed to the list-secret-paths-at-root
        // controller method, which requires a valid non-empty `Authorization:` header. The absence of this
        // header (irrespective of whether `X-ESS-Token:` is provided) will result in the controller body
        // not being executed and a 400 status-code returned.
        var secretPathIsRoot = StringUtils.strip(uri, "/").split("/+").length == 2;
        var bearerTokenNotProvided = StringUtils.isBlank(bearerToken);
        var rootSecretPathCallFailure = secretPathIsRoot && bearerTokenNotProvided;

        var controllerBodyExecuted = !headersHaveNonPrintableChars && !StringUtils.isBlank(namespace) &&
                versionIsNullOrValidTimeUUID && queryTypeIsNullOrValid && !rootSecretPathCallFailure;

        var requestBuilder = webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path(buildUrl("/v1/" + uri))
                        .queryParam("version", version)
                        .queryParam("query_type", queryType)
                        .build())
                .header(HttpHeaders.AUTHORIZATION, bearerToken)
                .header(X_ESS_TOKEN_HEADER, xEssToken);

        if (!Objects.isNull(requestId)) {
            requestBuilder = requestBuilder.header(X_ESS_REQUEST_ID_HEADER, requestId);
        }
        if (!Objects.isNull(agentId)) {
            requestBuilder = requestBuilder.header(X_ESS_AGENT_ID_HEADER, agentId);
        }

        requestBuilder
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .exchange()
                .expectStatus()
                .isEqualTo(httpStatus)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        if (FieldUtils.containsOnlyPrintableChars(namespace)) {
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(NAMESPACE_KEY),
                    eq(StringUtils.isBlank(namespace) ? "UNKNOWN" : namespace));
            // RequestIdWebFilter is currently skipped when `NamespaceHeaderWebFilter` execution is aborted
            // due to an illegal `X-ESS-Namespace:` header-value. This may need to be fixed.
            if (FieldUtils.containsOnlyPrintableChars(requestId)) {
                if (!Objects.isNull(requestId)) {
                    verify(telemetryComponents).setSpanAttribute(
                            any(ServerWebExchange.class), eq(REQUEST_ID_KEY), eq(requestId));
                } else {
                    verify(telemetryComponents).setSpanAttribute(
                            any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
                }

                // EssAgentIdWebFilter is currently skipped when `RequestIdWebFilter` execution is skipped or
                // aborted. This may need to be fixed.
                if (!Objects.isNull(agentId) && FieldUtils.containsOnlyPrintableChars(agentId)) {
                    verify(telemetryComponents).setSpanAttribute(
                            any(ServerWebExchange.class), eq(AGENT_ID_KEY), eq(agentId));
                }
            }
        }

        if (controllerBodyExecuted) {
            var entityType = uri.split("/+")[0];
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
            verify(telemetryComponents).setSpanAttribute(
                    any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                    eq(!StringUtils.isBlank(queryType)
                            ? queryType.toUpperCase()
                            : SecretQueryType.DEFAULT_VALUE));

            if (Objects.isNull(queryType) ||
                    SecretQueryType.valueOf(queryType.toUpperCase()) == SecretQueryType.FETCH_SECRET) {
                if (!StringUtils.isBlank(bearerToken)) {
                    verify(telemetryComponents).setSpanAttribute(
                            any(ServerWebExchange.class), eq(SECRET_READ_AUTH_TYPE_KEY),
                            eq(AuthorizationType.OAUTH.name()));
                    verify(customMetricsRegistry)
                            .recordSecretRead(namespace, AuthorizationType.OAUTH, false);
                } else if (!StringUtils.isBlank(xEssToken)) {
                    verify(telemetryComponents).setSpanAttribute(
                            any(ServerWebExchange.class), eq(SECRET_READ_AUTH_TYPE_KEY),
                            eq(AuthorizationType.NOTARY.name()));
                    verify(customMetricsRegistry)
                            .recordSecretRead(namespace, AuthorizationType.NOTARY, false);
                }
            }
        }

        if (httpStatus.is4xxClientError()) {
            verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                    any(ServerWebExchange.class), any(Throwable.class));
        } else {
            verify(telemetryComponents).recordException(
                    any(ServerWebExchange.class), any(Throwable.class));
        }
    }


    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void getSecret_onFailedAuth_403(boolean isNonNotaryAuth) {

        if (isNonNotaryAuth) {
            when(authChecker.authTenant(eq(TEST_NAMESPACE), any(),
                    eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})
            ))
                    .thenReturn(Mono.error(() -> new ForbiddenException("auth failure")));
        } else {
            when(authChecker.authNotaryClient(eq(TEST_NAMESPACE), anyString(), eq(TEST_SECRET_PATH)))
                    .thenReturn(Mono.error(() -> new ForbiddenException("auth failure")));
        }

        webTestClient.get()
                .uri(buildUrl("/v1/" + TEST_SECRET_PATH))
                .header(isNonNotaryAuth ? HttpHeaders.AUTHORIZATION : X_ESS_TOKEN_HEADER, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isForbidden()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        var expectedAuthType = isNonNotaryAuth ? AuthorizationType.OAUTH : AuthorizationType.NOTARY;
        var entityType = TEST_SECRET_PATH.split("/+")[0];
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.DEFAULT_VALUE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_READ_AUTH_TYPE_KEY),
                eq(expectedAuthType.name()));
        verify(customMetricsRegistry)
                .recordSecretRead(TEST_NAMESPACE, expectedAuthType, false);
        verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                any(ServerWebExchange.class), any(Throwable.class));
    }


    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @ParameterizedTest
    @MethodSource("validSecretGetParams")
    void getSecret_onNonNotaryAuth_200(String uri, String entityType, String entityId, String secretPath, String version) {

        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.just(true));

        when(secretFacade.getSecret(eq(TEST_NAMESPACE), eq(entityType), eq(entityId), eq(secretPath), any()))
                .thenReturn(Mono.just(mock(SecretResponse.class)));
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path(buildUrl(uri))
                        .queryParam("version", version)
                        .build())
                .header(HttpHeaders.AUTHORIZATION, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(SecretResponse.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.DEFAULT_VALUE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_READ_AUTH_TYPE_KEY),
                eq(AuthorizationType.OAUTH.name()));
    }

    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @Test
    void getSecret_onNonNotaryAuthAndFetchFailure_500() {

        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.just(true));

        when(secretFacade.getSecret(eq(TEST_NAMESPACE), any(), any(), any(), any()))
                .thenReturn(Mono.error(() -> new InternalErrorException("error")));
        webTestClient.get()
                .uri(buildUrl("/v1/" + TEST_SECRET_PATH))
                .header(HttpHeaders.AUTHORIZATION, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .is5xxServerError()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        var entityType = TEST_SECRET_PATH.split("/+")[0];
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.DEFAULT_VALUE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_READ_AUTH_TYPE_KEY),
                eq(AuthorizationType.OAUTH.name()));
        verify(telemetryComponents).recordException(
                any(ServerWebExchange.class), any(Throwable.class));
    }

    @Test
    void listSecretPathsOnRoot_onNonNotaryAuth_200() {

        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.just(true));

        when(secretFacade.getSecretPaths(TEST_NAMESPACE, TEST_ENTITY_TYPE, TEST_ENTITY_ID, ""))
                .thenReturn(Mono.just(mock(SecretResponse.class)));
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path(buildUrl("/v1/" + TEST_ENTITY_TYPE + "/" + TEST_ENTITY_ID))
                        .queryParam("query_type", SecretQueryType.LIST_SECRETS.name()).build())
                .header(HttpHeaders.AUTHORIZATION, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(SecretResponse.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.LIST_SECRETS.name()));
    }

    @ParameterizedTest
    @MethodSource("validSecretControllerURIs")
    void listSecretPaths_onNonNotaryAuthFailure_403(String uri, String entityType, String entityId, String partialPath) {

        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.error(new ForbiddenException("forbidden")));

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path(buildUrl(uri))
                .queryParam("query_type", SecretQueryType.LIST_SECRETS.name()).build())
                .header(HttpHeaders.AUTHORIZATION, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isEqualTo(HttpStatus.FORBIDDEN)
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.LIST_SECRETS.name()));
        verify(telemetryComponents).recordExceptionWithoutErrorStatus(
                any(ServerWebExchange.class), any(Throwable.class));
        verifyNoInteractions(secretFacade);        
    }

    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @ParameterizedTest
    @MethodSource("validSecretControllerURIs")
    void listSecretPaths_onNonNotaryAuth_200(String uri, String entityType, String entityId, String partialPath) {

        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.just(true));

        when(secretFacade.getSecretPaths(TEST_NAMESPACE, entityType, entityId, partialPath))
                .thenReturn(Mono.just(mock(SecretResponse.class)));
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path(buildUrl(uri))
                        .queryParam("query_type", SecretQueryType.LIST_SECRETS.name()).build())
                .header(HttpHeaders.AUTHORIZATION, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(SecretResponse.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.LIST_SECRETS.name()));
    }


    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @Test
    void listSecretPaths_onNonNotaryAuthAndFetchFailure_500() {

        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.just(true));

        when(secretFacade.getSecretPaths(eq(TEST_NAMESPACE), any(), any(), any()))
                .thenReturn(Mono.error(() -> new InternalErrorException("error")));
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path(buildUrl("/v1/" + TEST_SECRET_PATH))
                        .queryParam("query_type", SecretQueryType.LIST_SECRETS.name()).build())
                .header(HttpHeaders.AUTHORIZATION, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .is5xxServerError()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        var entityType = TEST_SECRET_PATH.split("/+")[0];
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.LIST_SECRETS.name()));
        verify(telemetryComponents).recordException(
                any(ServerWebExchange.class), any(Throwable.class));
    }

    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @ParameterizedTest
    @MethodSource("validSecretControllerURIs")
    void listSecretVersions_onNonNotaryAuth_200(String uri, String entityType, String entityId, String secretPath) {

        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.just(true));

        when(secretFacade.getSecretVersions(TEST_NAMESPACE, entityType, entityId, secretPath))
                .thenReturn(Mono.just(mock(SecretResponse.class)));
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path(buildUrl(uri))
                        .queryParam("query_type", SecretQueryType.LIST_VERSIONS.name()).build())
                .header(HttpHeaders.AUTHORIZATION, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(SecretResponse.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.LIST_VERSIONS.name()));
    }


    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @Test
    void listSecretVersions_onNonNotaryAuthAndFetchFailure_500() {

        when(authChecker.authTenant(eq(TEST_NAMESPACE), any(), eq(new String[]{AuthScope.ESS_SECRETS_CONSUMER, AuthScope.ESS_SECRETS_ADMIN})))
                .thenReturn(Mono.just(true));

        when(secretFacade.getSecretVersions(eq(TEST_NAMESPACE), any(), any(), any()))
                .thenReturn(Mono.error(() -> new InternalErrorException("error")));
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path(buildUrl("/v1/" + TEST_SECRET_PATH))
                        .queryParam("query_type", SecretQueryType.LIST_VERSIONS.name()).build())
                .header(HttpHeaders.AUTHORIZATION, "auth token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .is5xxServerError()
                .expectHeader()
                .contentType(MediaType.APPLICATION_PROBLEM_JSON)
                .expectBody(ProblemDetail.class)
                .returnResult();

        var entityType = TEST_SECRET_PATH.split("/+")[0];
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.LIST_VERSIONS.name()));
        verify(telemetryComponents).recordException(
                any(ServerWebExchange.class), any(Throwable.class));
    }


    // NOTE: not useful test, but needed for coverage. Should be covered with integration tests instead
    @ParameterizedTest
    @MethodSource("validSecretControllerURIs")
    void getSecret_onNotaryAuth_200(String uri, String entityType, String entityId, String secretPath) {

        when(authChecker.authNotaryClient(eq(TEST_NAMESPACE), any(), any()))
                .thenReturn(Mono.just(true));

        when(secretFacade.getSecret(eq(TEST_NAMESPACE), eq(entityType), eq(entityId), eq(secretPath), any()))
                .thenReturn(Mono.just(mock(SecretResponse.class)));
        webTestClient.get()
                .uri(buildUrl(uri))
                .header(X_ESS_TOKEN_HEADER, "ess token")
                .header(X_ESS_NAMESPACE_HEADER, TEST_NAMESPACE)
                .exchange()
                .expectStatus()
                .isOk()
                .expectHeader()
                .contentType(MediaType.APPLICATION_JSON)
                .expectBody(SecretResponse.class)
                .returnResult();

        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(NAMESPACE_KEY), eq(TEST_NAMESPACE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(ENTITY_TYPE_KEY), eq(entityType));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_QUERY_TYPE_KEY),
                eq(SecretQueryType.DEFAULT_VALUE));
        verify(telemetryComponents).setSpanAttribute(
                any(ServerWebExchange.class), eq(SECRET_READ_AUTH_TYPE_KEY),
                eq(AuthorizationType.NOTARY.name()));
    }
}
