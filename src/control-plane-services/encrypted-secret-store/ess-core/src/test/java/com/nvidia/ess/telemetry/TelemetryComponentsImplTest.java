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
package com.nvidia.ess.telemetry;

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import io.micrometer.observation.Observation;
import io.micrometer.observation.contextpropagation.ObservationThreadLocalAccessor;
import io.micrometer.tracing.Span;
import io.micrometer.tracing.handler.TracingObservationHandler.TracingContext;
import io.micrometer.tracing.otel.bridge.OtelSpan;
import io.opentelemetry.api.trace.SpanContext;
import io.opentelemetry.api.trace.StatusCode;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Nested;
import org.junit.jupiter.api.Test;
import org.springframework.http.server.reactive.observation.ServerRequestObservationContext;
import org.springframework.mock.http.server.reactive.MockServerHttpRequest;
import org.springframework.mock.http.server.reactive.MockServerHttpResponse;
import org.springframework.mock.web.server.MockServerWebExchange;
import reactor.util.context.Context;
import reactor.util.context.ContextView;

class TelemetryComponentsImplTest {

    private Span span;
    private io.opentelemetry.api.trace.Span otelDelegate;
    private TelemetryComponentsImpl telemetryComponents;

    @BeforeEach
    void setUp() {
        span = mock(Span.class);
        otelDelegate = mock(io.opentelemetry.api.trace.Span.class);
        when(otelDelegate.getSpanContext()).thenReturn(SpanContext.getInvalid());
        telemetryComponents = new TelemetryComponentsImpl();
    }

    private OtelSpan otelSpan() {
        return new OtelSpan(otelDelegate);
    }

    private MockServerWebExchange exchangeWithSpan(Span span) {
        MockServerHttpRequest request = MockServerHttpRequest.get("/test").build();
        MockServerWebExchange exchange = MockServerWebExchange.from(request);

        ServerRequestObservationContext obsCtx = new ServerRequestObservationContext(
                request, new MockServerHttpResponse(), exchange.getAttributes());
        TracingContext tracingContext = new TracingContext();
        tracingContext.setSpan(span);
        obsCtx.put(TracingContext.class, tracingContext);
        exchange.getAttributes().put(
                ServerRequestObservationContext.CURRENT_OBSERVATION_CONTEXT_ATTRIBUTE, obsCtx);

        return exchange;
    }

    private ContextView contextViewWithSpan(Span span) {
        Observation observation = mock(Observation.class);
        Observation.ContextView obsContextView = mock(Observation.ContextView.class);
        TracingContext tracingContext = new TracingContext();
        tracingContext.setSpan(span);

        when(observation.getContextView()).thenReturn(obsContextView);
        when(obsContextView.get(TracingContext.class)).thenReturn(tracingContext);

        return Context.of(ObservationThreadLocalAccessor.KEY, observation);
    }

    @Nested
    class ExchangeBased {

        @Test
        void addSpanAttribute_withString_tagsSpan() {
            var exchange = exchangeWithSpan(span);

            telemetryComponents.setSpanAttribute(exchange, "env", "production");

            verify(span).tag("env", "production");
        }

        @Test
        void addSpanAttribute_withLong_tagsSpan() {
            var exchange = exchangeWithSpan(span);

            telemetryComponents.setSpanAttribute(exchange, "retries", 5L);

            verify(span).tag("retries", 5L);
        }

        @Test
        void addSpanAttribute_withBoolean_tagsSpan() {
            var exchange = exchangeWithSpan(span);

            telemetryComponents.setSpanAttribute(exchange, "cached", true);

            verify(span).tag("cached", true);
        }

        @Test
        void recordException_setsError() {
            var exchange = exchangeWithSpan(span);
            var ex = new RuntimeException("test");

            telemetryComponents.recordException(exchange, ex);

            verify(span).error(ex);
        }

