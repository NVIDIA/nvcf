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

package com.nvidia.boot.cassandra.configuration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import java.util.Collections;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.context.annotation.ConditionContext;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.type.AnnotatedTypeMetadata;
import org.springframework.mock.env.MockEnvironment;

class CassandraMetricsEnabledConditionTest {

    private final CassandraMetricsConfiguration.CassandraMetricsEnabledCondition condition =
            new CassandraMetricsConfiguration.CassandraMetricsEnabledCondition();

    @Test
    void matchesFalseWhenNoMetricsProperties() {
        var env = new MockEnvironment();
        assertThat(matches(env)).isFalse();
    }

    @Test
    void matchesFalseWhenOnlyUnrelatedCassandraProperty() {
        var env = new MockEnvironment();
        env.setProperty("spring.cassandra.port", "9042");
        assertThat(matches(env)).isFalse();
    }

    @Test
    void matchesTrueWhenSessionMetricsIndexed() {
        var env = new MockEnvironment();
        env.setProperty("spring.cassandra.session-metrics[0]", "connected-nodes");
        assertThat(matches(env)).isTrue();
    }

    @Test
    void matchesTrueWhenNodeMetricsIndexed() {
        var env = new MockEnvironment();
        env.setProperty("spring.cassandra.node-metrics[0]", "pool.in-flight");
        assertThat(matches(env)).isTrue();
    }

    @Test
    void matchesTrueWhenBothListsHaveEntries() {
        var env = new MockEnvironment();
        env.setProperty("spring.cassandra.session-metrics[0]", "bytes-sent");
        env.setProperty("spring.cassandra.node-metrics[0]", "pool.open-connections");
        assertThat(matches(env)).isTrue();
    }

    @Test
    void matchesFalseWhenSessionMetricsExplicitlyEmpty() {
        var env = new MockEnvironment();
        env.getPropertySources()
                .addFirst(new MapPropertySource(
                        "test",
                        Map.of("spring.cassandra.session-metrics", Collections.<String>emptyList())));
        assertThat(matches(env)).isFalse();
    }

    @Test
    void matchesFalseWhenBothListsExplicitlyEmpty() {
        var env = new MockEnvironment();
        env.getPropertySources()
                .addFirst(new MapPropertySource(
                        "test",
                        Map.of(
                                "spring.cassandra.session-metrics", List.<String>of(),
                                "spring.cassandra.node-metrics", List.<String>of())));
        assertThat(matches(env)).isFalse();
    }

    @Test
    void matchesTrueWhenSessionNonEmptyAndNodeExplicitlyEmpty() {
        var env = new MockEnvironment();
        env.getPropertySources()
                .addFirst(new MapPropertySource(
                        "empty-node",
                        Map.of("spring.cassandra.node-metrics", List.<String>of())));
        env.setProperty("spring.cassandra.session-metrics[0]", "cql-requests");
        assertThat(matches(env)).isTrue();
    }

    private boolean matches(org.springframework.core.env.Environment env) {
        var ctx = mock(ConditionContext.class);
        when(ctx.getEnvironment()).thenReturn(env);
        var meta = mock(AnnotatedTypeMetadata.class);
        return condition.matches(ctx, meta);
    }
}
