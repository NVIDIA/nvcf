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
package com.nvidia.icms.service.telemetry;

import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.service.telemetry.model.UnifiedErrorMetric;
import io.micrometer.tracing.Tracer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

public class DatadogEventLogger {

    private static final Logger ddLogger = LoggerFactory.getLogger("datadog.events");
    private static final String DD_TRACE_ID = "dd.trace_id";
    private static final String DD_SPAN_ID = "dd.span_id";

    private final boolean enabled;
    private final ObjectMapper objectMapper;
    private final Tracer tracer;

    public DatadogEventLogger(boolean enabled, ObjectMapper objectMapper, Tracer tracer) {
        this.enabled = enabled;
        this.objectMapper = objectMapper;
        this.tracer = tracer;
    }

    public void logEvent(GenericMetric metric) {
        if (!enabled || metric == null) {
            return;
        }
        try {
            ObjectNode node = objectMapper.valueToTree(metric);
            injectTraceContext(node);
            ddLogger.info(objectMapper.writeValueAsString(node));
        } catch (Exception e) {
            ddLogger.warn("Failed to serialize GenericMetric for Datadog: {}", e.getMessage(), e);
        }
    }

    public void logErrorEvent(UnifiedErrorMetric metric) {
        if (!enabled || metric == null) {
            return;
        }
        try {
            ObjectNode node = objectMapper.valueToTree(metric);
            injectTraceContext(node);
            ddLogger.info(objectMapper.writeValueAsString(node));
        } catch (Exception e) {
            ddLogger.warn("Failed to serialize UnifiedErrorMetric for Datadog: {}", e.getMessage(), e);
        }
    }

    private void injectTraceContext(ObjectNode node) {
        var span = tracer.currentSpan();
        if (span != null) {
            var context = span.context();
            if (context != null) {
                if (context.traceId() != null) {
                    node.put(DD_TRACE_ID, convertToDecimal(context.traceId()));
                }
                if (context.spanId() != null) {
                    node.put(DD_SPAN_ID, convertToDecimal(context.spanId()));
                }
            }
        }
    }

    /**
     * Converts a hex trace/span ID to its decimal string representation.
     * Datadog expects decimal IDs for trace-log correlation.
     * Uses the lower 64 bits for 128-bit trace IDs.
     */
    private String convertToDecimal(String hexId) {
        if (hexId.length() > 16) {
            hexId = hexId.substring(hexId.length() - 16);
        }
        return Long.toUnsignedString(Long.parseUnsignedLong(hexId, 16));
    }
}
