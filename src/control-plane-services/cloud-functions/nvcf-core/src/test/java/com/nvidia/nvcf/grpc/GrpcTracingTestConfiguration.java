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
package com.nvidia.nvcf.grpc;

import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.trace.SpanProcessor;
import io.opentelemetry.sdk.trace.export.SimpleSpanProcessor;
import io.opentelemetry.sdk.trace.export.SpanExporter;
import java.util.concurrent.atomic.AtomicInteger;
import net.devh.boot.grpc.server.event.GrpcServerStartedEvent;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Primary;
import org.springframework.context.event.EventListener;

@TestConfiguration(proxyBeanMethods = false)
public class GrpcTracingTestConfiguration {

    static final InMemorySpanExporter SPAN_EXPORTER = InMemorySpanExporter.create();
    private static final AtomicInteger GRPC_PORT = new AtomicInteger();

    static int grpcPort() {
        return GRPC_PORT.get();
    }

    @EventListener
    public void captureGrpcPort(GrpcServerStartedEvent event) {
        GRPC_PORT.set(event.getServer().getPort());
    }

    @Bean
    @Primary
    public SpanExporter inMemorySpanExporter() {
        return SPAN_EXPORTER;
    }

    @Bean
    public SpanProcessor testSpanProcessor() {
        return SimpleSpanProcessor.create(SPAN_EXPORTER);
    }
}
