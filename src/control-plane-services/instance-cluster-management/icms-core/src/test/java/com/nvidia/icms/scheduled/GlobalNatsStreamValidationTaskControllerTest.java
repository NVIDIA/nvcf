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

import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.outbound.nats.NatsStreamManager;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.time.Duration;

@ExtendWith(MockitoExtension.class)
class GlobalNatsStreamValidationTaskControllerTest {

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private LockProviderService lockProviderService;

    @Mock
    private NatsConfigurationProperties natsConfigurationProperties;

    @Mock
    private NatsStreamManager natsStreamManager;

    private GlobalNatsStreamValidationTaskController controller;

    @BeforeEach
    void setUp() {
        controller = new GlobalNatsStreamValidationTaskController(
                telemetryEventClient,
                lockProviderService,
                natsConfigurationProperties,
                natsStreamManager);
    }

    @Test
    void validateGlobalStreams_whenTaskDisabled_shouldNotExecute() {
        // Prepare
        doReturn(false).when(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();

        // Act
        controller.validateGlobalStreams();

        // Assert
        verify(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();
        verifyNoMoreInteractions(natsConfigurationProperties);
        verifyNoInteractions(lockProviderService);
        verifyNoInteractions(natsStreamManager);
        verifyNoInteractions(telemetryEventClient);
    }

    @Test
    void validateGlobalStreams_whenLockNotAcquired_shouldNotExecute() {
        // Prepare
        doReturn(true).when(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();
        doReturn(Duration.ofSeconds(120)).when(natsConfigurationProperties).getGlobalStreamValidationTaskLockTtl();
        doReturn(false).when(lockProviderService).obtainLockWithTtl(anyString(), anyInt());

        // Act
        controller.validateGlobalStreams();

        // Assert
        verify(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();
        verify(lockProviderService).obtainLockWithTtl(
                eq(GlobalNatsStreamValidationTaskController.GLOBAL_NATS_STREAM_VALIDATION_JOB_NAME),
                anyInt());
        verifyNoMoreInteractions(lockProviderService);
        verifyNoInteractions(natsStreamManager);
        verifyNoInteractions(telemetryEventClient);
    }

    @Test
    void validateGlobalStreams_whenSuccessful_shouldExecuteAndSendTelemetry() {
        // Prepare
        doReturn(true).when(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();
        doReturn(Duration.ofSeconds(120)).when(natsConfigurationProperties).getGlobalStreamValidationTaskLockTtl();
        doReturn(true).when(lockProviderService).obtainLockWithTtl(anyString(), anyInt());
        doNothing().when(natsStreamManager).validateNatsStreams();
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        controller.validateGlobalStreams();

        // Assert
        verify(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();
        verify(lockProviderService).obtainLockWithTtl(
                eq(GlobalNatsStreamValidationTaskController.GLOBAL_NATS_STREAM_VALIDATION_JOB_NAME),
                anyInt());
        verify(natsStreamManager).validateNatsStreams();
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    @Test
    void validateGlobalStreams_whenExceptionOccurs_shouldSendErrorTelemetry() {
        // Prepare
        String errorMessage = "Test error";
        doReturn(true).when(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();
        doReturn(Duration.ofSeconds(120)).when(natsConfigurationProperties).getGlobalStreamValidationTaskLockTtl();
        doReturn(true).when(lockProviderService).obtainLockWithTtl(anyString(), anyInt());
        doThrow(new RuntimeException(errorMessage)).when(natsStreamManager).validateNatsStreams();
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        controller.validateGlobalStreams();

        // Assert
        verify(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();
        verify(lockProviderService).obtainLockWithTtl(
                eq(GlobalNatsStreamValidationTaskController.GLOBAL_NATS_STREAM_VALIDATION_JOB_NAME),
                anyInt());
        verify(natsStreamManager).validateNatsStreams();
        verify(telemetryEventClient).triggerEvent(argThat(metrics -> {
            if (metrics == null || metrics.size() != 1) {
                return false;
            }
            GenericMetric metric = metrics.getFirst();
            return Events.GLOBAL_NATS_STREAM_VALIDATION_TASK.toString().equals(metric.getEventName()) &&
                   metric.getMetadata() != null &&
                   metric.getMetadata().containsKey(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName()) &&
                   errorMessage.equals(metric.getError());
        }));
    }

    @Test
    void validateGlobalStreams_shouldSendTelemetryWithExecutionTime() {
        // Prepare
        doReturn(true).when(natsConfigurationProperties).isGlobalStreamValidationTaskEnabled();
        doReturn(Duration.ofSeconds(120)).when(natsConfigurationProperties).getGlobalStreamValidationTaskLockTtl();
        doReturn(true).when(lockProviderService).obtainLockWithTtl(anyString(), anyInt());
        doNothing().when(natsStreamManager).validateNatsStreams();
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        // Act
        controller.validateGlobalStreams();

        // Assert
        verify(telemetryEventClient).triggerEvent(argThat(metrics -> {
            if (metrics == null || metrics.size() != 1) {
                return false;
            }
            GenericMetric metric = metrics.getFirst();
            return Events.GLOBAL_NATS_STREAM_VALIDATION_TASK.toString().equals(metric.getEventName()) &&
                   metric.getMetadata() != null &&
                   metric.getMetadata().containsKey(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName()) &&
                   metric.getError() == null;
        }));
    }
}
