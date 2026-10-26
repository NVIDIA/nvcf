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

import static com.datastax.oss.driver.api.core.config.DefaultDriverOption.RETRY_POLICY_CLASS;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.config.DriverConfig;
import com.datastax.oss.driver.api.core.config.DriverExecutionProfile;
import com.datastax.oss.driver.api.core.context.DriverContext;
import com.nvidia.boot.cassandra.retry.NextHostRetryPolicy;
import org.junit.jupiter.api.Test;
import org.springframework.boot.health.contributor.Status;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;

class CassandraSessionLivenessConfigurationTest {

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withUserConfiguration(CassandraSessionLivenessConfiguration.class)
            .withPropertyValues("app.cassandra.session-liveness.check-interval=1h");

    @Test
    void registersCassandraSessionContributor() {
        contextRunner.withBean(CqlSession.class, CassandraSessionLivenessConfigurationTest::session)
                .run(context -> {
                    // The bean name maps to the "cassandraSession" contributor in health groups.
                    assertThat(context).hasBean("cassandraSessionHealthIndicator");
                    var indicator = context.getBean(CassandraSessionLivenessIndicator.class);
                    assertThat(indicator.health().getStatus()).isEqualTo(Status.UP);
                });
    }

    @Test
    void reportsUpWithoutAUniqueSession() {
        contextRunner.run(context -> assertThat(context.getBean(CassandraSessionLivenessIndicator.class)
                                                        .health().getStatus()).isEqualTo(Status.UP));
    }

    @Test
    void canBeDisabled() {
        contextRunner.withPropertyValues("app.cassandra.session-liveness.enabled=false")
                .withBean(CqlSession.class, CassandraSessionLivenessConfigurationTest::session)
                .run(context -> assertThat(context).doesNotHaveBean(CassandraSessionLivenessIndicator.class));
    }

    private static CqlSession session() {
        var session = mock(CqlSession.class);
        var context = mock(DriverContext.class);
        var config = mock(DriverConfig.class);
        var profile = mock(DriverExecutionProfile.class);
        when(session.getContext()).thenReturn(context);
        when(context.getConfig()).thenReturn(config);
        when(config.getDefaultProfile()).thenReturn(profile);
        when(profile.withClass(eq(RETRY_POLICY_CLASS), eq(NextHostRetryPolicy.class))).thenReturn(profile);
        return session;
    }
}
