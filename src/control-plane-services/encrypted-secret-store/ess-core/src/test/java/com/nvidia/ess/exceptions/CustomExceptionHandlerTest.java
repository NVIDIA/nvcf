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
package com.nvidia.ess.exceptions;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;

import com.nvidia.ess.config.properties.LoggingProperties;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.telemetry.TelemetryComponents;
import java.util.Locale;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.context.i18n.LocaleContext;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.http.server.reactive.ServerHttpRequest;
import org.springframework.http.server.reactive.ServerHttpResponse;
import org.springframework.web.ErrorResponseException;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
class CustomExceptionHandlerTest {

    private CustomExceptionHandler handler;

    @Mock
    private TelemetryComponents telemetryComponents;

    @Mock
    private CustomMetricsRegistry customMetricsRegistry;

    @Mock
    private ServerWebExchange exchange;

    @Mock
    private ServerHttpRequest request;

    @Mock
    private ServerHttpResponse response;

    @BeforeEach
    void setUp() {
        handler = new CustomExceptionHandler();
        handler.setTelemetryComponents(telemetryComponents);
        handler.setCustomMetricsRegistry(customMetricsRegistry);

        var loggingProperties = new LoggingProperties();
        handler.setLoggingProperties(loggingProperties);

        LocaleContext localeContext = mock(LocaleContext.class);
        lenient().when(localeContext.getLocale()).thenReturn(Locale.ENGLISH);

        lenient().when(exchange.getRequest()).thenReturn(request);
        lenient().when(exchange.getResponse()).thenReturn(response);
        lenient().when(exchange.getLocaleContext()).thenReturn(localeContext);
        lenient().when(request.getHeaders()).thenReturn(HttpHeaders.EMPTY);
        lenient().when(response.getHeaders()).thenReturn(new HttpHeaders());
    }

    @Test
    void handleExceptionInternal_errorResponseWithCause_addsCauseToBody() {
        var cause = new RuntimeException("underlying database timeout");
        var ex = new ErrorResponseException(HttpStatus.INTERNAL_SERVER_ERROR, cause);

        Mono<ResponseEntity<Object>> result = handler.handleExceptionInternal(
                ex, null, HttpHeaders.EMPTY, HttpStatus.INTERNAL_SERVER_ERROR, exchange);

        StepVerifier.create(result)
                .assertNext(entity -> {
                    ProblemDetail body = (ProblemDetail) entity.getBody();
                    assertNotNull(body.getProperties(),
                            "Properties map should exist when cause is present");
                    assertEquals("underlying database timeout",
                            body.getProperties().get("cause"));
                })
                .verifyComplete();
    }

    @Test
    void handleExceptionInternal_errorResponseWithCauseNoMessage_doesNotAddCause() {
        var cause = new RuntimeException((String) null);
        var ex = new ErrorResponseException(HttpStatus.BAD_REQUEST, cause);

        Mono<ResponseEntity<Object>> result = handler.handleExceptionInternal(
                ex, null, HttpHeaders.EMPTY, HttpStatus.BAD_REQUEST, exchange);

        StepVerifier.create(result)
                .assertNext(entity -> {
                    ProblemDetail body = (ProblemDetail) entity.getBody();
                    assertNull(body.getProperties());
                })
                .verifyComplete();
    }

    @Test
    void handleExceptionInternal_errorResponseWithNoCause_doesNotAddCause() {
        var ex = new ErrorResponseException(HttpStatus.NOT_FOUND);

        Mono<ResponseEntity<Object>> result = handler.handleExceptionInternal(
                ex, null, HttpHeaders.EMPTY, HttpStatus.NOT_FOUND, exchange);

        StepVerifier.create(result)
                .assertNext(entity -> {
                    ProblemDetail body = (ProblemDetail) entity.getBody();
                    assertNull(body.getProperties());
                })
                .verifyComplete();
    }

    @Test
    void handleExceptionInternal_nonErrorResponseException_doesNotAddCause() {
        var ex = new RuntimeException("some error");

        Mono<ResponseEntity<Object>> result = handler.handleExceptionInternal(
                ex, null, HttpHeaders.EMPTY, HttpStatus.INTERNAL_SERVER_ERROR, exchange);

        StepVerifier.create(result)
                .assertNext(entity -> {
                    if (entity.getBody() instanceof ProblemDetail body) {
                        assertTrue(body.getProperties() == null
                                || !body.getProperties().containsKey("cause"));
                    }
                })
                .verifyComplete();
    }
}
