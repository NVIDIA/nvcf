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

import io.opentelemetry.sdk.common.CompletableResultCode;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.export.SpanExporter;
import java.util.Collection;

/**
 * SpanExporter that wraps a delegate and shortens exception stack traces in span events
 * before export. Uses {@link ExceptionShorteningProcessor} to filter stack traces to
 * tracked packages (e.g. com.nvidia) plus one line of context.
 */
class ExceptionShorteningSpanExporter implements SpanExporter {

    private final SpanExporter delegate;
    private final ExceptionShorteningProcessor processor;

    ExceptionShorteningSpanExporter(SpanExporter delegate,
                                    ExceptionShorteningProcessor processor) {
        this.delegate = delegate;
        this.processor = processor;
    }

    @Override
    public CompletableResultCode export(Collection<SpanData> spans) {
        var processed = processSpans(spans);
        return delegate.export(processed);
    }

    @Override
    public CompletableResultCode flush() {
        return delegate.flush();
    }

    @Override
    public CompletableResultCode shutdown() {
        return delegate.shutdown();
    }

    private Collection<SpanData> processSpans(Collection<SpanData> spans) {
        return spans.stream()
                .map(this::processSpan)
                .toList();
    }

    private SpanData processSpan(SpanData span) {
        if (!processor.shouldProcess(new MutableSpanData(span))) {
            return span;
        }
        var mutable = new MutableSpanData(span);
        processor.process(mutable);
        return mutable;
    }
}
