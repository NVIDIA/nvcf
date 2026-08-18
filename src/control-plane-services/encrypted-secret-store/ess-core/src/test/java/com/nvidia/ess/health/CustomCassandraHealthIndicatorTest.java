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
package com.nvidia.ess.health;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.cql.SimpleStatement;
import com.nvidia.ess.telemetry.TelemetryComponents;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.boot.health.contributor.Status;
import org.springframework.data.cassandra.core.ReactiveCassandraTemplate;
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.context.Context;

@ExtendWith(MockitoExtension.class)
class CustomCassandraHealthIndicatorTest {

    @Mock
    private ReactiveCassandraTemplate reactiveCassandraTemplate;

    @Mock
    private TelemetryComponents telemetryComponents;

    @Mock
    private ServerWebExchange exchange;

    private CustomCassandraHealthIndicator healthIndicator;

    @BeforeEach
    void setUp() {
        healthIndicator = new CustomCassandraHealthIndicator(reactiveCassandraTemplate, telemetryComponents);
    }

    @Test
    @SuppressWarnings("unchecked")
    void doHealthCheck_cassandraResponds_returnsHealthUp() {
        when(reactiveCassandraTemplate.selectOne(any(SimpleStatement.class), any(Class.class)))
                .thenReturn(Mono.just(Map.of("current_time", "2026-03-30T00:00:00Z")));

        StepVerifier.create(healthIndicator.health()
                        .contextWrite(Context.of(ServerWebExchange.class, exchange)))
                .assertNext(health -> assertEquals(Status.UP, health.getStatus()))
                .verifyComplete();

        verifyNoInteractions(telemetryComponents);
    }

    @Test
    @SuppressWarnings("unchecked")
    void doHealthCheck_cassandraReturnsEmpty_completesWithoutError() {
        when(reactiveCassandraTemplate.selectOne(any(SimpleStatement.class), any(Class.class)))
                .thenReturn(Mono.empty());

        StepVerifier.create(healthIndicator.health()
                        .contextWrite(Context.of(ServerWebExchange.class, exchange)))
                .verifyComplete();

        verifyNoInteractions(telemetryComponents);
    }

    @Test
    @SuppressWarnings("unchecked")
    void doHealthCheck_cassandraThrowsException_returnsHealthDown() {
        RuntimeException exception = new RuntimeException("connection refused");
        when(reactiveCassandraTemplate.selectOne(any(SimpleStatement.class), any(Class.class)))
                .thenReturn(Mono.error(exception));

        StepVerifier.create(healthIndicator.health()
                        .contextWrite(Context.of(ServerWebExchange.class, exchange)))
                .assertNext(health -> {
                    assertEquals(Status.DOWN, health.getStatus());
                    assertEquals(
                            exception.getClass().getName() + ": " + exception.getMessage(),
                            health.getDetails().get("error"));
                })
                .verifyComplete();
    }

    @Test
    @SuppressWarnings("unchecked")
    void doHealthCheck_cassandraThrowsException_recordsExceptionOnSpan() {
        RuntimeException exception = new RuntimeException("timeout");
        when(reactiveCassandraTemplate.selectOne(any(SimpleStatement.class), any(Class.class)))
                .thenReturn(Mono.error(exception));

        StepVerifier.create(healthIndicator.health()
                        .contextWrite(Context.of(ServerWebExchange.class, exchange)))
                .assertNext(health -> assertEquals(Status.DOWN, health.getStatus()))
                .verifyComplete();

        verify(telemetryComponents).recordExceptionWithoutErrorStatus(exchange, exception);
    }
}
