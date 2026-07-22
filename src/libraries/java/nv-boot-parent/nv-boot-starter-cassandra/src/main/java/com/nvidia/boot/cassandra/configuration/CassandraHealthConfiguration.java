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

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.cql.SimpleStatement;
import com.nvidia.boot.cassandra.retry.NextHostRetryPolicy;
import java.util.Map;
import org.springframework.boot.health.autoconfigure.contributor.CompositeHealthContributorConfiguration;
import org.springframework.boot.cassandra.health.CassandraDriverHealthIndicator;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthContributor;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class CassandraHealthConfiguration extends
        CompositeHealthContributorConfiguration<CassandraDriverHealthIndicator, CqlSession> {

    CassandraHealthConfiguration() {
        super(ActiveCassandraDriverHealthIndicator::new);
    }

    private static class ActiveCassandraDriverHealthIndicator extends
            CassandraDriverHealthIndicator {

        private final CqlSession session;
        private final SimpleStatement healthQuery;

        public ActiveCassandraDriverHealthIndicator(CqlSession session) {
            super(session);
            this.session = session;
            var defaultProfile = session.getContext().getConfig().getDefaultProfile();
            var executionProfile = defaultProfile.withClass(RETRY_POLICY_CLASS,
                                                            NextHostRetryPolicy.class);
            this.healthQuery = SimpleStatement.builder("SELECT release_version FROM system.local")
                    .setExecutionProfile(executionProfile)
                    .build();
        }

        @Override
        protected void doHealthCheck(Health.Builder builder) throws Exception {
            session.execute(healthQuery);
            super.doHealthCheck(builder);
        }
    }

    @Bean
    HealthContributor cassandraHealthContributor(Map<String, CqlSession> sessions) {
        return createContributor(sessions);
    }
}
