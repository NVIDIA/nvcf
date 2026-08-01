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
package com.nvidia.icms.service.scheduled.request;

import static com.nvidia.icms.service.scheduled.request.ShuttingDownInstanceTerminationTask.STUCK_IN_SHUTTING_DOWN_ERROR_LOG;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.*;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.byoc.ByocTerminateService;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Arrays;
import java.util.List;
import java.util.function.Consumer;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
@Slf4j
class ShuttingDownInstanceTerminationTaskTest {

    @Mock
    private InstanceV2Repository instanceV2Repository;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private ByocTerminateService byocTerminateService;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    private ShuttingDownInstanceTerminationTask task;

    @BeforeEach
    void setUp() {
        task = new ShuttingDownInstanceTerminationTask(
                instanceV2Repository,
                telemetryEventClient,
                icmsConfigurationProperties,
                byocTerminateService,
                instanceServiceHelper
        );
    }

    // Test Case 1: Terminate instances in shutting-down state more than 24 hours
    @Test
    void execute_shouldTerminateInstancesInShuttingDownStateMoreThan24Hours() {

        // Arrange
        int thresholdHours = 24;
        when(icmsConfigurationProperties.getShuttingDownInstanceTerminationThresholdInHours()).thenReturn(thresholdHours);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        Instant now = Instant.now();
        Instant stuckTime = now.minus(25, ChronoUnit.HOURS); // 25 hours > 24h threshold

        InstanceV2Entity stuckInstance = createTestInstance(
                "stuck-instance-1",
                "test-request-1",
                stuckTime,
                "test-zone",
                "dummy_gpu_4.large",
                "test-nca",
                SpotInstanceInternalState.SHUTTING_DOWN
        );

        // Mock terminate service to return the entity
        when(byocTerminateService.updateInstanceEntityState(eq(stuckInstance), eq(STUCK_IN_SHUTTING_DOWN_ERROR_LOG)))
                .thenReturn(stuckInstance);

        // Mock repository to process the stuck instance
        doAnswer(invocation -> {
            Consumer<InstanceV2Entity> action = invocation.getArgument(0);
            action.accept(stuckInstance);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));

        // Act
        task.execute();

        // Assert
        verify(icmsConfigurationProperties, times(2)).getShuttingDownInstanceTerminationThresholdInHours();
        verify(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));
        verify(byocTerminateService).updateInstanceEntityState(eq(stuckInstance), eq(STUCK_IN_SHUTTING_DOWN_ERROR_LOG));
        verify(instanceV2Repository).update(eq(stuckInstance));
        verify(telemetryEventClient).triggerEvent(anyList()); // Success telemetry
    }

    // Test Case 2: Don't terminate instances in shutting-down state which are less than 24 hours
    @Test
    void execute_shouldNotTerminateInstancesInShuttingDownStateLessThan24Hours() {
        // Arrange
        int thresholdHours = 24;
        when(icmsConfigurationProperties.getShuttingDownInstanceTerminationThresholdInHours()).thenReturn(thresholdHours);

        Instant now = Instant.now();
        Instant recentTime = now.minus(23, ChronoUnit.HOURS); // 23 hours < 24h threshold

        InstanceV2Entity recentInstance = createTestInstance(
                "recent-instance-1",
                "test-request-1",
                recentTime,
                "test-zone",
                "dummy_gpu_4.large",
                "test-nca",
                SpotInstanceInternalState.SHUTTING_DOWN
        );

        // Mock repository to process the recent instance
        doAnswer(invocation -> {
            Consumer<InstanceV2Entity> action = invocation.getArgument(0);
            action.accept(recentInstance);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));

        // Act
        task.execute();

        // Assert
        verify(icmsConfigurationProperties).getShuttingDownInstanceTerminationThresholdInHours();
        verify(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));
        
        // Should NOT process recent instance
        verify(byocTerminateService, never()).updateInstanceEntityState(any(), any());
        verify(instanceV2Repository, never()).update(any());
        verifyNoInteractions(telemetryEventClient); // No telemetry for skipped instances
    }

    // Test Case 3: Don't terminate instances either in starting/running state
    @Test
    void execute_shouldNotTerminateInstancesInStartingOrRunningState() {
        // Arrange
        int thresholdHours = 24;
        when(icmsConfigurationProperties.getShuttingDownInstanceTerminationThresholdInHours()).thenReturn(thresholdHours);

        Instant now = Instant.now();
        Instant oldTime = now.minus(30, ChronoUnit.HOURS); // 30 hours > threshold but wrong state

        InstanceV2Entity startingInstance = createTestInstance(
                "starting-instance-1",
                "test-request-1",
                oldTime,
                "test-zone",
                "dummy_gpu_4.large",
                "test-nca",
                SpotInstanceInternalState.STARTING
        );

        InstanceV2Entity runningInstance = createTestInstance(
                "running-instance-1",
                "test-request-2",
                oldTime,
                "test-zone",
                "dummy_gpu_4.large",
                "test-nca",
                SpotInstanceInternalState.RUNNING
        );

        List<InstanceV2Entity> testInstances = Arrays.asList(startingInstance, runningInstance);

        // Mock repository to process both instances
        doAnswer(invocation -> {
            Consumer<InstanceV2Entity> action = invocation.getArgument(0);
            testInstances.forEach(action);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));

        // Act
        task.execute();

        // Assert
        verify(icmsConfigurationProperties).getShuttingDownInstanceTerminationThresholdInHours();
        verify(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));
        
        // Should NOT process instances in STARTING or RUNNING state
        verify(byocTerminateService, never()).updateInstanceEntityState(eq(startingInstance), any());
        verify(byocTerminateService, never()).updateInstanceEntityState(eq(runningInstance), any());
        verify(instanceV2Repository, never()).update(eq(startingInstance));
        verify(instanceV2Repository, never()).update(eq(runningInstance));
        verifyNoInteractions(telemetryEventClient); // No telemetry for skipped instances
    }

    // Test Case 4: Individual failure to update instance state should not stop other instances
    @Test
    void execute_individualFailureShouldNotStopOtherInstances() {
        // Arrange
        int thresholdHours = 24;
        when(icmsConfigurationProperties.getShuttingDownInstanceTerminationThresholdInHours()).thenReturn(thresholdHours);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        Instant now = Instant.now();
        Instant stuckTime = now.minus(25, ChronoUnit.HOURS); // Both stuck for > 24 hours

        InstanceV2Entity failingInstance = createTestInstance(
                "failing-instance",
                "test-request-1",
                stuckTime,
                "test-zone",
                "dummy_gpu_4.large",
                "test-nca",
                SpotInstanceInternalState.SHUTTING_DOWN
        );

        InstanceV2Entity successInstance = createTestInstance(
                "success-instance",
                "test-request-2",
                stuckTime,
                "test-zone",
                "dummy_gpu_4.large",
                "test-nca",
                SpotInstanceInternalState.SHUTTING_DOWN
        );

        // Mock first instance to fail, second to succeed
        doThrow(new RuntimeException("Database connection failed"))
                .when(byocTerminateService).updateInstanceEntityState(eq(failingInstance), eq(STUCK_IN_SHUTTING_DOWN_ERROR_LOG));
        when(byocTerminateService.updateInstanceEntityState(eq(successInstance), eq(STUCK_IN_SHUTTING_DOWN_ERROR_LOG)))
                .thenReturn(successInstance); // Return the entity (we ignore the return value in actual code)

        List<InstanceV2Entity> testInstances = Arrays.asList(failingInstance, successInstance);

        // Mock repository to process both instances
        doAnswer(invocation -> {
            Consumer<InstanceV2Entity> action = invocation.getArgument(0);
            testInstances.forEach(action);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));

        // Act
        task.execute();

        // Assert
        verify(icmsConfigurationProperties, times(3)).getShuttingDownInstanceTerminationThresholdInHours();
        verify(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));
        
        // Both instances should be attempted
        verify(byocTerminateService).updateInstanceEntityState(eq(failingInstance), eq(STUCK_IN_SHUTTING_DOWN_ERROR_LOG));
        verify(byocTerminateService).updateInstanceEntityState(eq(successInstance), eq(STUCK_IN_SHUTTING_DOWN_ERROR_LOG));
        
        // Only successful instance should be updated in repository
        verify(instanceV2Repository, never()).update(eq(failingInstance)); // Failed, so no DB update
        verify(instanceV2Repository).update(eq(successInstance)); // Successful update
        
        // Both instances should send telemetry (failure + success)
        verify(telemetryEventClient, times(2)).triggerEvent(anyList()); // Two telemetry events: one for the failing instance and one for the succeeding instance
    }

    // Combined test covering all scenarios in a single comprehensive test
    @Test
    void execute_comprehensiveScenario_allTestCasesInOne() {
        // Arrange
        int thresholdHours = 24;
        when(icmsConfigurationProperties.getShuttingDownInstanceTerminationThresholdInHours()).thenReturn(thresholdHours);
        doNothing().when(telemetryEventClient).triggerEvent(anyList());

        Instant now = Instant.now();
        Instant stuckTime = now.minus(30, ChronoUnit.HOURS);    // > 24h threshold
        Instant recentTime = now.minus(12, ChronoUnit.HOURS);   // < 24h threshold

        // Test Case 1: Should terminate - shutting-down > 24h
        InstanceV2Entity shouldTerminate = createTestInstance(
                "should-terminate",
                "request-1",
                stuckTime,
                "zone-1",
                "dummy_gpu_4.large",
                "nca-1",
                SpotInstanceInternalState.SHUTTING_DOWN
        );

        // Test Case 2: Should NOT terminate - shutting-down < 24h
        InstanceV2Entity recentShuttingDown = createTestInstance(
                "recent-shutting-down",
                "request-2",
                recentTime,
                "zone-2",
                "dummy_gpu_4.large",
                "nca-2",
                SpotInstanceInternalState.SHUTTING_DOWN
        );

        // Test Case 3: Should NOT terminate - starting state (even if old)
        InstanceV2Entity startingInstance = createTestInstance(
                "starting-instance",
                "request-3",
                stuckTime,
                "zone-3",
                "dummy_gpu_4.large",
                "nca-3",
                SpotInstanceInternalState.STARTING
        );

        // Test Case 3: Should NOT terminate - running state (even if old)
        InstanceV2Entity runningInstance = createTestInstance(
                "running-instance",
                "request-4",
                stuckTime,
                "zone-4",
                "dummy_gpu_4.large",
                "nca-4",
                SpotInstanceInternalState.RUNNING
        );

        List<InstanceV2Entity> allInstances = Arrays.asList(
                shouldTerminate, recentShuttingDown, startingInstance, runningInstance
        );

        // Mock terminate service for the one that should be terminated
        when(byocTerminateService.updateInstanceEntityState(eq(shouldTerminate), eq(STUCK_IN_SHUTTING_DOWN_ERROR_LOG)))
                .thenReturn(shouldTerminate);

        // Mock repository to process all instances
        doAnswer(invocation -> {
            Consumer<InstanceV2Entity> action = invocation.getArgument(0);
            allInstances.forEach(action);
            return null;
        }).when(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));

        // Act
        task.execute();

        // Assert
        verify(icmsConfigurationProperties, times(2)).getShuttingDownInstanceTerminationThresholdInHours();
        verify(instanceV2Repository).findAllInstancesAndApplyAction(any(), eq(500));

        // Only the stuck shutting-down instance should be processed
        verify(byocTerminateService).updateInstanceEntityState(eq(shouldTerminate), eq(STUCK_IN_SHUTTING_DOWN_ERROR_LOG));
        verify(instanceV2Repository).update(eq(shouldTerminate));

        // All others should NOT be processed
        verify(byocTerminateService, never()).updateInstanceEntityState(eq(recentShuttingDown), any());
        verify(byocTerminateService, never()).updateInstanceEntityState(eq(startingInstance), any());
        verify(byocTerminateService, never()).updateInstanceEntityState(eq(runningInstance), any());
        verify(instanceV2Repository, never()).update(eq(recentShuttingDown));
        verify(instanceV2Repository, never()).update(eq(startingInstance));
        verify(instanceV2Repository, never()).update(eq(runningInstance));

        // Should send telemetry for the terminated instance
        verify(telemetryEventClient).triggerEvent(anyList());
    }

    private InstanceV2Entity createTestInstance(String instanceId, String requestId, Instant updateTime,
                                                   String zone, String instanceType, String ncaId,
                                                   SpotInstanceInternalState state) {
        return InstanceV2Entity.builder()
                .instanceId(instanceId)
                .requestId(requestId)
                .createTimeuuid(TimeUtils.getTimeUuidNow())
                .customer("test-customer")
                .instanceUpdateTime(updateTime)
                .zone(zone)
                .instanceType(instanceType)
                .ncaId(ncaId)
                .instanceStateName(state)
                .instanceStateCode(SpotInstanceInternalState.getStateCode(state))
                .requestState(SpotInstanceRequestState.OPEN)
                .resourceProvider(ResourceProvider.BYOC)
                .build();
    }
} 