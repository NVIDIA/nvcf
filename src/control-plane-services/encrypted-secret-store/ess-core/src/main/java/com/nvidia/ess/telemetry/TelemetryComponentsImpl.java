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

import io.micrometer.observation.Observation;
import io.micrometer.observation.contextpropagation.ObservationThreadLocalAccessor;
import io.micrometer.tracing.Span;
import io.micrometer.tracing.handler.TracingObservationHandler.TracingContext;
import io.micrometer.tracing.otel.bridge.OtelSpan;
import io.opentelemetry.api.trace.StatusCode;
import jakarta.annotation.Nullable;
import org.springframework.http.server.reactive.observation.ServerRequestObservationContext;
import org.springframework.stereotype.Component;
import org.springframework.web.server.ServerWebExchange;
import reactor.util.context.ContextView;

@Component(TelemetryComponentsImpl.BEAN_NAME)
public class TelemetryComponentsImpl implements TelemetryComponents {

    public static final String BEAN_NAME = "telemetryComponentsImpl";

    @Nullable
    private Span spanFromExchange(ServerWebExchange exchange) {
        return ServerRequestObservationContext.findCurrent(exchange.getAttributes())
                .map(ctx -> ctx.<TracingContext>get(TracingContext.class))
                .map(TracingContext::getSpan)
                .orElse(null);
    }

    @Nullable
    private Span spanFromContext(ContextView ctx) {
        return ctx.<Observation>getOrEmpty(ObservationThreadLocalAccessor.KEY)
                .map(obs -> obs.getContextView().<TracingContext>get(TracingContext.class))
                .map(TracingContext::getSpan)
                .orElse(null);
    }

    @Override
    public void setSpanAttribute(ServerWebExchange exchange, String key, String value) {
        var span = spanFromExchange(exchange);
        if (span != null) {
            span.tag(key, value);
        }
    }

    @Override
    public void setSpanAttribute(ServerWebExchange exchange, String key, long value) {
        var span = spanFromExchange(exchange);
        if (span != null) {
            span.tag(key, value);
        }
    }

    @Override
    public void setSpanAttribute(ServerWebExchange exchange, String key, boolean value) {
        var span = spanFromExchange(exchange);
        if (span != null) {
            span.tag(key, value);
        }
    }

    @Override
    public void recordException(ServerWebExchange exchange, Throwable throwable) {
        var span = spanFromExchange(exchange);
        if (span != null) {
            span.error(throwable);
        }
    }

    @Override
    public void recordExceptionWithoutErrorStatus(ServerWebExchange exchange, Throwable throwable) {
        var span = spanFromExchange(exchange);
        if (span instanceof OtelSpan) {
            OtelSpan.toOtel(span).recordException(throwable);
        }
    }

    @Override
    public void setSpanStatusOk(ServerWebExchange exchange) {
        var span = spanFromExchange(exchange);
        if (span instanceof OtelSpan) {
            OtelSpan.toOtel(span).setStatus(StatusCode.OK);
        }
    }

    @Override
    public void setSpanAttribute(ContextView ctx, String key, String value) {
        var span = spanFromContext(ctx);
        if (span != null) {
            span.tag(key, value);
        }
    }

    @Override
    public void setSpanAttribute(ContextView ctx, String key, long value) {
        var span = spanFromContext(ctx);
        if (span != null) {
            span.tag(key, value);
        }
    }

    @Override
    public void setSpanAttribute(ContextView ctx, String key, boolean value) {
        var span = spanFromContext(ctx);
        if (span != null) {
            span.tag(key, value);
        }
    }


    @Override
    public void recordException(ContextView ctx, Throwable throwable) {
        var span = spanFromContext(ctx);
        if (span != null) {
            span.error(throwable);
        }
    }

    @Override
    public void recordExceptionWithoutErrorStatus(ContextView ctx, Throwable throwable) {
        var span = spanFromContext(ctx);
        if (span instanceof OtelSpan) {
            OtelSpan.toOtel(span).recordException(throwable);
        }
    }

    @Override
    public void setSpanStatusOk(ContextView ctx) {
        var span = spanFromContext(ctx);
        if (span instanceof OtelSpan) {
            OtelSpan.toOtel(span).setStatus(StatusCode.OK);
        }
    }
}
