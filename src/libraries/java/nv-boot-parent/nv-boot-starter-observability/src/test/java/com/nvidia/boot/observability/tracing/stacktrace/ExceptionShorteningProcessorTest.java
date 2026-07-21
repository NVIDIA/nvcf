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

import static org.assertj.core.api.Assertions.assertThat;

import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.sdk.testing.trace.TestSpanData;
import io.opentelemetry.sdk.trace.data.EventData;
import io.opentelemetry.sdk.trace.data.StatusData;
import io.opentelemetry.semconv.ExceptionAttributes;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import org.junit.jupiter.api.Test;

class ExceptionShorteningProcessorTest {

    private static final String NEW_LINE = System.lineSeparator();
    private static final String DEFAULT_SHORT = "com.nvidia.exception asdf" + NEW_LINE
            + "com.nvidia.exception2 asdf2" + NEW_LINE
            + "not.nvidia.exception3 asdf3";
    private static final String DEFAULT_FULL = "com.nvidia.exception asdf" + NEW_LINE
            + "com.nvidia.exception2 asdf2" + NEW_LINE
            + "not.nvidia.exception3 asdf3" + NEW_LINE
            + "really.not.nvidia.exception4" + NEW_LINE;

    @Test
    void filterStackDefault() {
        var processor = new ExceptionShorteningProcessor(null, true);
        assertThat(processor.filterStack(DEFAULT_FULL)).isEqualTo(DEFAULT_SHORT);
    }

    @Test
    void filterStackDisabled() {
        var processor = new ExceptionShorteningProcessor(null, false);
        assertThat(processor.filterStack(DEFAULT_FULL)).isEqualTo(DEFAULT_FULL);
    }

    @Test
    void filterStackMultiplePackages() {
        var packages = Arrays.asList("com.nvidia", "not.nvidia");
        var processor = new ExceptionShorteningProcessor(packages, true);
        var input = "com.nvidia.exception asdf" + NEW_LINE
                + "com.nvidia.exception2 asdf2" + NEW_LINE
                + "not.nvidia.exception3 asdf3" + NEW_LINE
                + "not2.nvidia.exception4" + NEW_LINE
                + "not3.nvidia.exception5" + NEW_LINE;
        var expected = "com.nvidia.exception asdf" + NEW_LINE
                + "com.nvidia.exception2 asdf2" + NEW_LINE
                + "not.nvidia.exception3 asdf3" + NEW_LINE
                + "not2.nvidia.exception4";
        assertThat(processor.filterStack(input)).isEqualTo(expected);
    }

    @Test
    void shouldProcessReturnsFalseWhenNoExceptionEvent() {
        var processor = new ExceptionShorteningProcessor(Collections.emptyList(), true);
        var span = new MutableSpanData(TestSpanData.builder()
                .setName("test")
                .setKind(SpanKind.CLIENT)
                .setStartEpochNanos(1234)
                .setEndEpochNanos(12345)
                .setStatus(StatusData.ok())
                .setHasEnded(true)
                .build());
        assertThat(processor.shouldProcess(span)).isFalse();
    }

    @Test
    void shouldProcessReturnsTrueWhenExceptionEventPresent() {
        var processor = new ExceptionShorteningProcessor(Collections.emptyList(), true);
        var span = new MutableSpanData(TestSpanData.builder()
                .setName("test")
                .setKind(SpanKind.CLIENT)
                .setStartEpochNanos(1234)
                .setEndEpochNanos(12345)
                .setStatus(StatusData.ok())
                .setHasEnded(true)
                .setEvents(List.of(EventData.create(0, "exception", Attributes.empty())))
                .build());
        assertThat(processor.shouldProcess(span)).isTrue();
    }

    @Test
    void processShortensStackAndPromotesToSpanAttributes() {
        var span = new MutableSpanData(TestSpanData.builder()
                .setName("test")
                .setKind(SpanKind.CLIENT)
                .setStartEpochNanos(1234)
                .setEndEpochNanos(12345)
                .setStatus(StatusData.ok())
                .setHasEnded(true)
                .setEvents(List.of(EventData.create(0, "exception",
                        Attributes.builder()
                                .put(ExceptionAttributes.EXCEPTION_TYPE, "type")
                                .put(ExceptionAttributes.EXCEPTION_MESSAGE, "message")
                                .put(ExceptionAttributes.EXCEPTION_STACKTRACE, DEFAULT_FULL)
                                .build())))
                .build());
        var processor = new ExceptionShorteningProcessor(List.of("com.nvidia"), true);
        processor.process(span);

        assertThat(span.getEvents()).hasSize(1);
        var event = span.getEvents().get(0);
        assertThat(event.getName()).isEqualTo("exception");
        assertThat(event.getAttributes().get(ExceptionAttributes.EXCEPTION_STACKTRACE))
                .isEqualTo(DEFAULT_SHORT);

        assertThat(span.getAttributes().get(ExceptionAttributes.EXCEPTION_TYPE)).isEqualTo("type");
        assertThat(span.getAttributes().get(ExceptionAttributes.EXCEPTION_MESSAGE)).isEqualTo("message");
    }
}
