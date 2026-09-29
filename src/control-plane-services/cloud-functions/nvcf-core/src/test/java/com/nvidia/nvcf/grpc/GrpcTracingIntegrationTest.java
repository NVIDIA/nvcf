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

import static org.assertj.core.api.Assertions.assertThat;
import static org.awaitility.Awaitility.await;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import io.grpc.ManagedChannelBuilder;
import io.grpc.health.v1.HealthCheckRequest;
import io.grpc.health.v1.HealthGrpc;
import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.sdk.trace.data.SpanData;
import java.time.Duration;
import java.util.List;
import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@SpringBootTest(
        classes = {
                NvcfTestApp.class,
                IntegrationTestConfiguration.class,
                GrpcTracingTestConfiguration.class
        },
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "management.tracing.enabled=true"
        })
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class GrpcTracingIntegrationTest {

    private static final AttributeKey<String> RPC_METHOD = AttributeKey.stringKey("rpc.method");
    private static final AttributeKey<String> RPC_SERVICE = AttributeKey.stringKey("rpc.service");
    private static final AttributeKey<String> GRPC_STATUS_CODE =
            AttributeKey.stringKey("grpc.status_code");

    @BeforeEach
    void resetSpanExporter() {
        GrpcTracingTestConfiguration.SPAN_EXPORTER.reset();
    }

    @Test
    @DisplayName("gRPC endpoint produces server trace span")
    void grpcEndpointProducesTrace() {
        var channel = ManagedChannelBuilder
                .forAddress("localhost", GrpcTracingTestConfiguration.grpcPort())
                .usePlaintext()
                .build();
        try {
            HealthGrpc.newBlockingStub(channel)
                    .withDeadlineAfter(5, TimeUnit.SECONDS)
                    .check(HealthCheckRequest.newBuilder().build());
        } finally {
            channel.shutdownNow();
        }

        List<SpanData> spans = await()
                .atMost(Duration.ofSeconds(5))
                .until(
                        GrpcTracingTestConfiguration.SPAN_EXPORTER::getFinishedSpanItems,
                        items -> !items.isEmpty());

        assertThat(spans)
                .anyMatch(span -> span.getKind() == SpanKind.SERVER
                        && "grpc.health.v1.Health".equals(
                                span.getAttributes().get(RPC_SERVICE))
                        && "Check".equals(span.getAttributes().get(RPC_METHOD))
                        && "OK".equals(span.getAttributes().get(GRPC_STATUS_CODE)));
    }
}
