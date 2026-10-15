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
package com.nvidia.icms.service.scheduled.gpuusage;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.LatestInstanceStateEventService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.TestUtil;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Optional;
import java.util.Set;
import java.util.function.Consumer;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class GpuUsageTaskTest {

    @Mock
    private InstanceV2Repository instanceV2Repository;

    @Mock
    private GpuUsageEventService gpuUsageEventService;

    @Mock
    private CloudHealthRepository cloudHealthRepository;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private LatestInstanceStateEventService latestInstanceStateEventService;

    private GpuUsageTask gpuUsageTask;

    @BeforeEach
    void setUp() {
        gpuUsageTask = new GpuUsageTask(instanceV2Repository, gpuUsageEventService, cloudHealthRepository, telemetryEventClient, latestInstanceStateEventService);
    }

    @Test
    void execute_ShouldProcessRunningInstances() {
        // Arrange
        InstanceV2Entity runningInstance = createTestInstance(
                "test-instance-1",
                "test-request-1",
                Instant.now().minus(30, ChronoUnit.MINUTES),
                "test-zone",
                "dummy_gpu_4.large",
                "test-gpu",
                "test-nca",
                "test-request-data",
                SpotInstanceInternalState.RUNNING,
                Instant.now(),
                SpotInstanceRequestState.OPEN
        );

        when(cloudHealthRepository.finalAllHealthyZones()).thenReturn(Set.of("test-zone"));

        // Mock repository to process the instance
        doAnswer(invocation -> {
            var action = invocation.getArgument(0, Consumer.class);
            action.accept(runningInstance);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), anyInt());

        // Act
        gpuUsageTask.execute();

        // Assert
        ArgumentCaptor<Instant> currentTimeCaptor = ArgumentCaptor.forClass(Instant.class);
        ArgumentCaptor<Instant> previousTimeCaptor = ArgumentCaptor.forClass(Instant.class);
        verify(gpuUsageEventService).sendGpuUsageEventForRunningInstance(
                eq(runningInstance),
                currentTimeCaptor.capture(),
                previousTimeCaptor.capture(),
                any()
        );
        Instant currentTime = currentTimeCaptor.getValue();
        Instant previousTime = previousTimeCaptor.getValue();
        assertEquals(1L, ChronoUnit.HOURS.between(previousTime, currentTime));

        verify(cloudHealthRepository).finalAllHealthyZones();
    }

    @Test
    void execute_ShouldSkipNonRunningInstances() {
        // Arrange
        InstanceV2Entity terminatedInstance = createTestInstance(
                "test-instance-2",
                "test-request-2",
                Instant.now().minus(30, ChronoUnit.MINUTES),
                "test-zone",
                "dummy_gpu_4.large",
                "test-gpu",
                "test-nca",
                "test-request-data",
                SpotInstanceInternalState.TERMINATED,
                Instant.now(),
                SpotInstanceRequestState.CLOSED
        );

        when(cloudHealthRepository.finalAllHealthyZones()).thenReturn(Set.of("test-zone"));

        // Mock repository to process the instance
        doAnswer(invocation -> {
            var action = invocation.getArgument(0, Consumer.class);
            action.accept(terminatedInstance);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), anyInt());

        // Act
        gpuUsageTask.execute();

        // Assert
        verify(gpuUsageEventService, never()).sendGpuUsageEventForRunningInstance(
                any(), any(), any(), any()
        );
        verify(cloudHealthRepository).finalAllHealthyZones();
    }

    @Test
    void execute_ShouldSkipRunningInstanceFromUnhealthyCluster() {
        // Arrange
        InstanceV2Entity terminatedInstance = createTestInstance(
                "test-instance-2",
                "test-request-2",
                Instant.now().minus(30, ChronoUnit.MINUTES),
                "test-zone",
                "dummy_gpu_4.large",
                "test-gpu",
                "test-nca",
                "test-request-data",
                SpotInstanceInternalState.RUNNING,
                Instant.now(),
                SpotInstanceRequestState.OPEN
        );

        when(cloudHealthRepository.finalAllHealthyZones()).thenReturn(Set.of());

        // Mock repository to process the instance
        doAnswer(invocation -> {
            var action = invocation.getArgument(0, Consumer.class);
            action.accept(terminatedInstance);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), anyInt());

        // Act
        gpuUsageTask.execute();

        // Assert
        verify(gpuUsageEventService, never()).sendGpuUsageEventForRunningInstance(
                any(), any(), any(), any()
        );
        verify(cloudHealthRepository).finalAllHealthyZones();
    }

    @Test
    void execute_ShouldHandleMultipleInstances() {
        // Arrange
        InstanceV2Entity instance1 = createTestInstance(
                "test-instance-1",
                "test-request-1",
                Instant.now().minus(30, ChronoUnit.MINUTES),
                "test-zone-1",
                "dummy_gpu_4.large",
                "test-gpu-1",
                "test-nca-1",
                "test-request-data-1",
                SpotInstanceInternalState.RUNNING,
                Instant.now(),
                SpotInstanceRequestState.OPEN
        );

        InstanceV2Entity instance2 = createTestInstance(
                "test-instance-2",
                "test-request-2",
                Instant.now().minus(45, ChronoUnit.MINUTES),
                "test-zone-2",
                "dummy_gpu_4.large",
                "test-gpu-2",
                "test-nca-2",
                "test-request-data-2",
                SpotInstanceInternalState.RUNNING,
                Instant.now(),
                SpotInstanceRequestState.OPEN
        );

        when(cloudHealthRepository.finalAllHealthyZones()).thenReturn(Set.of("test-zone-1", "test-zone-2"));

        // Mock repository to process both instances
        doAnswer(invocation -> {
            var action = invocation.getArgument(0, Consumer.class);
            action.accept(instance1);
            action.accept(instance2);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), anyInt());

        // Act
        gpuUsageTask.execute();

        // Assert
        verify(gpuUsageEventService, times(2)).sendGpuUsageEventForRunningInstance(
                any(), any(), any(), any()
        );
        verify(cloudHealthRepository).finalAllHealthyZones();
    }

    @Test
    void execute_ShouldSkipShuttingDownInstances() {
        // Arrange
        InstanceV2Entity terminatedInstance = createTestInstance(
                "test-instance-2",
                "test-request-2",
                Instant.now().minus(30, ChronoUnit.MINUTES),
                "test-zone",
                "dummy_gpu_4.large",
                "test-gpu",
                "test-nca",
                "test-request-data",
                SpotInstanceInternalState.SHUTTING_DOWN,
                Instant.now(),
                SpotInstanceRequestState.OPEN
        );

        when(cloudHealthRepository.finalAllHealthyZones()).thenReturn(Set.of("test-zone"));
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());

        // Mock repository to process the instance
        doAnswer(invocation -> {
            var action = invocation.getArgument(0, Consumer.class);
            action.accept(terminatedInstance);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), anyInt());

        // Act
        gpuUsageTask.execute();

        // Assert
        verify(gpuUsageEventService, never()).sendGpuUsageEventForRunningInstance(
                any(), any(), any(), any()
        );
        verify(cloudHealthRepository).finalAllHealthyZones();
        verify(telemetryEventClient).triggerEvent(Mockito.any());
    }

    private InstanceV2Entity createTestInstance(
            String instanceId,
            String requestId,
            Instant creationTime,
            String zone,
            String instanceType,
            String gpu,
            String ncaId,
            String requestRawData,
            SpotInstanceInternalState state,
            Instant updateTime,
            SpotInstanceRequestState requestState) {
        return InstanceV2Entity.builder()
                .instanceId(instanceId)
                .requestId(requestId)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(creationTime))
                .zone(zone)
                .instanceType(instanceType)
                .gpu(gpu)
                .ncaId(ncaId)
                .requestRawData(requestRawData)
                .instanceStateName(state)
                .instanceUpdateTime(updateTime)
                .resourceProvider(ResourceProvider.BYOC)
                .requestState(requestState)
                .build();
    }
} 