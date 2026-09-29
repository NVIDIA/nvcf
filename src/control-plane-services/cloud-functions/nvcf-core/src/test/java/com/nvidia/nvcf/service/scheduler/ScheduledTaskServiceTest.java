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

import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.nvcf.configuration.scheduler.FunctionDeploymentsTaskProperties;
import java.time.Duration;
import net.javacrumbs.shedlock.core.LockAssert;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.boot.context.event.ApplicationReadyEvent;

@ExtendWith(MockitoExtension.class)
class ScheduledTaskServiceTest {

    @Mock
    private FunctionDeploymentsTask functionDeploymentsTask;

    @Mock
    private CleanNatsStreamsTask cleanNatsStreamsTask;

    @Mock
    private FunctionDeploymentsTaskProperties functionDeploymentsTaskProperties;

    @Mock
    private ApplicationReadyEvent applicationReadyEvent;

    private ScheduledTaskService scheduledTaskService;

    @BeforeEach
    void beforeEach() {
        LockAssert.TestHelper.makeAllAssertsPass(true);
        scheduledTaskService = new ScheduledTaskService(
                functionDeploymentsTask,
                cleanNatsStreamsTask,
                functionDeploymentsTaskProperties);
        scheduledTaskService.onApplicationEvent(applicationReadyEvent);
    }

    @AfterEach
    void afterEach() {
        LockAssert.TestHelper.makeAllAssertsPass(false);
    }

    @Test
    void shouldRunFunctionDeploymentsTask() throws InterruptedException {
        when(functionDeploymentsTaskProperties.getCurrentRegion()).thenReturn("test-region");

        scheduledTaskService.functionDeployments();

        verify(functionDeploymentsTaskProperties).getCurrentRegion();
        verify(functionDeploymentsTask).run();
    }

    @Test
    void shouldRunCleanNatsStreamsTask() throws Exception {
        scheduledTaskService.cleanNatsStreams();

        verify(cleanNatsStreamsTask).run(Duration.ofMinutes(15));
    }
}
