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

import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import ch.qos.logback.classic.spi.ILoggingEvent;
import com.fasterxml.jackson.core.JsonGenerator;
import java.io.IOException;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class DatadogTraceIdJsonProviderTest {

    private DatadogTraceIdJsonProvider provider;
    private JsonGenerator generator;
    private ILoggingEvent event;

    @BeforeEach
    void setUp() {
        provider = new DatadogTraceIdJsonProvider();
        generator = mock(JsonGenerator.class);
        event = mock(ILoggingEvent.class);
    }

    @Test
    void writesDecimalTraceIdAndSpanId() throws IOException {
        when(event.getMDCPropertyMap()).thenReturn(Map.of(
                "traceId", "0af7651916cd43dd8448eb211c80319c",
                "spanId", "b7ad6b7169203331"
        ));

        provider.writeTo(generator, event);

        verify(generator).writeStringField("dd.trace_id",
                Long.toUnsignedString(Long.parseUnsignedLong("8448eb211c80319c", 16)));
        verify(generator).writeStringField("dd.span_id",
                Long.toUnsignedString(Long.parseUnsignedLong("b7ad6b7169203331", 16)));
    }

    @Test
    void handlesMaxValueIds() throws IOException {
        when(event.getMDCPropertyMap()).thenReturn(Map.of(
                "traceId", "ffffffffffffffffffffffffffffffff",
                "spanId", "ffffffffffffffff"
        ));

        provider.writeTo(generator, event);

        verify(generator).writeStringField("dd.trace_id",
                Long.toUnsignedString(Long.parseUnsignedLong("ffffffffffffffff", 16)));
        verify(generator).writeStringField("dd.span_id",
                Long.toUnsignedString(Long.parseUnsignedLong("ffffffffffffffff", 16)));
    }

    @Test
    void handlesAllZeroIds() throws IOException {
        when(event.getMDCPropertyMap()).thenReturn(Map.of(
                "traceId", "00000000000000000000000000000000",
                "spanId", "0000000000000000"
        ));

        provider.writeTo(generator, event);

        verify(generator).writeStringField("dd.trace_id", "0");
        verify(generator).writeStringField("dd.span_id", "0");
    }

    @Test
    void skipsWhenMdcIsEmpty() throws IOException {
        when(event.getMDCPropertyMap()).thenReturn(Map.of());

        provider.writeTo(generator, event);

        verify(generator, never()).writeStringField(anyString(), anyString());
    }

    @Test
    void skipsTraceIdWithWrongLength() throws IOException {
        when(event.getMDCPropertyMap()).thenReturn(Map.of(
                "traceId", "too-short",
                "spanId", "b7ad6b7169203331"
        ));

        provider.writeTo(generator, event);

        verify(generator, never()).writeStringField(eq("dd.trace_id"), anyString());
        verify(generator).writeStringField("dd.span_id",
                Long.toUnsignedString(Long.parseUnsignedLong("b7ad6b7169203331", 16)));
    }

    @Test
    void skipsSpanIdWithWrongLength() throws IOException {
        when(event.getMDCPropertyMap()).thenReturn(Map.of(
                "traceId", "0af7651916cd43dd8448eb211c80319c",
                "spanId", "short"
        ));

        provider.writeTo(generator, event);

        verify(generator).writeStringField("dd.trace_id",
                Long.toUnsignedString(Long.parseUnsignedLong("8448eb211c80319c", 16)));
        verify(generator, never()).writeStringField(eq("dd.span_id"), anyString());
    }

    @Test
    void handlesInvalidHexGracefully() throws IOException {
        when(event.getMDCPropertyMap()).thenReturn(Map.of(
                "traceId", "0af7651916cd43ddZZZZZZZZZZZZZZZZ",
                "spanId", "ZZZZZZZZZZZZZZZZ"
        ));

        provider.writeTo(generator, event);

        verify(generator, never()).writeStringField(anyString(), anyString());
    }
}
