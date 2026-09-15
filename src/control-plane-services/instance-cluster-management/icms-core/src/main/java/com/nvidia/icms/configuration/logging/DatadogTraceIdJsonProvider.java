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
package com.nvidia.icms.configuration.logging;

import ch.qos.logback.classic.spi.ILoggingEvent;
import com.fasterxml.jackson.core.JsonGenerator;
import java.io.IOException;
import java.util.Map;
import net.logstash.logback.composite.AbstractJsonProvider;

/**
 * Logstash-logback-encoder provider that converts 128-bit hex traceId / 64-bit hex spanId
 * (injected into MDC by Spring Boot 3 Micrometer tracing bridge) into Datadog decimal
 * {@code dd.trace_id} / {@code dd.span_id} for native log-to-trace correlation.
 */
public class DatadogTraceIdJsonProvider extends AbstractJsonProvider<ILoggingEvent> {

    private static final String MDC_TRACE_ID = "traceId";
    private static final String MDC_SPAN_ID = "spanId";
    private static final int OTEL_TRACE_ID_HEX_LENGTH = 32;
    private static final int OTEL_SPAN_ID_HEX_LENGTH = 16;

    @Override
    public void writeTo(JsonGenerator generator, ILoggingEvent event) throws IOException {
        Map<String, String> mdc = event.getMDCPropertyMap();
        String traceId = mdc.get(MDC_TRACE_ID);
        String spanId = mdc.get(MDC_SPAN_ID);

        try {
            if (traceId != null && traceId.length() == OTEL_TRACE_ID_HEX_LENGTH) {
                generator.writeStringField("dd.trace_id",
                        Long.toUnsignedString(Long.parseUnsignedLong(traceId.substring(16), 16)));
            }
        } catch (NumberFormatException e) {
            addWarn("Malformed OTel trace_id, skipping dd.trace_id: " + traceId, e);
        }
        try {
            if (spanId != null && spanId.length() == OTEL_SPAN_ID_HEX_LENGTH) {
                generator.writeStringField("dd.span_id",
                        Long.toUnsignedString(Long.parseUnsignedLong(spanId, 16)));
            }
        } catch (NumberFormatException e) {
            addWarn("Malformed OTel span_id, skipping dd.span_id: " + spanId, e);
        }
    }
}
