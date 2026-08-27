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
package com.nvidia.nvcf.persistence.telemetry.entity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import java.time.Instant;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class TelemetryByAccountEntityTest {

    @Test
    void shouldBuildTelemetryByAccountEntitySuccessfully() {
        // Arrange
        var key = TelemetryByAccountKey.builder()
                .ncaId("test-nca-id")
                .telemetryId(UUID.randomUUID())
                .build();
        var name = "Test Telemetry";
        var endpoint = "http://example.com";
        var protocol = TelemetryProtocol.HTTP;
        var provider = TelemetryProvider.DATADOG;
        var types = Set.of(TelemetryType.LOGS, TelemetryType.METRICS);
        var createdAt = Instant.now();

        // Act
        var entity = TelemetryByAccountEntity.builder()
                .key(key)
                .name(name)
                .endpoint(endpoint)
                .protocol(protocol)
                .provider(provider)
                .types(types)
                .createdAt(createdAt)
                .build();

        // Assert
        assertNotNull(entity);
        assertEquals(key, entity.getKey());
        assertEquals(name, entity.getName());
        assertEquals(endpoint, entity.getEndpoint());
        assertEquals(protocol, entity.getProtocol());
        assertEquals(provider, entity.getProvider());
        assertEquals(types, entity.getTypes());
        assertEquals(createdAt, entity.getCreatedAt());
    }

    @ParameterizedTest
    @MethodSource("provideInvalidTelemetryData")
    void shouldThrowExceptionForInvalidTelemetryData(
            TelemetryByAccountKey key,
            String name,
            String endpoint,
            TelemetryProtocol protocol,
            TelemetryProvider provider,
            Set<TelemetryType> types,
            String expectedMessage) {

        var exception = assertThrows(IllegalArgumentException.class, () ->
            TelemetryByAccountEntity.builder()
                    .key(key)
                    .name(name)
                    .endpoint(endpoint)
                    .protocol(protocol)
                    .provider(provider)
                    .types(types)
                    .build());

        assertTrue(exception.getMessage().contains(expectedMessage));
    }

    private static Stream<Arguments> provideInvalidTelemetryData() {
        return Stream.of(
            Arguments.of(
                null,
                "Test Name",
                "http://example.com",
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                "'key' is required"
            ),
            Arguments.of(
                TelemetryByAccountKey.builder()
                        .ncaId("test-nca-id")
                        .telemetryId(UUID.randomUUID())
                        .build(),
                null,
                "http://example.com",
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                "'name' is required"
            ),
            Arguments.of(
                TelemetryByAccountKey.builder()
                        .ncaId("test-nca-id")
                        .telemetryId(UUID.randomUUID())
                        .build(),
                "Test Name",
                null,
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                "'endpoint' is required"
            ),
            Arguments.of(
                TelemetryByAccountKey.builder()
                        .ncaId("test-nca-id")
                        .telemetryId(UUID.randomUUID())
                        .build(),
                "Test Name",
                "http://example.com",
                null,
                TelemetryProvider.DATADOG,
                Set.of(TelemetryType.LOGS),
                "'protocol' is required"
            ),
            Arguments.of(
                TelemetryByAccountKey.builder()
                        .ncaId("test-nca-id")
                        .telemetryId(UUID.randomUUID())
                        .build(),
                "Test Name",
                "http://example.com",
                TelemetryProtocol.HTTP,
                null,
                Set.of(TelemetryType.LOGS),
                "'provider' is required"
            ),
            Arguments.of(
                TelemetryByAccountKey.builder()
                        .ncaId("test-nca-id")
                        .telemetryId(UUID.randomUUID())
                        .build(),
                "Test Name",
                "http://example.com",
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                null,
                "'types' is required"
            ),
            Arguments.of(
                TelemetryByAccountKey.builder()
                        .ncaId("test-nca-id")
                        .telemetryId(UUID.randomUUID())
                        .build(),
                "Test Name",
                "http://example.com",
                TelemetryProtocol.HTTP,
                TelemetryProvider.DATADOG,
                Set.of(),
                "'types' is required"
            )
        );
    }
}