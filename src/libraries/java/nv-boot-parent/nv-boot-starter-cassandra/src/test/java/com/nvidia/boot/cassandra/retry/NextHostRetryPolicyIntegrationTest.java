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

package com.nvidia.boot.cassandra.retry;

import static org.junit.jupiter.api.Assertions.assertEquals;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.config.DefaultDriverOption;
import com.datastax.oss.driver.api.core.config.DriverConfigLoader;
import org.junit.jupiter.api.Test;
import org.testcontainers.cassandra.CassandraContainer;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;

@Testcontainers
class NextHostRetryPolicyIntegrationTest {

    @Container
    static final CassandraContainer CASSANDRA = new CassandraContainer("cassandra:5");

    @Test
    void nextHostRetryPolicyCanBeRegisteredWithDriver() {
        var contactPoint = CASSANDRA.getContactPoint();

        var loader = DriverConfigLoader.programmaticBuilder()
                .withClass(DefaultDriverOption.RETRY_POLICY_CLASS, NextHostRetryPolicy.class)
                .build();

        try (var session = CqlSession.builder()
                .addContactPoint(contactPoint)
                .withLocalDatacenter(CASSANDRA.getLocalDatacenter())
                .withConfigLoader(loader)
                .build()) {
            assertEquals(NextHostRetryPolicy.class,
                    session.getContext().getRetryPolicies().get("default").getClass());
        }
    }
}
