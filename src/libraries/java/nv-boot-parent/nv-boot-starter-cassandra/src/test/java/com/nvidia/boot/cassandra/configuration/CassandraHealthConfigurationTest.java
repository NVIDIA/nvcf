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
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.config.DriverConfig;
import com.datastax.oss.driver.api.core.config.DriverExecutionProfile;
import com.datastax.oss.driver.api.core.context.DriverContext;
import com.datastax.oss.driver.api.core.cql.ResultSet;
import com.datastax.oss.driver.api.core.cql.Statement;
import com.datastax.oss.driver.api.core.metadata.Metadata;
import com.nvidia.boot.cassandra.retry.NextHostRetryPolicy;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.health.contributor.CompositeHealthContributor;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthContributor;
import org.springframework.boot.health.contributor.HealthIndicator;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;

class CassandraHealthConfigurationTest {

    private ApplicationContextRunner contextRunner;

    @BeforeEach
    void setUp() {
        contextRunner = new ApplicationContextRunner()
                .withUserConfiguration(CassandraHealthConfiguration.class)
                .withBean("cassandra", CqlSession.class, this::createMockCqlSession);
    }

    private CqlSession createMockCqlSession() {
        var session = mock(CqlSession.class);
        var context = mock(DriverContext.class);
        var config = mock(DriverConfig.class);
        var defaultProfile = mock(DriverExecutionProfile.class);
        var profileWithRetry = mock(DriverExecutionProfile.class);

        var metadata = mock(Metadata.class);
        lenient().when(session.getMetadata()).thenReturn(metadata);
        lenient().when(metadata.getNodes()).thenReturn(java.util.Collections.emptyMap());
        lenient().when(session.getContext()).thenReturn(context);
        lenient().when(context.getConfig()).thenReturn(config);
        lenient().when(config.getDefaultProfile()).thenReturn(defaultProfile);
        lenient().when(defaultProfile.withClass(eq(RETRY_POLICY_CLASS), eq(NextHostRetryPolicy.class)))
                .thenReturn(profileWithRetry);

        var resultSet = mock(ResultSet.class);
        lenient().when(session.execute(any(Statement.class))).thenReturn(resultSet);

        return session;
    }

    @Test
    void cassandraHealthContributorIsCreatedWhenCqlSessionAvailable() {
        contextRunner.run(context -> {
            assertThat(context).hasSingleBean(HealthContributor.class);
            var contributor = context.getBean("cassandraHealthContributor", HealthContributor.class);
            assertThat(contributor).isNotNull();
        });
    }

    @Test
    void healthIndicatorReturnsHealth() {
        contextRunner.run(context -> {
            var contributor = context.getBean("cassandraHealthContributor", HealthContributor.class);
            assertThat(contributor).isInstanceOf(HealthIndicator.class);
            Health health = ((HealthIndicator) contributor).health();
            assertThat(health).isNotNull();
            assertThat(health.getStatus()).isNotNull();
        });
    }

    @Test
    void cassandraHealthContributorIsCompositeWithMultipleSessions() {
        new ApplicationContextRunner()
                .withUserConfiguration(CassandraHealthConfiguration.class)
                .withBean("cassandra", CqlSession.class, this::createMockCqlSession)
                .withBean("cassandraSecondary", CqlSession.class, this::createMockCqlSession)
                .run(context -> {
                    var contributor = context.getBean("cassandraHealthContributor", HealthContributor.class);
                    assertThat(contributor).isInstanceOf(CompositeHealthContributor.class);

                    var composite = (CompositeHealthContributor) contributor;
                    var names = new java.util.ArrayList<String>();
                    composite.forEach(nc -> names.add(nc.name()));
                    assertThat(names).containsExactlyInAnyOrder("cassandra", "cassandraSecondary");
                });
    }

    @Test
    void contextFailsToStartWhenNoCqlSessionsAvailable() {
        new ApplicationContextRunner()
                .withUserConfiguration(CassandraHealthConfiguration.class)
                .run(context -> assertThat(context).hasFailed());
    }
}
