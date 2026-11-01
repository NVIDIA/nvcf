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
package com.nvidia.nvct.configuration;

import com.datastax.oss.driver.api.core.ConsistencyLevel;
import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.DefaultConsistencyLevel;
import net.javacrumbs.shedlock.core.LockProvider;
import net.javacrumbs.shedlock.provider.cassandra.CassandraLockProvider;
import net.javacrumbs.shedlock.spring.annotation.EnableSchedulerLock;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
@EnableSchedulerLock(defaultLockAtMostFor = "60s")
public class DistributedLockConfiguration {

    @Bean
    public LockProvider lockProvider(
            CqlSession cqlSession,
            // Higher quorum for cross region locks
            @Value("${nvct.scheduled-routines.lock-consistency:EACH_QUORUM}")
            DefaultConsistencyLevel consistencyLevel) {
        return new CassandraLockProvider(
                CassandraLockProvider.Configuration.builder()
                        .withCqlSession(cqlSession)
                        .withTableName("lock")
                        // higher quorum for cross region locks
                        .withConsistencyLevel(consistencyLevel)
                        .withSerialConsistencyLevel(ConsistencyLevel.SERIAL)
                        .build());
    }

}
