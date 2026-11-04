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

package com.nvidia.boot.observability.tracing.cassandra;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.observability.TestApplication;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.cassandra.autoconfigure.CqlSessionBuilderCustomizer;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.TestPropertySource;

/**
 * Integration test verifying that CassandraTracingAutoConfiguration contributes
 * a CqlSessionBuilderCustomizer bean when Spring Data Cassandra is on the classpath.
 *
 * <p>Cassandra connection auto-configurations are excluded to avoid requiring a
 * real cluster; the test only verifies that our customizer bean is registered.
 *
 * <p>Coverage for the observableCqlSession bean (session wrapping) is in
 * {@link CassandraTracingAutoConfigurationTest#wrapsCqlSessionWithObservabilityByDefault()}.
 */
@SpringBootTest(classes = TestApplication.class)
@TestPropertySource(properties = {
        "spring.autoconfigure.exclude="
                + "org.springframework.boot.cassandra.autoconfigure.CassandraAutoConfiguration,"
                + "org.springframework.boot.data.cassandra.autoconfigure.DataCassandraAutoConfiguration,"
                + "org.springframework.boot.cassandra.autoconfigure.health.CassandraHealthContributorAutoConfiguration"
})
class CassandraTracingIntegrationTest {

    @Autowired(required = false)
    private CqlSessionBuilderCustomizer cassandraObservationRequestTrackerCustomizer;

    @Test
    @DisplayName("Application context loads with Cassandra tracing customizer when Spring Data Cassandra on classpath")
    void contextLoadsWithCassandraTracingCustomizer() {
        assertThat(cassandraObservationRequestTrackerCustomizer).isNotNull();
    }
}
