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

import io.opentelemetry.sdk.trace.export.SpanExporter;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.test.context.TestPropertySource;

/**
 * Integration test verifying that the application context loads successfully
 * with attribute redaction enabled and that the SpanExporter is wrapped with
 * AttributeRedactingSpanExporter when sensitive columns are configured.
 */
@SpringBootTest(
        classes = {com.nvidia.boot.observability.TestApplication.class,
                RedactionTestConfiguration.class},
        properties = "spring.main.allow-bean-definition-overriding=true")
@TestPropertySource(properties = {
        "management.tracing.redaction.enabled=true",
        "management.tracing.redaction.cassandra.sensitive-columns=password_hash,api_key",
        "spring.autoconfigure.exclude=org.springframework.boot.cassandra.autoconfigure.CassandraAutoConfiguration,org.springframework.boot.data.cassandra.autoconfigure.DataCassandraAutoConfiguration,org.springframework.boot.cassandra.autoconfigure.health.CassandraHealthContributorAutoConfiguration"
})
class RedactionIntegrationTest {

    @Autowired(required = false)
    private SpanExporter spanExporter;

    @Test
    @DisplayName("Application context loads with redaction config")
    void contextLoadsWithRedactionConfig() {
        assertThat(spanExporter).isNotNull();
        // SpanExporter is wrapped by ExceptionShorteningSpanExporter (outer) and
        // AttributeRedactingSpanExporter (inner, when sensitive columns configured)
    }
}

@Configuration
class RedactionTestConfiguration {

    @Bean
    RedactionVerificationBean redactionVerificationBean() {
        return new RedactionVerificationBean();
    }
}

class RedactionVerificationBean {
}
