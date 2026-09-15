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

import static com.nvidia.icms.scheduled.ShuttingDownInstanceTerminationTaskController.SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.scheduled.request.ShuttingDownInstanceTerminationTask;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
@Slf4j
class ShuttingDownInstanceTerminationTaskControllerTest {

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private LockProviderService lockProviderService;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private ShuttingDownInstanceTerminationTask shuttingDownInstanceTerminationTask;

    @InjectMocks
    private ShuttingDownInstanceTerminationTaskController controller;

    @Test
    void terminateStuckShuttingDownInstances_whenTaskDisabled_shouldNotExecute() {
        // Arrange
        doReturn(false).when(icmsConfigurationProperties).isShuttingDownInstanceTerminationTaskEnabled();

        // Act
        controller.terminateStuckShuttingDownInstances();

        // Assert
        verify(icmsConfigurationProperties).isShuttingDownInstanceTerminationTaskEnabled();
        verifyNoMoreInteractions(icmsConfigurationProperties);
        verifyNoInteractions(lockProviderService);
        verifyNoInteractions(shuttingDownInstanceTerminationTask);
        verifyNoInteractions(telemetryEventClient);
    }

    @Test
    void terminateStuckShuttingDownInstances_whenLockNotAcquired_shouldNotExecute() {
        // Arrange
        doReturn(true).when(icmsConfigurationProperties).isShuttingDownInstanceTerminationTaskEnabled();
        doReturn(120).when(icmsConfigurationProperties).getShuttingDownInstanceTerminationTaskLockTtlInSeconds();
        doReturn(false).when(lockProviderService).obtainLockWithTtl(
                eq(SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME), eq(120));

        // Act
        controller.terminateStuckShuttingDownInstances();

        // Assert
        verify(icmsConfigurationProperties).isShuttingDownInstanceTerminationTaskEnabled();
        verify(icmsConfigurationProperties).getShuttingDownInstanceTerminationTaskLockTtlInSeconds();
        verify(lockProviderService).obtainLockWithTtl(SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME, 120);
        verifyNoInteractions(shuttingDownInstanceTerminationTask);
        verifyNoInteractions(telemetryEventClient);
    }

    @Test
    void terminateStuckShuttingDownInstances_whenLockAcquired_shouldExecuteTask() {
        // Arrange
        doReturn(true).when(icmsConfigurationProperties).isShuttingDownInstanceTerminationTaskEnabled();
        doReturn(120).when(icmsConfigurationProperties).getShuttingDownInstanceTerminationTaskLockTtlInSeconds();
        doReturn(true).when(lockProviderService).obtainLockWithTtl(
                eq(SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME), eq(120));
        doNothing().when(shuttingDownInstanceTerminationTask).execute();
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        controller.terminateStuckShuttingDownInstances();

        // Assert
        verify(icmsConfigurationProperties).isShuttingDownInstanceTerminationTaskEnabled();
        verify(icmsConfigurationProperties).getShuttingDownInstanceTerminationTaskLockTtlInSeconds();
        verify(lockProviderService).obtainLockWithTtl(SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME, 120);
        verify(shuttingDownInstanceTerminationTask).execute();
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void terminateStuckShuttingDownInstances_whenTaskThrowsException_shouldCaptureErrorAndSendTelemetry() {
        // Arrange
        RuntimeException testException = new RuntimeException("Test exception");
        doReturn(true).when(icmsConfigurationProperties).isShuttingDownInstanceTerminationTaskEnabled();
        doReturn(120).when(icmsConfigurationProperties).getShuttingDownInstanceTerminationTaskLockTtlInSeconds();
        doReturn(true).when(lockProviderService).obtainLockWithTtl(
                eq(SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME), eq(120));
        doThrow(testException).when(shuttingDownInstanceTerminationTask).execute();
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        controller.terminateStuckShuttingDownInstances();

        // Assert
        verify(icmsConfigurationProperties).isShuttingDownInstanceTerminationTaskEnabled();
        verify(icmsConfigurationProperties).getShuttingDownInstanceTerminationTaskLockTtlInSeconds();
        verify(lockProviderService).obtainLockWithTtl(SHUTTING_DOWN_INSTANCE_TERMINATION_JOB_NAME, 120);
        verify(shuttingDownInstanceTerminationTask).execute();
        verify(telemetryEventClient).triggerEvent(anyList()); // Should still send telemetry with error
    }
} 