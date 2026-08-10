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

package com.nvidia.boot.observability.tracing.redaction;

import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.common.AttributesBuilder;
import io.opentelemetry.sdk.common.CompletableResultCode;
import io.opentelemetry.sdk.common.InstrumentationLibraryInfo;
import io.opentelemetry.sdk.common.InstrumentationScopeInfo;
import io.opentelemetry.sdk.resources.Resource;
import io.opentelemetry.sdk.trace.data.EventData;
import io.opentelemetry.sdk.trace.data.LinkData;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.data.StatusData;
import io.opentelemetry.sdk.trace.export.SpanExporter;
import java.util.Collection;
import java.util.List;
import java.util.Set;
import java.util.regex.Pattern;
import java.util.stream.Collectors;

/**
 * SpanExporter that wraps a delegate and redacts sensitive attribute values
 * (e.g. Cassandra query parameters) before export.
 */
class AttributeRedactingSpanExporter implements SpanExporter {

    private static final String DB_SYSTEM_CASSANDRA = "cassandra";
    private static final String REDACTED_PLACEHOLDER = "?";

    private final SpanExporter delegate;
    private final Pattern columnValuePattern;
    private final boolean hasSensitiveColumns;

    AttributeRedactingSpanExporter(SpanExporter delegate, Set<String> sensitiveColumns) {
        this.delegate = delegate;
        this.hasSensitiveColumns = sensitiveColumns != null && !sensitiveColumns.isEmpty();
        if (hasSensitiveColumns) {
            var columnRegex = sensitiveColumns.stream()
                    .map(Pattern::quote)
                    .collect(Collectors.joining("|", "(", ")"));
            this.columnValuePattern = Pattern.compile(
                    columnRegex + "\\s*=\\s*'[^']*'",
                    Pattern.CASE_INSENSITIVE);
        } else {
            this.columnValuePattern = null;
        }
    }

    @Override
    public CompletableResultCode export(Collection<SpanData> spans) {
        if (!hasSensitiveColumns) {
            return delegate.export(spans);
        }
        var redacted = spans.stream()
                .map(this::redactIfCassandra)
                .collect(Collectors.toList());
        return delegate.export(redacted);
    }

    private SpanData redactIfCassandra(SpanData span) {
        var dbSystem = span.getAttributes().get(AttributeKey.stringKey("db.system"));
        if (!DB_SYSTEM_CASSANDRA.equals(dbSystem)) {
            return span;
        }
        return new RedactingSpanData(span, columnValuePattern);
    }

    @Override
    public CompletableResultCode flush() {
        return delegate.flush();
    }

    @Override
    public CompletableResultCode shutdown() {
        return delegate.shutdown();
    }

    private static class RedactingSpanData implements SpanData {

        private final SpanData delegate;
        private final Attributes redactedAttributes;

        RedactingSpanData(SpanData delegate, Pattern columnValuePattern) {
            this.delegate = delegate;
            var original = delegate.getAttributes();
            var builder = Attributes.builder();
            original.forEach((key, value) -> {
                var keyStr = key.getKey();
                if (("db.query.text".equals(keyStr) || keyStr.startsWith("db.query.parameter."))
                        && value instanceof String) {
                    var str = (String) value;
                    var redacted = columnValuePattern.matcher(str).replaceAll("$1 = " + REDACTED_PLACEHOLDER);
                    builder.put(keyStr, redacted);
                } else {
                    putAttributeValue(builder, keyStr, value);
                }
            });
            this.redactedAttributes = builder.build();
        }

        @SuppressWarnings("unchecked")
        private static void putAttributeValue(AttributesBuilder builder, String key, Object value) {
            if (value instanceof String) {
                builder.put(key, (String) value);
            } else if (value instanceof Long) {
                builder.put(key, (Long) value);
            } else if (value instanceof Double) {
                builder.put(key, (Double) value);
            } else if (value instanceof Boolean) {
                builder.put(key, (Boolean) value);
            } else if (value instanceof List) {
                var list = (List<?>) value;
                if (!list.isEmpty() && list.get(0) instanceof String) {
                    builder.put(key, ((List<String>) list).toArray(new String[0]));
                }
            }
        }

        @Override
        public Attributes getAttributes() {
            return redactedAttributes;
        }

        @Override
        public int getTotalAttributeCount() {
            return redactedAttributes.size();
        }

        @Override
        public String getName() {
            return delegate.getName();
        }

        @Override
        public List<LinkData> getLinks() {
            return delegate.getLinks();
        }

        @Override
        public int getTotalRecordedLinks() {
            return delegate.getTotalRecordedLinks();
        }

        @Override
        public int getTotalRecordedEvents() {
            return delegate.getTotalRecordedEvents();
        }

        @Override
        public io.opentelemetry.api.trace.SpanContext getSpanContext() {
            return delegate.getSpanContext();
        }

        @Override
        public io.opentelemetry.api.trace.SpanContext getParentSpanContext() {
            return delegate.getParentSpanContext();
        }

        @Override
        public String getTraceId() {
            return delegate.getTraceId();
        }

        @Override
        public String getSpanId() {
            return delegate.getSpanId();
        }

        @Override
        public StatusData getStatus() {
            return delegate.getStatus();
        }

        @Override
        public long getStartEpochNanos() {
            return delegate.getStartEpochNanos();
        }

        @Override
        public List<EventData> getEvents() {
            return delegate.getEvents();
        }

        @Override
        public long getEndEpochNanos() {
            return delegate.getEndEpochNanos();
        }

        @Override
        public boolean hasEnded() {
            return delegate.hasEnded();
        }

        @Override
        public Resource getResource() {
            return delegate.getResource();
        }

        @Override
        public InstrumentationScopeInfo getInstrumentationScopeInfo() {
            return delegate.getInstrumentationScopeInfo();
        }

        @Override
        public io.opentelemetry.api.trace.SpanKind getKind() {
            return delegate.getKind();
        }

        @Override
        @SuppressWarnings("deprecation")
        public InstrumentationLibraryInfo getInstrumentationLibraryInfo() {
            return delegate.getInstrumentationLibraryInfo();
        }
    }
}
