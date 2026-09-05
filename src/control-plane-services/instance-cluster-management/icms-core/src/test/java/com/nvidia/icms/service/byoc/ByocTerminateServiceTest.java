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
package com.nvidia.icms.service.byoc;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.TERMINATE_INSTANCES;
import static com.nvidia.icms.service.byoc.ByocTerminateService.TERMINATION_MESSAGE_PREFIX;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_RUNNING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_STARTING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_TRACE_PARENT;
import static com.nvidia.icms.util.TestUtil.DUMMY_TRACE_STATE;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static com.nvidia.icms.util.TestUtil.getDummyClientRequestDataModel;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.fnds.model.FndsStages;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.FunctionDeploymentStagesService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformService;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocTerminatePodMessageModel;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TestUtil;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import org.junit.jupiter.api.Assertions;
import com.nvidia.icms.service.workers.WorkerIdentifierService;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class ByocTerminateServiceTest {

    @Mock
    private InstanceV2Repository instanceV2Repository;

    @Mock
    private AppAuditService auditService;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private ClusterRepository clusterRepository;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    @Mock
    private FunctionDeploymentStagesService functionDeploymentStagesService;

    @Mock
    private ComputePlatformService computePlatformService;

    @Mock
    private WorkerIdentifierService workerIdentifierService;

    @InjectMocks
    private ByocTerminateService byocTerminateService;

    @BeforeEach
    void setUp() {
        Mockito.lenient().when(instanceServiceHelper.getTraceParent()).thenReturn(DUMMY_TRACE_PARENT);
        Mockito.lenient().when(instanceServiceHelper.getTraceStateMap()).thenReturn(DUMMY_TRACE_STATE);
    }

    @Test
    void terminateInstances_withInstancesFromSameRequest_returnsSuccess() {
        // Prepare
        var runningInstance = TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                  SpotInstanceRequestState.ACTIVE,
                                                                  Instant.now(),
                                                                  ResourceProvider.BYOC);

        var startedInstance = TestUtil.getDummyInstanceEntity(
                SpotInstanceInternalState.STARTING,
                SpotInstanceRequestState.ACTIVE, Instant.now(), ResourceProvider.BYOC);

        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_ZONE);

        var byocTerminateMessage = ByocTerminatePodMessageModel.builder()
                .action(TERMINATE_INSTANCES.getRequestAction())
                .availabilityZone(clusterEntity.getClusterName())
                .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                .instanceIds(Set.of(DUMMY_RUNNING_INSTANCE_ID, DUMMY_STARTING_INSTANCE_ID))
                .traceParent(DUMMY_TRACE_PARENT)
                .traceState(DUMMY_TRACE_STATE)
                .ncaId(startedInstance.getNcaId()).build();

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_ZONE, true)).thenReturn(
                Optional.of(clusterEntity));

        doNothing().when(instanceServiceHelper)
                .sendTerminateMessage(clusterEntity.getTerminationQueueUrl(),
                                     List.of(byocTerminateMessage),
                                     TERMINATION_MESSAGE_PREFIX,
                                     clusterEntity.getClusterId());

        doNothing().when(instanceV2Repository).update(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                CloudProvider.AZURE.toString()).getLaunchSpecification()).when(
                instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());

        // Act
        var terminationResponse = byocTerminateService.terminateInstances(Set.of(runningInstance,
                                                                                      startedInstance),
                                                                               new HashMap<>());

        // Assert
        var expectedInstanceIds = Set.of(DUMMY_STARTING_INSTANCE_ID, DUMMY_RUNNING_INSTANCE_ID);
        for (TerminateInstancesResponse.TerminatingInstance terminatingInstance : terminationResponse.getTerminatingInstances()) {
            Assertions.assertTrue(
                    expectedInstanceIds.contains(terminatingInstance.getInstanceId()));
            Assertions.assertEquals(terminatingInstance.getCurrentState().getName(),
                                    SpotInstanceInternalState.SHUTTING_DOWN.getStateName());
        }
        verify(telemetryEventClient, times(1)).triggerEvent(Mockito.any());
        verify(auditService, times(2)).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                                     Mockito.any());
        verify(instanceV2Repository, times(2)).update(Mockito.any());
        verify(instanceServiceHelper, times(1)).sendTerminateMessage(
                eq(clusterEntity.getTerminationQueueUrl()),
                eq(List.of(byocTerminateMessage)),
                eq(TERMINATION_MESSAGE_PREFIX), eq(clusterEntity.getClusterId()));
        verify(clusterRepository, times(1)).getClusterInfoByClusterId(DUMMY_ZONE, true);
        verify(instanceServiceHelper, times(2)).getLaunchSpecificationForTelemetry(Mockito.any());
        verify(functionDeploymentStagesService).sendFunctionDeploymentStage(Mockito.argThat(arg -> arg.getInstanceId().equals(DUMMY_RUNNING_INSTANCE_ID)),
                                                                            eq(FndsStages.REQUESTING_TERMINATION.toString()));

    }

    @Test
    void terminateInstances_withInstancesFromDifferentRequestButSameCluster_returnsSuccess() {

        // Prepare
        var runningInstance = TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                  SpotInstanceRequestState.ACTIVE,
                                                                  Instant.now(),
                                                                  ResourceProvider.BYOC);
        runningInstance.setRequestId("r1");

        var startedInstance = TestUtil.getDummyInstanceEntity(
                SpotInstanceInternalState.STARTING,
                SpotInstanceRequestState.ACTIVE, Instant.now(), ResourceProvider.BYOC);
        startedInstance.setRequestId("r2");

        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_ZONE);

        var byocTerminatePodMessageModelList = List.of(
                ByocTerminatePodMessageModel.builder()
                        .action(TERMINATE_INSTANCES.getRequestAction())
                        .availabilityZone(
                                clusterEntity.getClusterName())
                        .requestId("r1").instanceIds(
                                Set.of(DUMMY_RUNNING_INSTANCE_ID))
                        .ncaId(startedInstance.getNcaId())
                        .traceParent(DUMMY_TRACE_PARENT)
                        .traceState(DUMMY_TRACE_STATE)
                        .build(),
                ByocTerminatePodMessageModel.builder()
                        .action(TERMINATE_INSTANCES.getRequestAction())
                        .availabilityZone(
                                clusterEntity.getClusterName())
                        .requestId("r2").instanceIds(
                                Set.of(DUMMY_STARTING_INSTANCE_ID))
                        .ncaId(startedInstance.getNcaId())
                        .traceState(DUMMY_TRACE_STATE)
                        .traceParent(DUMMY_TRACE_PARENT)
                        .build());

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_ZONE, true)).thenReturn(
                Optional.of(clusterEntity));

        doNothing().when(instanceServiceHelper)
                .sendTerminateMessage(eq(clusterEntity.getTerminationQueueUrl()),
                                 Mockito.argThat(list -> list.containsAll(
                                         byocTerminatePodMessageModelList)),
                                 eq(TERMINATION_MESSAGE_PREFIX), eq(clusterEntity.getClusterId()));

        doNothing().when(instanceV2Repository).update(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                CloudProvider.AZURE.toString()).getLaunchSpecification()).when(
                instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());

        // Act
        var terminationResponse = byocTerminateService.terminateInstances(Set.of(runningInstance,
                                                                                      startedInstance),
                                                                               new HashMap<>());

        // Assert
        var expectedInstanceIds = Set.of(DUMMY_STARTING_INSTANCE_ID, DUMMY_RUNNING_INSTANCE_ID);
        for (TerminateInstancesResponse.TerminatingInstance terminatingInstance : terminationResponse.getTerminatingInstances()) {
            Assertions.assertTrue(
                    expectedInstanceIds.contains(terminatingInstance.getInstanceId()));
            Assertions.assertEquals(terminatingInstance.getCurrentState().getName(),
                                    SpotInstanceInternalState.SHUTTING_DOWN.getStateName());
        }

        verify(telemetryEventClient, times(1)).triggerEvent(Mockito.any());
        verify(auditService, times(2)).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                                     Mockito.any());
        verify(instanceV2Repository, times(2)).update(Mockito.any());
        verify(instanceServiceHelper, times(1)).sendTerminateMessage(
                eq(clusterEntity.getTerminationQueueUrl()),
                Mockito.argThat(list -> list.containsAll(byocTerminatePodMessageModelList)),
                eq(TERMINATION_MESSAGE_PREFIX), eq(clusterEntity.getClusterId()));
        verify(clusterRepository, times(2)).getClusterInfoByClusterId(DUMMY_ZONE, true);
        verify(instanceServiceHelper, times(2)).getLaunchSpecificationForTelemetry(Mockito.any());
        verify(functionDeploymentStagesService).sendFunctionDeploymentStage(Mockito.argThat(arg -> arg.getInstanceId().equals(DUMMY_RUNNING_INSTANCE_ID)),
                                                                            eq(FndsStages.REQUESTING_TERMINATION.toString()));

    }

    @Test
    void terminateInstances_withAlreadyTerminatedInstancesProvided_returnsEmptyListInResponse() {
        // Prepare
        var terminatedInstance =
                TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                                                    SpotInstanceRequestState.ACTIVE, Instant.now(),
                                                    ResourceProvider.BYOC);

        var shuttingDownInstance =
                TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.SHUTTING_DOWN,
                                                    SpotInstanceRequestState.ACTIVE, Instant.now(),
                                                    ResourceProvider.BYOC);

        doNothing().when(telemetryEventClient).triggerEvent(new ArrayList<>());

        // Act
        var terminationResponse = byocTerminateService.terminateInstances(Set.of(terminatedInstance,
                                                                                      shuttingDownInstance),
                                                                               new HashMap<>());

        // Assert
        Assertions.assertEquals(0, terminationResponse.getTerminatingInstances().size());

        verify(telemetryEventClient, times(1)).triggerEvent(new ArrayList<>());
        verify(auditService, times(0)).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                                     Mockito.any());
        verify(instanceV2Repository, times(0)).update(Mockito.any());
        verify(instanceServiceHelper, times(0)).sendMessageToSqsQueue(Mockito.any(), Mockito.any(),
                                                                 Mockito.any());
        verify(clusterRepository, times(0)).getClusterInfoByClusterId(Mockito.any(),
                                                                      Mockito.anyBoolean());
        verifyNoInteractions(functionDeploymentStagesService);

    }

    @Test
    void terminateInstances_withClusterInfoFetchingFailed_terminatesWithoutException() {
        // Prepare
        var runningInstance = TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                  SpotInstanceRequestState.ACTIVE,
                                                                  Instant.now(),
                                                                  ResourceProvider.BYOC);

        List<GenericMetric> genericMetricList = new ArrayList<>();
        when(clusterRepository.getClusterInfoByClusterId(DUMMY_ZONE, true)).thenReturn(
                Optional.empty());

        // Act
        var terminationResponse =
                byocTerminateService.terminateInstances(Set.of(runningInstance),
                                                             new HashMap<>());

        // Assert
        verify(auditService, times(1)).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                                     Mockito.any());
        verify(instanceV2Repository, times(1)).update(Mockito.any());
        verify(instanceServiceHelper, times(0)).sendMessageToSqsQueue(Mockito.any(), Mockito.any(),
                                                                 Mockito.any());
        verify(clusterRepository, times(1)).getClusterInfoByClusterId(DUMMY_ZONE, true);
        verifyNoInteractions(functionDeploymentStagesService);
    }

    @Test
    void terminateInstances_withDbUpdateFailed_ignoreExceptionReturnSuccess() {
        // Prepare
        var runningInstance = TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                  SpotInstanceRequestState.ACTIVE,
                                                                  Instant.now(),
                                                                  ResourceProvider.BYOC);

        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterName(DUMMY_ZONE);
        clusterEntity.setClusterId(DUMMY_ZONE);

        var byocTerminateMessage = ByocTerminatePodMessageModel.builder()
                .action(TERMINATE_INSTANCES.getRequestAction()).availabilityZone(DUMMY_ZONE)
                .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                .instanceIds(Set.of(DUMMY_RUNNING_INSTANCE_ID)).ncaId(runningInstance.getNcaId())
                .traceParent(DUMMY_TRACE_PARENT)
                .traceState(DUMMY_TRACE_STATE)
                .build();

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_ZONE, true)).thenReturn(
                Optional.of(clusterEntity));

        doNothing().when(instanceServiceHelper)
                .sendTerminateMessage(clusterEntity.getTerminationQueueUrl(),
                                      List.of(byocTerminateMessage),
                                      TERMINATION_MESSAGE_PREFIX,
                                      clusterEntity.getClusterId());

        doThrow(new RuntimeException("dummy-exception")).when(instanceV2Repository)
                .update(Mockito.any());
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                CloudProvider.AZURE.toString()).getLaunchSpecification()).when(
                instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());

        // Act
        var terminationResponse = byocTerminateService.terminateInstances(Set.of(runningInstance),
                                                                               new HashMap<>());

        // Assert
        var expectedInstanceIds = Set.of(DUMMY_RUNNING_INSTANCE_ID);
        for (TerminateInstancesResponse.TerminatingInstance terminatingInstance : terminationResponse.getTerminatingInstances()) {
            Assertions.assertTrue(
                    expectedInstanceIds.contains(terminatingInstance.getInstanceId()));
            Assertions.assertEquals(terminatingInstance.getCurrentState().getName(),
                                    SpotInstanceInternalState.SHUTTING_DOWN.getStateName());
        }
        verify(telemetryEventClient, times(1)).triggerEvent(Mockito.any());
        verify(auditService, times(0)).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                                     Mockito.any());
        verify(instanceV2Repository, times(1)).update(Mockito.any());
        verify(instanceServiceHelper, times(1)).sendTerminateMessage(
                clusterEntity.getTerminationQueueUrl(), List.of(byocTerminateMessage),
                TERMINATION_MESSAGE_PREFIX, clusterEntity.getClusterId());
        verify(clusterRepository, times(1)).getClusterInfoByClusterId(DUMMY_ZONE, true);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
        verify(functionDeploymentStagesService).sendFunctionDeploymentStage(Mockito.argThat(arg -> arg.getInstanceId().equals(DUMMY_RUNNING_INSTANCE_ID)),
                                                                            eq(FndsStages.REQUESTING_TERMINATION.toString()));
    }

    @Test
    void terminateInstances_withSqsInsertFailed_throwException() {
        // Prepare
        var runningInstance = TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                  SpotInstanceRequestState.ACTIVE,
                                                                  Instant.now(),
                                                                  ResourceProvider.BYOC);

        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_ZONE);

        var byocTerminateMessage = ByocTerminatePodMessageModel.builder()
                .action(TERMINATE_INSTANCES.getRequestAction())
                .availabilityZone(clusterEntity.getClusterName())
                .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                .instanceIds(Set.of(DUMMY_RUNNING_INSTANCE_ID)).ncaId(runningInstance.getNcaId())
                .traceParent(DUMMY_TRACE_PARENT)
                .traceState(DUMMY_TRACE_STATE)
                .build();

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_ZONE, true)).thenReturn(
                Optional.of(clusterEntity));

        doThrow(new RuntimeException("dummy-exception")).when(instanceServiceHelper)
                .sendTerminateMessage(
                        eq(clusterEntity.getTerminationQueueUrl()),
                        eq(List.of(byocTerminateMessage)),
                        eq(TERMINATION_MESSAGE_PREFIX), eq(clusterEntity.getClusterId()));

        doReturn(getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                CloudProvider.AZURE.toString()).getLaunchSpecification()).when(
                instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());

        // Act
       Exception exception = assertThrows(RuntimeException.class, () -> {
            byocTerminateService.terminateInstances(Set.of(runningInstance), new HashMap<>());
        });

        // Assert
        assertEquals("dummy-exception", exception.getMessage());
        verify(instanceServiceHelper, times(1)).sendTerminateMessage(
                clusterEntity.getTerminationQueueUrl(), List.of(byocTerminateMessage),
                TERMINATION_MESSAGE_PREFIX, clusterEntity.getClusterId());
        verify(clusterRepository, times(1)).getClusterInfoByClusterId(DUMMY_ZONE, true);
        verifyNoInteractions(functionDeploymentStagesService);
    }

    @Test
    void terminateInstanceRequests_withInstancesFromSameRequest_returnsSuccess() {
        // Prepare
        var runningInstance = TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                  SpotInstanceRequestState.ACTIVE,
                                                                  Instant.now(),
                                                                  ResourceProvider.BYOC);

        var startedInstance = TestUtil.getDummyInstanceEntity(
                SpotInstanceInternalState.STARTING,
                SpotInstanceRequestState.ACTIVE, Instant.now(), ResourceProvider.BYOC);

        Map<String, InstanceV2Entity> runningInstanceEntityMap = new HashMap<>();
        runningInstanceEntityMap.put(DUMMY_RUNNING_INSTANCE_ID, runningInstance);
        runningInstanceEntityMap.put(DUMMY_STARTING_INSTANCE_ID, startedInstance);

        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_ZONE);

        var byocTerminateMessage = ByocTerminatePodMessageModel.builder()
                .action(TERMINATE_INSTANCES.getRequestAction())
                .availabilityZone(clusterEntity.getClusterName())
                .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                .instanceIds(Set.of(DUMMY_RUNNING_INSTANCE_ID, DUMMY_STARTING_INSTANCE_ID))
                .traceParent(DUMMY_TRACE_PARENT)
                .traceState(DUMMY_TRACE_STATE)
                .ncaId(startedInstance.getNcaId()).build();

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_ZONE, true)).thenReturn(
                Optional.of(clusterEntity));

        doNothing().when(instanceServiceHelper)
                .sendTerminateMessage(clusterEntity.getTerminationQueueUrl(),
                                      List.of(byocTerminateMessage),
                                      TERMINATION_MESSAGE_PREFIX,
                                      clusterEntity.getClusterId());

        doNothing().when(instanceV2Repository).update(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.any());
        doReturn(getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                CloudProvider.AZURE.toString()).getLaunchSpecification()).when(
                instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());

        // Act
        var terminationResponse = byocTerminateService.terminateInstanceRequests(runningInstanceEntityMap,
                                                                              new HashMap<>());

        // Assert
        var expectedInstanceIds = Set.of(DUMMY_STARTING_INSTANCE_ID, DUMMY_RUNNING_INSTANCE_ID);
        for (TerminateInstancesResponse.TerminatingInstance terminatingInstance : terminationResponse.getTerminatingInstances()) {
            Assertions.assertTrue(
                    expectedInstanceIds.contains(terminatingInstance.getInstanceId()));
            Assertions.assertEquals(terminatingInstance.getCurrentState().getName(),
                                    SpotInstanceInternalState.SHUTTING_DOWN.getStateName());
        }
        verify(telemetryEventClient, times(1)).triggerEvent(Mockito.any());
        verify(auditService, times(2)).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                                     Mockito.any());
        verify(instanceV2Repository, times(2)).update(Mockito.any());
        verify(instanceServiceHelper, times(1)).sendTerminateMessage(
                clusterEntity.getTerminationQueueUrl(), List.of(byocTerminateMessage),
                TERMINATION_MESSAGE_PREFIX, clusterEntity.getClusterId());
        verify(clusterRepository, times(1)).getClusterInfoByClusterId(DUMMY_ZONE, true);
        verify(functionDeploymentStagesService).sendFunctionDeploymentStage(Mockito.argThat(arg -> arg.getInstanceId().equals(DUMMY_RUNNING_INSTANCE_ID)),
                                                                            eq(FndsStages.REQUESTING_TERMINATION.toString()));
    }

    @Test
    void terminateInstanceRequests_withSqsInsertFailed_throwsException() {
        // Prepare
        var runningInstance = TestUtil.getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                  SpotInstanceRequestState.ACTIVE,
                                                                  Instant.now(),
                                                                  ResourceProvider.BYOC);

        Map<String, InstanceV2Entity> runningInstanceEntityMap = new HashMap<>();
        runningInstanceEntityMap.put(DUMMY_RUNNING_INSTANCE_ID, runningInstance);

        var clusterEntity = TestUtil.getDummyClusterEntity();
        clusterEntity.setClusterId(DUMMY_ZONE);

        var byocTerminateMessage = ByocTerminatePodMessageModel.builder()
                .action(TERMINATE_INSTANCES.getRequestAction())
                .availabilityZone(clusterEntity.getClusterName())
                .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                .instanceIds(Set.of(DUMMY_RUNNING_INSTANCE_ID)).ncaId(runningInstance.getNcaId())
                .traceParent(DUMMY_TRACE_PARENT)
                .traceState(DUMMY_TRACE_STATE)
                .build();

        when(clusterRepository.getClusterInfoByClusterId(DUMMY_ZONE, true)).thenReturn(
                Optional.of(clusterEntity));

        doThrow(new RuntimeException("dummy-exception")).when(instanceServiceHelper)
                .sendTerminateMessage(clusterEntity.getTerminationQueueUrl(),
                                      List.of(byocTerminateMessage),
                                      TERMINATION_MESSAGE_PREFIX,
                                      clusterEntity.getClusterId());

        doReturn(getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                CloudProvider.AZURE.toString()).getLaunchSpecification()).when(
                instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());


        // Act
        Exception exception = assertThrows(RuntimeException.class, () -> {
            byocTerminateService.terminateInstanceRequests(runningInstanceEntityMap, new HashMap<>());
        });

        // Assert
        assertEquals("dummy-exception", exception.getMessage());
        verify(instanceServiceHelper, times(1)).sendTerminateMessage(
                clusterEntity.getTerminationQueueUrl(), List.of(byocTerminateMessage),
                TERMINATION_MESSAGE_PREFIX, clusterEntity.getClusterId());
        verify(clusterRepository, times(1)).getClusterInfoByClusterId(DUMMY_ZONE, true);
        verifyNoInteractions(functionDeploymentStagesService);
    }
}