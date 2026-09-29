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
package com.nvidia.ess.metrics;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.ConsistencyLevel;
import com.datastax.oss.driver.api.core.config.DefaultDriverOption;
import com.datastax.oss.driver.api.core.config.DriverConfig;
import com.datastax.oss.driver.api.core.config.DriverExecutionProfile;
import com.datastax.oss.driver.api.core.cql.Statement;
import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import java.util.Collections;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Nested;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.data.cassandra.observability.CassandraObservationContext;

@ExtendWith(MockitoExtension.class)
class CustomCassandraObservationConventionTest {

    private static final String CONSISTENCY_KEY = "db.cassandra.consistency_level";

    @Mock
    private DriverConfig driverConfig;

    @Mock
    private DriverExecutionProfile defaultProfile;

    @Mock
    private Statement<?> statement;

    private CustomCassandraObservationConvention convention;

    @BeforeEach
    void setUp() {
        convention = new CustomCassandraObservationConvention(driverConfig);
    }

    private CassandraObservationContext contextFor(boolean isPrepare) {
        return new CassandraObservationContext(
                statement, "SELECT 1", isPrepare, "testMethod", "s0", "ess");
    }

    @Nested
    class WhenPrepareStatement {

        @Test
        void doesNotAddConsistencyLevel() {
            CassandraObservationContext context = contextFor(true);

            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            assertThat(kv.stream().filter(k -> k.getKey().equals(CONSISTENCY_KEY)))
                    .as("prepare statements should not get a custom consistency_level tag")
                    .isEmpty();
        }
    }

    @Nested
    class WhenConsistencyLevelAlreadySet {

        @Test
        void preservesStatementLevelConsistency() {
            when(statement.getConsistencyLevel()).thenReturn(ConsistencyLevel.QUORUM);
            CassandraObservationContext context = contextFor(false);

            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            assertThat(kv)
                    .as("should keep the statement-level consistency emitted by super")
                    .contains(KeyValue.of(CONSISTENCY_KEY, "QUORUM"));
        }

        @Test
        void doesNotInvokeResolverNorDoubleEmit() {
            // driverConfig is intentionally not stubbed. If the resolver runs, it will
            // NPE on the null profile returned from getDefaultProfile(), failing the test.
            when(statement.getConsistencyLevel()).thenReturn(ConsistencyLevel.QUORUM);
            CassandraObservationContext context = contextFor(false);

            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            assertThat(kv.stream().filter(k -> k.getKey().equals(CONSISTENCY_KEY)).count())
                    .as("consistency key should be emitted exactly once")
                    .isEqualTo(1L);
        }
    }

    @Nested
    class WhenConsistencyLevelIsNull {

        @BeforeEach
        void setUpNullConsistency() {
            when(statement.getConsistencyLevel()).thenReturn(null);
        }

        @Test
        void resolvesFromInlineExecutionProfile() {
            DriverExecutionProfile inlineProfile = mock(DriverExecutionProfile.class);
            stubConsistency(inlineProfile, "each_quorum");
            when(statement.getExecutionProfile()).thenReturn(inlineProfile);

            CassandraObservationContext context = contextFor(false);
            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            assertThat(kv).contains(KeyValue.of(CONSISTENCY_KEY, "EACH_QUORUM"));
        }

        @Test
        void resolvesFromNamedProfile() {
            when(statement.getExecutionProfile()).thenReturn(null);
            when(statement.getExecutionProfileName()).thenReturn("olap");

            DriverExecutionProfile namedProfile = mock(DriverExecutionProfile.class);
            stubConsistency(namedProfile, "local_quorum");
            doReturn(Map.of("olap", namedProfile)).when(driverConfig).getProfiles();
            when(driverConfig.getProfile("olap")).thenReturn(namedProfile);

            CassandraObservationContext context = contextFor(false);
            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            assertThat(kv).contains(KeyValue.of(CONSISTENCY_KEY, "LOCAL_QUORUM"));
        }

        @Test
        void fallsBackToDefaultProfileWhenNamedProfileNotFound() {
            when(statement.getExecutionProfile()).thenReturn(null);
            when(statement.getExecutionProfileName()).thenReturn("nonexistent");
            doReturn(Collections.emptyMap()).when(driverConfig).getProfiles();
            when(driverConfig.getDefaultProfile()).thenReturn(defaultProfile);
            stubConsistency(defaultProfile, "local_one");

            CassandraObservationContext context = contextFor(false);
            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            assertThat(kv).contains(KeyValue.of(CONSISTENCY_KEY, "LOCAL_ONE"));
        }

        @Test
        void fallsBackToDefaultProfileWhenNoProfileNameSet() {
            when(statement.getExecutionProfile()).thenReturn(null);
            when(statement.getExecutionProfileName()).thenReturn(null);
            when(driverConfig.getDefaultProfile()).thenReturn(defaultProfile);
            stubConsistency(defaultProfile, "one");

            CassandraObservationContext context = contextFor(false);
            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            assertThat(kv).contains(KeyValue.of(CONSISTENCY_KEY, "ONE"));
        }

        @Test
        void uppercasesTheResolvedConsistencyLevel() {
            when(statement.getExecutionProfile()).thenReturn(null);
            when(statement.getExecutionProfileName()).thenReturn(null);
            when(driverConfig.getDefaultProfile()).thenReturn(defaultProfile);
            stubConsistency(defaultProfile, "local_quorum");

            CassandraObservationContext context = contextFor(false);
            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            KeyValue clTag = kv.stream()
                    .filter(k -> k.getKey().equals(CONSISTENCY_KEY))
                    .findFirst()
                    .orElseThrow();
            assertThat(clTag.getValue())
                    .isEqualTo("LOCAL_QUORUM")
                    .isUpperCase();
        }

        @Test
        void emitsNoneWhenProfileDoesNotDefineConsistency() {
            // Simulate a profile where REQUEST_CONSISTENCY is absent. The real two-arg
            // getString(option, defaultValue) default implementation checks
            // isDefined(option) and returns defaultValue when false. We let the real
            // default method run and stub isDefined() to false so the fallback
            // KeyValue.NONE_VALUE ("none") is returned.
            when(statement.getExecutionProfile()).thenReturn(null);
            when(statement.getExecutionProfileName()).thenReturn(null);
            when(driverConfig.getDefaultProfile()).thenReturn(defaultProfile);
            when(defaultProfile.isDefined(DefaultDriverOption.REQUEST_CONSISTENCY))
                    .thenReturn(false);
            when(defaultProfile.getString(DefaultDriverOption.REQUEST_CONSISTENCY, KeyValue.NONE_VALUE))
                    .thenCallRealMethod();

            CassandraObservationContext context = contextFor(false);
            KeyValues kv = convention.getHighCardinalityKeyValues(context);

            assertThat(kv)
                    .as("undefined consistency should fall back to NONE rather than NPE")
                    .contains(KeyValue.of(CONSISTENCY_KEY, "NONE"));
        }

        private void stubConsistency(DriverExecutionProfile profile, String value) {
            // Stub the two-arg overload the production code calls; Mockito mocks all
            // interface methods (including defaults), so the default implementation
            // that delegates via isDefined() + single-arg getString() never runs.
            when(profile.getString(DefaultDriverOption.REQUEST_CONSISTENCY, KeyValue.NONE_VALUE))
                    .thenReturn(value);
        }
    }
}
