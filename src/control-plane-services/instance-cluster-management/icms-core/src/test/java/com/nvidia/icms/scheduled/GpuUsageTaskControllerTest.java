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

import static com.nvidia.icms.scheduled.GpuUsageTaskController.GPU_USAGE_EVENT_NAME;
import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.scheduled.gpuusage.GpuUsageTask;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
@Slf4j
class GpuUsageTaskControllerTest {

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private LockProviderService lockProviderService;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private GpuUsageTask gpuUsageTask;

    @InjectMocks
    private GpuUsageTaskController gpuUsageTaskController;

    @Test
    void sendGpuUsageEvent_withValidConfig_returnsSuccess() {
        // Prepare
        when(icmsConfigurationProperties.isGpuUsagePerInstanceTaskEnabled()).thenReturn(true);
        when(icmsConfigurationProperties.getGpuUsageTaskBoundSleepDurationInSeconds()).thenReturn(45);
        when(icmsConfigurationProperties.getGpuUsageTaskLockTtlInSeconds()).thenReturn(1800);
        when(icmsConfigurationProperties.isGpuUsageTaskSecureRandomEnabled()).thenReturn(false);

        when(lockProviderService.obtainLockWithTtl(GPU_USAGE_EVENT_NAME, 1800)).thenReturn(true);
        doNothing().when(gpuUsageTask).execute();
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        gpuUsageTaskController.sendGpuUsageEvent();

        // Assert
        verify(telemetryEventClient).triggerEvent(anyList());
        verify(icmsConfigurationProperties).isGpuUsagePerInstanceTaskEnabled();
        verify(icmsConfigurationProperties).getGpuUsageTaskBoundSleepDurationInSeconds();
        verify(icmsConfigurationProperties).getGpuUsageTaskLockTtlInSeconds();
        verify(icmsConfigurationProperties).isGpuUsageTaskSecureRandomEnabled();
    }

    @Test
    void getRandomSleepDuration_secureRandom_returnsSuccess() {
        // Prepare
        when(icmsConfigurationProperties.isGpuUsageTaskSecureRandomEnabled()).thenReturn(true);
        when(icmsConfigurationProperties.getGpuUsageTaskBoundSleepDurationInSeconds()).thenReturn(40);

        // Act
        int sleepDuration = gpuUsageTaskController.getRandomSleepDuration();

        // Assert
        verify(icmsConfigurationProperties).isGpuUsageTaskSecureRandomEnabled();
        verify(icmsConfigurationProperties).getGpuUsageTaskBoundSleepDurationInSeconds();
    }

    @Test
    void getRandomSleepDuration_podHash_returnsSuccess() {
        // Prepare
        when(icmsConfigurationProperties.isGpuUsageTaskSecureRandomEnabled()).thenReturn(true);
        when(icmsConfigurationProperties.getGpuUsageTaskBoundSleepDurationInSeconds()).thenReturn(40);

        // Act
        int sleepDuration = gpuUsageTaskController.getRandomSleepDuration();

        // Assert
        verify(icmsConfigurationProperties).isGpuUsageTaskSecureRandomEnabled();
        verify(icmsConfigurationProperties).getGpuUsageTaskBoundSleepDurationInSeconds();
    }
}
