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
package com.nvidia.icms.configuration;

import io.opentelemetry.exporter.otlp.trace.OtlpGrpcSpanExporter;
import io.opentelemetry.sdk.trace.export.SpanExporter;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * Registers a second OTLP gRPC {@link SpanExporter} that sends traces to the
 * Datadog agent sidecar. Activates only when {@code datadog.otlp.tracing.endpoint}
 * is set (staging / environments with a DD agent sidecar).
 *
 * <p>The primary Lightstep exporter is auto-configured by Spring Boot from
 * {@code management.otlp.tracing.*}; this bean runs alongside it so every
 * span is exported to both backends.
 */
@Configuration
@ConditionalOnProperty("icms.datadog.otlp.tracing.endpoint")
public class DatadogOtlpExporterConfiguration {

    @Bean
    public SpanExporter datadogOtlpGrpcSpanExporter(
            @Value("${icms.datadog.otlp.tracing.endpoint}") String endpoint) {
        return OtlpGrpcSpanExporter.builder()
                .setEndpoint(endpoint)
                .build();
    }
}