        @Test
        void recordExceptionWithoutErrorStatus_recordsExceptionOnly() {
            var oSpan = otelSpan();
            var exchange = exchangeWithSpan(oSpan);
            var ex = new RuntimeException("test");

            telemetryComponents.recordExceptionWithoutErrorStatus(exchange, ex);

            verify(otelDelegate).recordException(ex);
            verify(otelDelegate, never()).setStatus(any(StatusCode.class));
            verify(otelDelegate, never()).setStatus(any(StatusCode.class), any());
        }

        @Test
        void setSpanStatusOk_setsStatusOk() {
            var oSpan = otelSpan();
            var exchange = exchangeWithSpan(oSpan);

            telemetryComponents.setSpanStatusOk(exchange);

            verify(otelDelegate).setStatus(StatusCode.OK);
        }

        @Test
        void recordExceptionWithoutErrorStatus_whenNonOtelSpan_doesNothing() {
            var exchange = exchangeWithSpan(span);
            var ex = new RuntimeException("test");

            telemetryComponents.recordExceptionWithoutErrorStatus(exchange, ex);

            verifyNoInteractions(span);
        }

        @Test
        void addSpanAttribute_whenNoObservationContext_doesNothing() {
            MockServerHttpRequest request = MockServerHttpRequest.get("/test").build();
            MockServerWebExchange exchange = MockServerWebExchange.from(request);

            telemetryComponents.setSpanAttribute(exchange, "key", "value");

            verifyNoInteractions(span);
        }

        @Test
        void addSpanAttribute_withLong_whenNoObservationContext_doesNothing() {
            MockServerHttpRequest request = MockServerHttpRequest.get("/test").build();
            MockServerWebExchange exchange = MockServerWebExchange.from(request);

            telemetryComponents.setSpanAttribute(exchange, "key", 42L);

            verifyNoInteractions(span);
        }

        @Test
        void addSpanAttribute_withBoolean_whenNoObservationContext_doesNothing() {
            MockServerHttpRequest request = MockServerHttpRequest.get("/test").build();
            MockServerWebExchange exchange = MockServerWebExchange.from(request);

            telemetryComponents.setSpanAttribute(exchange, "key", true);

            verifyNoInteractions(span);
        }

        @Test
        void recordException_whenNoObservationContext_doesNothing() {
            MockServerHttpRequest request = MockServerHttpRequest.get("/test").build();
            MockServerWebExchange exchange = MockServerWebExchange.from(request);

            telemetryComponents.recordException(exchange, new RuntimeException("test"));

            verifyNoInteractions(span);
        }

        @Test
        void recordExceptionWithoutErrorStatus_whenNoObservationContext_doesNothing() {
            MockServerHttpRequest request = MockServerHttpRequest.get("/test").build();
            MockServerWebExchange exchange = MockServerWebExchange.from(request);

            telemetryComponents.recordExceptionWithoutErrorStatus(
                    exchange, new RuntimeException("test"));

            verifyNoInteractions(span);
        }

        @Test
        void setSpanStatusOk_whenNoObservationContext_doesNothing() {
            MockServerHttpRequest request = MockServerHttpRequest.get("/test").build();
            MockServerWebExchange exchange = MockServerWebExchange.from(request);

            telemetryComponents.setSpanStatusOk(exchange);

            verifyNoInteractions(span);
        }

        @Test
        void setSpanStatusOk_whenNonOtelSpan_doesNothing() {
            var exchange = exchangeWithSpan(span);

            telemetryComponents.setSpanStatusOk(exchange);

            verifyNoInteractions(otelDelegate);
        }
    }

    @Nested
    class ContextViewBased {

        @Test
        void addSpanAttribute_withString_tagsSpan() {
            var ctx = contextViewWithSpan(span);

            telemetryComponents.setSpanAttribute(ctx, "env", "production");

            verify(span).tag("env", "production");
        }

        @Test
        void addSpanAttribute_withLong_tagsSpan() {
            var ctx = contextViewWithSpan(span);

            telemetryComponents.setSpanAttribute(ctx, "retries", 5L);

            verify(span).tag("retries", 5L);
        }

