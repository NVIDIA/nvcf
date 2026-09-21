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
package com.nvidia.icms.scheduled;

import static com.nvidia.icms.scheduled.CreationBucketPopulationTaskController.CREATION_BUCKET_POPULATION_TASK_NAME;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.scheduled.CreationBucketPopulationTask;
import com.nvidia.icms.service.scheduled.CreationBucketPopulationTask.PopulationResult;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class CreationBucketPopulationTaskControllerTest {

    @Mock
    private CreationBucketPopulationTask task;
    @Mock
    private TelemetryEventClient telemetryEventClient;
    @Mock
    private LockProviderService lockProviderService;
    @Mock
    private IcmsConfigurationProperties configuration;
    @InjectMocks
    private CreationBucketPopulationTaskController controller;

    @Test
    void populateCreationBuckets_whenDisabled_doesNothing() {
        when(configuration.isCreationBucketPopulationTaskEnabled()).thenReturn(false);

        controller.populateCreationBuckets();

        verify(configuration).isCreationBucketPopulationTaskEnabled();
        verifyNoInteractions(task, telemetryEventClient, lockProviderService);
    }

    @Test
    void populateCreationBuckets_whenLockAcquired_executesTask() {
        when(configuration.isCreationBucketPopulationTaskEnabled()).thenReturn(true);
        when(configuration.getCreationBucketPopulationTaskLockTtlInSeconds()).thenReturn(120);
        when(lockProviderService.obtainLockWithTtl(
                CREATION_BUCKET_POPULATION_TASK_NAME, 120)).thenReturn(true);
        when(task.execute()).thenReturn(new PopulationResult(2, 0, 3, 1));

        controller.populateCreationBuckets();

        verify(task).execute();
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void populateCreationBuckets_whenLockNotAcquired_doesNotExecuteTask() {
        when(configuration.isCreationBucketPopulationTaskEnabled()).thenReturn(true);
        when(configuration.getCreationBucketPopulationTaskLockTtlInSeconds()).thenReturn(120);
        when(lockProviderService.obtainLockWithTtl(
                CREATION_BUCKET_POPULATION_TASK_NAME, 120)).thenReturn(false);

        controller.populateCreationBuckets();

        verifyNoInteractions(task, telemetryEventClient);
    }
}
