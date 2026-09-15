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

package com.nvidia.boot.observability.tracing.redaction;

import static org.assertj.core.api.Assertions.assertThat;

import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.testing.trace.TestSpanData;
import io.opentelemetry.sdk.trace.data.StatusData;
import java.util.Set;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;

class AttributeRedactingSpanExporterTest {

    private InMemorySpanExporter delegate;

    @BeforeEach
    void setUp() {
        delegate = InMemorySpanExporter.create();
    }

    @Test
    @DisplayName("Redacts sensitive column values in Cassandra db.query.text")
    void redactsCassandraQueryText() {
        var exporter = new AttributeRedactingSpanExporter(delegate,
                Set.of("password_hash", "api_key"));

        var span = TestSpanData.builder()
                .setName("cassandra.query")
                .setKind(SpanKind.CLIENT)
                .setStartEpochNanos(1000)
                .setEndEpochNanos(2000)
                .setStatus(StatusData.ok())
                .setHasEnded(true)
                .setAttributes(Attributes.builder()
                        .put("db.system", "cassandra")
                        .put("db.query.text", "SELECT * FROM users WHERE password_hash = 'secret123' AND api_key = 'key456'")
                        .build())
                .build();

        exporter.export(java.util.List.of(span));

        var exported = delegate.getFinishedSpanItems();
        assertThat(exported).hasSize(1);
        var attrs = exported.get(0).getAttributes();
        assertThat(attrs.get(AttributeKey.stringKey("db.query.text")))
                .isEqualTo("SELECT * FROM users WHERE password_hash = ? AND api_key = ?");
    }

    @Test
    @DisplayName("Redacts sensitive column values in db.query.parameter.* attributes")
    void redactsCassandraQueryParameters() {
        var exporter = new AttributeRedactingSpanExporter(delegate, Set.of("secret_data"));

        var span = TestSpanData.builder()
                .setName("cassandra.query")
                .setKind(SpanKind.CLIENT)
                .setStartEpochNanos(1000)
                .setEndEpochNanos(2000)
                .setStatus(StatusData.ok())
                .setHasEnded(true)
                .setAttributes(Attributes.builder()
                        .put("db.system", "cassandra")
                        .put("db.query.parameter.1", "secret_data = 'my-secret-value'")
                        .build())
                .build();

        exporter.export(java.util.List.of(span));

        var exported = delegate.getFinishedSpanItems();
        assertThat(exported).hasSize(1);
        assertThat(exported.get(0).getAttributes().get(AttributeKey.stringKey("db.query.parameter.1")))
                .isEqualTo("secret_data = ?");
    }

    @Test
    @DisplayName("Passes through non-Cassandra spans unchanged")
    void passesThroughNonCassandraSpans() {
        var exporter = new AttributeRedactingSpanExporter(delegate, Set.of("password_hash"));

        var span = TestSpanData.builder()
                .setName("http.get")
                .setKind(SpanKind.SERVER)
                .setStartEpochNanos(1000)
                .setEndEpochNanos(2000)
                .setStatus(StatusData.ok())
                .setHasEnded(true)
                .setAttributes(Attributes.builder()
                        .put("http.request.method", "GET")
                        .put("url.path", "/api/users")
                        .build())
                .build();

        exporter.export(java.util.List.of(span));

        var exported = delegate.getFinishedSpanItems();
        assertThat(exported).hasSize(1);
        assertThat(exported.get(0).getAttributes().get(AttributeKey.stringKey("url.path")))
                .isEqualTo("/api/users");
    }

    @Test
    @DisplayName("Passes through Cassandra spans unchanged when no sensitive columns configured")
    void passesThroughWhenNoSensitiveColumns() {
        var exporter = new AttributeRedactingSpanExporter(delegate, Set.of());

        var span = TestSpanData.builder()
                .setName("cassandra.query")
                .setKind(SpanKind.CLIENT)
                .setStartEpochNanos(1000)
                .setEndEpochNanos(2000)
                .setStatus(StatusData.ok())
                .setHasEnded(true)
                .setAttributes(Attributes.builder()
                        .put("db.system", "cassandra")
                        .put("db.query.text", "SELECT * FROM users WHERE id = 'u1'")
                        .build())
                .build();

        exporter.export(java.util.List.of(span));

        var exported = delegate.getFinishedSpanItems();
        assertThat(exported).hasSize(1);
        assertThat(exported.get(0).getAttributes().get(AttributeKey.stringKey("db.query.text")))
                .isEqualTo("SELECT * FROM users WHERE id = 'u1'");
    }

    @Test
    @DisplayName("Redaction is case-insensitive for column names")
    void redactionCaseInsensitive() {
        var exporter = new AttributeRedactingSpanExporter(delegate, Set.of("PASSWORD_HASH"));

        var span = TestSpanData.builder()
                .setName("cassandra.query")
                .setKind(SpanKind.CLIENT)
                .setStartEpochNanos(1000)
                .setEndEpochNanos(2000)
                .setStatus(StatusData.ok())
                .setHasEnded(true)
                .setAttributes(Attributes.builder()
                        .put("db.system", "cassandra")
                        .put("db.query.text", "SELECT * FROM users WHERE password_hash = 'secret'")
                        .build())
                .build();

        exporter.export(java.util.List.of(span));

        var exported = delegate.getFinishedSpanItems();
        assertThat(exported).hasSize(1);
        assertThat(exported.get(0).getAttributes().get(AttributeKey.stringKey("db.query.text")))
                .isEqualTo("SELECT * FROM users WHERE password_hash = ?");
    }
}