        @Test
        void addSpanAttribute_withBoolean_tagsSpan() {
            var ctx = contextViewWithSpan(span);

            telemetryComponents.setSpanAttribute(ctx, "cached", true);

            verify(span).tag("cached", true);
        }

        @Test
        void recordException_setsError() {
            var ctx = contextViewWithSpan(span);
            var ex = new RuntimeException("test");

            telemetryComponents.recordException(ctx, ex);

            verify(span).error(ex);
        }

        @Test
        void recordExceptionWithoutErrorStatus_recordsExceptionOnly() {
            var oSpan = otelSpan();
            var ctx = contextViewWithSpan(oSpan);
            var ex = new RuntimeException("test");

            telemetryComponents.recordExceptionWithoutErrorStatus(ctx, ex);

            verify(otelDelegate).recordException(ex);
            verify(otelDelegate, never()).setStatus(any(StatusCode.class));
            verify(otelDelegate, never()).setStatus(any(StatusCode.class), any());
        }

        @Test
        void setSpanStatusOk_setsStatusOk() {
            var oSpan = otelSpan();
            var ctx = contextViewWithSpan(oSpan);

            telemetryComponents.setSpanStatusOk(ctx);

            verify(otelDelegate).setStatus(StatusCode.OK);
        }

        @Test
        void recordExceptionWithoutErrorStatus_whenNonOtelSpan_doesNothing() {
            var ctx = contextViewWithSpan(span);
            var ex = new RuntimeException("test");

            telemetryComponents.recordExceptionWithoutErrorStatus(ctx, ex);

            verifyNoInteractions(span);
        }

        @Test
        void addSpanAttribute_whenNoObservation_doesNothing() {
            ContextView emptyCtx = Context.empty();

            telemetryComponents.setSpanAttribute(emptyCtx, "key", "value");

            verifyNoInteractions(span);
        }

        @Test
        void addSpanAttribute_whenNoTracingContext_doesNothing() {
            Observation observation = mock(Observation.class);
            Observation.ContextView obsContextView = mock(Observation.ContextView.class);
            when(observation.getContextView()).thenReturn(obsContextView);
            when(obsContextView.get(TracingContext.class)).thenReturn(null);

            ContextView ctx = Context.of(ObservationThreadLocalAccessor.KEY, observation);

            telemetryComponents.setSpanAttribute(ctx, "key", "value");

            verifyNoInteractions(span);
        }

        @Test
        void addSpanAttribute_withLong_whenNoObservation_doesNothing() {
            ContextView emptyCtx = Context.empty();

            telemetryComponents.setSpanAttribute(emptyCtx, "key", 42L);

            verifyNoInteractions(span);
        }

        @Test
        void addSpanAttribute_withBoolean_whenNoObservation_doesNothing() {
            ContextView emptyCtx = Context.empty();

            telemetryComponents.setSpanAttribute(emptyCtx, "key", true);

            verifyNoInteractions(span);
        }

        @Test
        void recordException_whenNoObservation_doesNothing() {
            ContextView emptyCtx = Context.empty();

            telemetryComponents.recordException(emptyCtx, new RuntimeException("test"));

            verifyNoInteractions(span);
        }

        @Test
        void recordExceptionWithoutErrorStatus_whenNoObservation_doesNothing() {
            ContextView emptyCtx = Context.empty();

            telemetryComponents.recordExceptionWithoutErrorStatus(
                    emptyCtx, new RuntimeException("test"));

            verifyNoInteractions(span);
        }

        @Test
        void setSpanStatusOk_whenNoObservation_doesNothing() {
            ContextView emptyCtx = Context.empty();

            telemetryComponents.setSpanStatusOk(emptyCtx);

            verifyNoInteractions(span);
        }

        @Test
        void setSpanStatusOk_whenNonOtelSpan_doesNothing() {
            var ctx = contextViewWithSpan(span);

            telemetryComponents.setSpanStatusOk(ctx);

            verifyNoInteractions(otelDelegate);
        }
    }
}
