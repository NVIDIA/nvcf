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

package com.nvidia.boot.observability.tracing.stacktrace;

import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.trace.SpanContext;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.sdk.common.InstrumentationLibraryInfo;
import io.opentelemetry.sdk.common.InstrumentationScopeInfo;
import io.opentelemetry.sdk.resources.Resource;
import io.opentelemetry.sdk.trace.data.EventData;
import io.opentelemetry.sdk.trace.data.LinkData;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.data.StatusData;
import java.util.List;
import lombok.AccessLevel;
import lombok.Data;
import lombok.Getter;
import lombok.NoArgsConstructor;

/**
 * Mutable implementation of SpanData for processing spans before export.
 * Used by ExceptionShorteningSpanExporter to modify exception events.
 */
@Data
@NoArgsConstructor(access = AccessLevel.PACKAGE)
class MutableSpanData implements SpanData {

    @Getter(AccessLevel.NONE)
    private boolean hasEnded;
    private Resource resource;
    private InstrumentationScopeInfo instrumentationScopeInfo;
    private String name;
    private SpanKind kind;
    private long startEpochNanos;
    private Attributes attributes;
    private List<EventData> events;
    private List<LinkData> links;
    private StatusData status;
    private long endEpochNanos;
    private SpanContext spanContext;
    private SpanContext parentSpanContext;

    MutableSpanData(SpanData spanData) {
        this.resource = spanData.getResource();
        this.instrumentationScopeInfo = spanData.getInstrumentationScopeInfo();
        this.name = spanData.getName();
        this.kind = spanData.getKind();
        this.startEpochNanos = spanData.getStartEpochNanos();
        this.attributes = spanData.getAttributes();
        this.events = spanData.getEvents();
        this.links = spanData.getLinks();
        this.status = spanData.getStatus();
        this.endEpochNanos = spanData.getEndEpochNanos();
        this.hasEnded = spanData.hasEnded();
        this.spanContext = spanData.getSpanContext();
        this.parentSpanContext = spanData.getParentSpanContext();
    }

    @Override
    public boolean hasEnded() {
        return hasEnded;
    }

    @Override
    public int getTotalRecordedEvents() {
        return events.size();
    }

    @Override
    public int getTotalRecordedLinks() {
        return links.size();
    }

    @Override
    public int getTotalAttributeCount() {
        return attributes.size();
    }

    /**
     * {@link SpanData} still requires this until OpenTelemetry removes the legacy API. Prefer
     * {@link #getInstrumentationScopeInfo()} (stored field copied from the source span).
     */
    @Override
    @SuppressWarnings("deprecation")
    public InstrumentationLibraryInfo getInstrumentationLibraryInfo() {
        return InstrumentationLibraryInfo.create(
                instrumentationScopeInfo.getName(),
                instrumentationScopeInfo.getVersion(),
                instrumentationScopeInfo.getSchemaUrl());
    }
}
