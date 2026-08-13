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
package com.nvidia.ess.config;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatNullPointerException;

import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import io.micrometer.observation.Observation;
import io.micrometer.observation.ObservationHandler;
import io.micrometer.tracing.Tracer;
import io.micrometer.tracing.handler.DefaultTracingObservationHandler;
import org.junit.jupiter.api.Test;

class ObservabilityConfigTest {

    private final ObservabilityConfig config = new ObservabilityConfig();

    @Test
    void defaultTracingObservationHandler_shouldReturnHandler() {
        DefaultTracingObservationHandler handler =
                config.defaultTracingObservationHandler(Tracer.NOOP);

        assertThat(handler).isNotNull();
    }

    @Test
    void getSpanName_shouldReturnContextualNameWhenPresent() {
        DefaultTracingObservationHandler handler =
                config.defaultTracingObservationHandler(Tracer.NOOP);
        Observation.Context context = new Observation.Context();
        context.setName("original-name");
        context.setContextualName("Contextual-Name");

        String spanName = handler.getSpanName(context);

        assertThat(spanName).isEqualTo("Contextual-Name");
    }

    @Test
    void getSpanName_shouldReturnNameWhenContextualNameIsNull() {
        DefaultTracingObservationHandler handler =
                config.defaultTracingObservationHandler(Tracer.NOOP);
        Observation.Context context = new Observation.Context();
        context.setName("Original-Name");

        String spanName = handler.getSpanName(context);

        assertThat(spanName).isEqualTo("Original-Name");
    }

    @Test
    void getSpanName_shouldReturnNameWhenContextualNameIsBlank() {
        DefaultTracingObservationHandler handler =
                config.defaultTracingObservationHandler(Tracer.NOOP);
        Observation.Context context = new Observation.Context();
        context.setName("Original-Name");
        context.setContextualName("   ");

        String spanName = handler.getSpanName(context);

        assertThat(spanName).isEqualTo("Original-Name");
    }

    @Test
    void getSpanName_shouldPreserveCasing() {
        DefaultTracingObservationHandler handler =
                config.defaultTracingObservationHandler(Tracer.NOOP);
        Observation.Context context = new Observation.Context();
        context.setName("HTTP GET /api/v1/Users");

        String spanName = handler.getSpanName(context);

        assertThat(spanName).isEqualTo("HTTP GET /api/v1/Users");
    }

    @Test
    void getSpanName_shouldThrowWhenBothNamesAreNull() {
        DefaultTracingObservationHandler handler =
                config.defaultTracingObservationHandler(Tracer.NOOP);
        Observation.Context context = new Observation.Context();

        assertThatNullPointerException().isThrownBy(() -> handler.getSpanName(context));
    }

    @Test
    void threadAttributeObservationHandler_shouldReturnHandler() {
        ObservationHandler<Observation.Context> handler =
                config.threadAttributeObservationHandler();

        assertThat(handler).isNotNull();
    }

    @Test
    void threadAttributeObservationHandler_supportsContext_shouldReturnTrue() {
        ObservationHandler<Observation.Context> handler =
                config.threadAttributeObservationHandler();

        assertThat(handler.supportsContext(new Observation.Context())).isTrue();
    }

    @Test
    void threadAttributeObservationHandler_onStart_shouldAddThreadId() {
        ObservationHandler<Observation.Context> handler =
                config.threadAttributeObservationHandler();
        Observation.Context context = new Observation.Context();

        handler.onStart(context);

        KeyValues keyValues = context.getHighCardinalityKeyValues();
        assertThat(keyValues)
                .extracting(KeyValue::getKey)
                .contains("thread.id");
        assertThat(keyValues)
                .filteredOn(kv -> "thread.id".equals(kv.getKey()))
                .first()
                .extracting(KeyValue::getValue)
                .isEqualTo(String.valueOf(Thread.currentThread().threadId()));
    }

    @Test
    void threadAttributeObservationHandler_onStart_shouldAddThreadName() {
        ObservationHandler<Observation.Context> handler =
                config.threadAttributeObservationHandler();
        Observation.Context context = new Observation.Context();

        handler.onStart(context);

        KeyValues keyValues = context.getHighCardinalityKeyValues();
        assertThat(keyValues)
                .extracting(KeyValue::getKey)
                .contains("thread.name");
        assertThat(keyValues)
                .filteredOn(kv -> "thread.name".equals(kv.getKey()))
                .first()
                .extracting(KeyValue::getValue)
                .isEqualTo(Thread.currentThread().getName());
    }
}
