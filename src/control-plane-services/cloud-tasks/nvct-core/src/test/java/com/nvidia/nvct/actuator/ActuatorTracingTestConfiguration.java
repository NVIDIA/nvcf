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
package com.nvidia.nvct.actuator;

import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.trace.SpanProcessor;
import io.opentelemetry.sdk.trace.export.SimpleSpanProcessor;
import io.opentelemetry.sdk.trace.export.SpanExporter;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;

/**
 * Test configuration that provides an in-memory span exporter for asserting
 * that actuator endpoints produce traces when using a separate management port.
 *
 * <p>Uses {@link SimpleSpanProcessor} so spans are exported immediately rather than
 * batched, avoiding timing-related test failures with {@link io.opentelemetry.sdk.trace.export.BatchSpanProcessor}.
 */
@Configuration
public class ActuatorTracingTestConfiguration {

    public static final InMemorySpanExporter SPAN_EXPORTER = InMemorySpanExporter.create();

    @Bean
    @Primary
    public SpanExporter inMemorySpanExporter() {
        return SPAN_EXPORTER;
    }

    /**
     * Ensures spans are exported immediately to the in-memory exporter for reliable test assertions.
     */
    @Bean
    public SpanProcessor testSpanProcessor() {
        return SimpleSpanProcessor.create(SPAN_EXPORTER);
    }
}
