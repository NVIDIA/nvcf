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
package com.nvidia.nvcf.service.scheduler;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.timeout;
import static org.mockito.Mockito.verify;

import com.datastax.oss.driver.api.core.CqlSession;
import com.datastax.oss.driver.api.core.cql.SimpleStatement;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoBean;

@TestInstance(Lifecycle.PER_CLASS)
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "nvcf.scheduler.enabled=true",
                "nvcf.scheduler.lock.consistency=LOCAL_ONE",
                "nvcf.scheduler.function-deployments.current-region=us-west-2",
                "nvcf.scheduler.function-deployments.regions[0]=us-west-2",
                "nvcf.scheduler.function-deployments.fixed-delay=PT24H",
                "nvcf.scheduler.function-deployments.lock-at-least-for=PT0S",
                "nvcf.scheduler.function-deployments.lock-at-most-for=PT1M"
        })
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ScheduledTaskServiceIntegrationTest {

    private static final String EXPECTED_LOCK_NAME =
            "functionDeploymentsTask-us-west-2";

    @Autowired
    private CqlSession cqlSession;

    @MockitoBean
    private FunctionDeploymentsTask functionDeploymentsTask;

    @MockitoBean
    private CleanNatsStreamsTask cleanNatsStreamsTask;

    @AfterEach
    void afterEach() {
        deleteLock();
    }

    @Test
    void shouldResolveRegionalFunctionDeploymentsLockName() {
        verify(functionDeploymentsTask, timeout(10_000)).run();

        var row = cqlSession.execute(SimpleStatement.newInstance(
                "SELECT name FROM lock WHERE name = ?", EXPECTED_LOCK_NAME)).one();
        assertThat(row).isNotNull();
        assertThat(row.getString("name")).isEqualTo(EXPECTED_LOCK_NAME);
    }

    private void deleteLock() {
        cqlSession.execute(SimpleStatement.newInstance(
                "DELETE FROM lock WHERE name = ?", EXPECTED_LOCK_NAME));
    }
}
