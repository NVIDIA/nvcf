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

import com.datastax.oss.driver.api.core.config.DefaultDriverOption;
import com.datastax.oss.driver.api.core.config.DriverConfig;
import com.datastax.oss.driver.api.core.config.DriverExecutionProfile;
import com.datastax.oss.driver.api.core.cql.Statement;
import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import java.util.Objects;
import org.springframework.data.cassandra.observability.CassandraObservationContext;
import org.springframework.data.cassandra.observability.DefaultCassandraObservationConvention;

/**
 * Extends {@link DefaultCassandraObservationConvention} to ensure
 * {@code db.cassandra.consistency_level} is present on execute spans even when
 * {@link Statement#getConsistencyLevel()} returns {@code null} (e.g. batch
 * statements or repository methods without {@code @Consistency}).
 *
 * <p>The effective consistency level is resolved per-statement from the driver's
 * execution profiles, following the same precedence the driver uses internally:
 * inline profile &rarr; named profile &rarr; default profile.
 */
public class CustomCassandraObservationConvention
        extends DefaultCassandraObservationConvention {

    private final DriverConfig driverConfig;

    public CustomCassandraObservationConvention(DriverConfig driverConfig) {
        this.driverConfig = driverConfig;
    }

    @Override
    public KeyValues getHighCardinalityKeyValues(CassandraObservationContext context) {
        String consistencyKey = "db.cassandra.consistency_level";
        KeyValues kvs = super.getHighCardinalityKeyValues(context);

        boolean consistencySet = kvs.stream().anyMatch(kv -> Objects.equals(consistencyKey, kv.getKey()));
        if (!context.isPrepare() && !consistencySet) {
            kvs = kvs.and(KeyValue.of(consistencyKey,
                    resolveConsistencyLevel(context.getStatement())));
        }
        return kvs;
    }

    private String resolveConsistencyLevel(Statement<?> statement) {
        DriverExecutionProfile profile = statement.getExecutionProfile();
        if (profile == null) {
            String name = statement.getExecutionProfileName();
            profile = name != null && driverConfig.getProfiles().containsKey(name)
                    ? driverConfig.getProfile(name)
                    : driverConfig.getDefaultProfile();
        }
        return profile.getString(DefaultDriverOption.REQUEST_CONSISTENCY, KeyValue.NONE_VALUE).toUpperCase();
    }
}
