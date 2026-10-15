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

import io.opentelemetry.sdk.trace.data.EventData;
import io.opentelemetry.semconv.ExceptionAttributes;
import java.util.ArrayList;
import java.util.Collection;
import java.util.Collections;
import java.util.stream.Collectors;
import org.apache.commons.lang3.StringUtils;
import org.springframework.util.CollectionUtils;

/**
 * Shortens exception stack traces in span events by keeping only the first line through
 * the last line matching tracked packages (e.g. com.nvidia) plus one line of context.
 *
 * <p>Example: a stack trace with com.nvidia, spring, and tomcat frames will be shortened
 * to keep the exception line plus com.nvidia frames and one additional frame for context.
 */
class ExceptionShorteningProcessor {

    private static final String EXCEPTION_EVENT_NAME = "exception";

    private final Collection<String> packages;
    private final boolean shortenExceptions;

    ExceptionShorteningProcessor(Collection<String> packages, boolean shortenExceptions) {
        this.packages = CollectionUtils.isEmpty(packages)
                ? Collections.singleton("com.nvidia") : packages;
        this.shortenExceptions = shortenExceptions;
    }

    boolean shouldProcess(MutableSpanData span) {
        return span.getEvents().stream()
                .map(EventData::getName)
                .anyMatch(EXCEPTION_EVENT_NAME::equals);
    }

    void process(MutableSpanData span) {
        var events = span.getEvents();
        var exceptionIndex = -1;
        for (var i = 0; i < events.size(); i++) {
            if (EXCEPTION_EVENT_NAME.equals(events.get(i).getName())) {
                exceptionIndex = i;
                break;
            }
        }
        if (exceptionIndex < 0) {
            return;
        }

        var event = events.get(exceptionIndex);
        var stack = event.getAttributes().get(ExceptionAttributes.EXCEPTION_STACKTRACE);
        if (stack != null) {
            var eventAttributesBuilder = event.getAttributes().toBuilder();
            eventAttributesBuilder.put(ExceptionAttributes.EXCEPTION_STACKTRACE, filterStack(stack));
            var modifiedAttributes = eventAttributesBuilder.build();
            var newEvent = EventData.create(
                    event.getEpochNanos(), event.getName(), modifiedAttributes);
            var eventsCopy = new ArrayList<EventData>(events);
            eventsCopy.set(exceptionIndex, newEvent);
            span.setEvents(eventsCopy);
        }

        var attributesBuilder = span.getAttributes().toBuilder();
        var type = event.getAttributes().get(ExceptionAttributes.EXCEPTION_TYPE);
        if (StringUtils.isNotBlank(type)) {
            attributesBuilder.put(ExceptionAttributes.EXCEPTION_TYPE, type);
        }
        var message = event.getAttributes().get(ExceptionAttributes.EXCEPTION_MESSAGE);
        if (StringUtils.isNotBlank(message)) {
            attributesBuilder.put(ExceptionAttributes.EXCEPTION_MESSAGE, message);
        }
        span.setAttributes(attributesBuilder.build());
    }

    /**
     * Keeps the first line (exception type + message) through the last tracked package's
     * line plus one line for context.
     */
    String filterStack(String stack) {
        if (!shortenExceptions) {
            return stack;
        }
        var lines = stack.lines().collect(Collectors.toList());
        var lastLineIndex = 1;
        for (var i = lastLineIndex; i < lines.size(); i++) {
            var line = lines.get(i);
            if (packages.stream().noneMatch(line::contains)) {
                lastLineIndex = i;
                break;
            }
        }
        return lines.stream()
                .limit(lastLineIndex + 1L)
                .collect(Collectors.joining(System.lineSeparator()));
    }
}
