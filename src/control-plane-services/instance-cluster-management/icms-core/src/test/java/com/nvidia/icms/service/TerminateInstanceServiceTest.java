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
package com.nvidia.icms.service;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceState;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleService;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import org.junit.jupiter.api.Assertions;
import com.nvidia.icms.service.workers.WorkerIdentifierService;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_RUNNING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_STARTING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.getDummyClientRequestDataModel;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceEntity;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceRequestEntity;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

@ExtendWith(MockitoExtension.class)
class TerminateInstanceServiceTest {

    @Mock
    private InstanceLifecycleService instanceLifecycleService;

    @Mock
    private ByocService byocService;

    @Mock
    private InstanceV2Repository instanceV2Repository;

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private AppAuditService auditService;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    @Mock
    private WorkerIdentifierService workerIdentifierService;

    private TerminateInstanceService terminateInstanceService;

    private Instant dummyInstant;

    @BeforeEach
    void init() {
        dummyInstant = Instant.now();
        terminateInstanceService = new TerminateInstanceService(instanceLifecycleService, byocService,
                                                        instanceRequestV2Repository,
                                                        instanceV2Repository,
                icmsConfigurationProperties,
                                                        auditService, telemetryEventClient,
                                                        instanceServiceHelper,
                                                        ComputePlatformTestFixtures.nonByocComputePlatformService(),
                                                        workerIdentifierService);
    }


    @Test
    void terminateInstances_withValidInstances_returnsSuccess() {

        // Prepare
        var instanceIds = Set.of(DUMMY_RUNNING_INSTANCE_ID, DUMMY_STARTING_INSTANCE_ID);

        var byocNewInstanceEntity = getDummyInstanceEntity(
                SpotInstanceInternalState.STARTING, SpotInstanceRequestState.ACTIVE, dummyInstant,
                ResourceProvider.BYOC);

        var nonByocInstanceEntity = getDummyInstanceEntity(
                SpotInstanceInternalState.RUNNING, SpotInstanceRequestState.ACTIVE, dummyInstant,
                ResourceProvider.OCI);

        var byocTerminateResponse = TerminateInstancesResponse.TerminatingInstance.builder()
                .instanceId(DUMMY_STARTING_INSTANCE_ID)
                .previousState(SpotInstanceState.builder()
                                       .code(SpotInstanceInternalState.getStateCode(
                                               SpotInstanceInternalState.RUNNING))
                                       .name(SpotInstanceInternalState.RUNNING.getStateName())
                                       .build())
                .currentState(SpotInstanceState.builder()
                                      .code(SpotInstanceInternalState.getStateCode(
                                              SpotInstanceInternalState.SHUTTING_DOWN))
                                      .name(SpotInstanceInternalState.SHUTTING_DOWN.getStateName())
                                      .build())
                .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                .build();

        var nonByocTerminateResponse = TerminateInstancesResponse.TerminatingInstance.builder()
                .instanceId(DUMMY_RUNNING_INSTANCE_ID)
                .previousState(SpotInstanceState.builder()
                                       .code(SpotInstanceInternalState.getStateCode(
                                               SpotInstanceInternalState.RUNNING))
                                       .name(SpotInstanceInternalState.RUNNING.getStateName())
                                       .build())
                .currentState(SpotInstanceState.builder()
                                      .code(SpotInstanceInternalState.getStateCode(
                                              SpotInstanceInternalState.SHUTTING_DOWN))
                                      .name(SpotInstanceInternalState.SHUTTING_DOWN.getStateName())
                                      .build())
                .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                .build();

        when(instanceV2Repository.findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                                    new HashSet<>(
                                                                            instanceIds))).thenReturn(
                List.of(byocNewInstanceEntity, nonByocInstanceEntity));

        when(byocService.terminateInstances(Set.of(byocNewInstanceEntity),
                                                 Map.of())).thenReturn(
                new TerminateInstancesResponse(List.of(byocTerminateResponse)));

        when(instanceLifecycleService.terminateInstances(DUMMY_CUSTOMER_ID,
                                                    Set.of(nonByocInstanceEntity),
                                                    Map.of())).thenReturn(
                new TerminateInstancesResponse(List.of(nonByocTerminateResponse)));

        // Act
        var terminateResponse =
                terminateInstanceService.terminateInstances(DUMMY_CUSTOMER_ID, instanceIds,
                                                             Map.of());

        // Assert
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertNotEquals(0, terminateResponse.getTerminatingInstances().size());

        for (TerminateInstancesResponse.TerminatingInstance terminatingInstance : terminateResponse.getTerminatingInstances()) {
            Assertions.assertTrue(instanceIds.contains(terminatingInstance.getInstanceId()));
            Assertions.assertEquals(terminatingInstance.getCurrentState().getName(),
                                    SpotInstanceInternalState.SHUTTING_DOWN.getStateName());
        }

        verify(instanceV2Repository).findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                                       new HashSet<>(
                                                                               instanceIds));
        verify(byocService).terminateInstances(Set.of(byocNewInstanceEntity),
                                                    Map.of());
        verify(instanceLifecycleService).terminateInstances(DUMMY_CUSTOMER_ID,
                                                       Set.of(nonByocInstanceEntity),
                                                       Map.of());
    }

    @Test
    void terminateInstances_withAllInvalidInstanceIds_throwException() {

        // Prepare
        var instanceIds = Set.of("id1");
        when(instanceV2Repository.findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                                    instanceIds)).thenReturn(
                List.of());

        // Act
        var exception = Assertions.assertThrows(IcmsNotFoundException.class, () -> {
            terminateInstanceService.terminateInstances(DUMMY_CUSTOMER_ID, Set.of("id1"),
                                                         Map.of());
        });

        // Assert
        Assertions.assertEquals(String.format("Invalid instance ids - %s", instanceIds),
                                exception.getBody().getDetail());
        verify(instanceV2Repository).findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                                       Set.of("id1"));
    }

    @Test
    void terminateInstances_withFewInvalidInstanceIds_throwException() {

        // Prepare
        var instanceIds = Set.of("id1", DUMMY_RUNNING_INSTANCE_ID);
        when(instanceV2Repository.findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                                    instanceIds)).thenReturn(
                List.of(getDummyInstanceEntity(
                        SpotInstanceInternalState.RUNNING, SpotInstanceRequestState.ACTIVE,
                        dummyInstant,
                        ResourceProvider.OCI)));

        // Act
        var exception = Assertions.assertThrows(IcmsNotFoundException.class, () -> {
            terminateInstanceService.terminateInstances(DUMMY_CUSTOMER_ID,
                                                         new HashSet<>(instanceIds), Map.of());
        });

        // Assert
        Assertions.assertEquals(String.format("Invalid instance ids - %s", List.of("id1")),
                                exception.getBody().getDetail());
        verify(instanceV2Repository).findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                                       instanceIds);
    }

    @Test
    void terminateInstanceRequests_validRequests_returnsSuccess() {

        // Prepare
        var nonByocInstanceId = "i1";
        var byocInstanceId = "i3";

        var nonByocRequestId = "r1";
        var byocRequestId = "r3";

        ClientRequestDataModel clientRequestDataModel1 =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, "r1",
                        CloudProvider.OCI.toString());
        ClientRequestDataModel clientRequestDataModel2 =
                getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID, "r3",
                        CloudProvider.OCI.toString());

        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        var userProvidedRequestIds = Set.of(nonByocRequestId, byocRequestId);

        var byocNewInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                                 SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                                 byocRequestId,
                                                                 dummyInstant,
                                                                 ResourceProvider.BYOC);

        var nonByocInstanceEntity = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                               SpotInstanceRequestState.ACTIVE,
                                                               dummyInstant, ResourceProvider.OCI);
        nonByocInstanceEntity.setInstanceId(nonByocInstanceId);
        nonByocInstanceEntity.setRequestId(nonByocRequestId);

        var byocInstanceEntity = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                SpotInstanceRequestState.ACTIVE,
                                                                dummyInstant,
                                                                ResourceProvider.BYOC);
        byocInstanceEntity.setInstanceId(byocInstanceId);
        byocInstanceEntity.setRequestId(byocRequestId);

        var nonByocEntityMap = Map.of(nonByocInstanceId, nonByocInstanceEntity);
        var byocEntityMap = Map.of(byocInstanceId, byocInstanceEntity);

        var nonByocTerminateResponse = getTerminateInstanceResponse(nonByocRequestId, nonByocInstanceId);
        var byocTerminateResponse = getTerminateInstanceResponse(byocRequestId, byocInstanceId);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(byocNewInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(byocNewInstanceRequestEntity));


        when(instanceV2Repository.findInstancesByCustomerAndRequestIds(null,
                                                                   Set.of(nonByocRequestId))).thenReturn(
                List.of(nonByocInstanceEntity));


        when(instanceLifecycleService.terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap),
                                                   any())).thenReturn(
                new TerminateInstancesResponse(List.of(nonByocTerminateResponse)));

        doNothing().when(auditService)
                .sendAuditEventForInstanceRequest(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(instanceRequestV2Repository).updateRequests(any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                        clientRequestDataModel1.getLaunchSpecification())
                .thenReturn(clientRequestDataModel2.getLaunchSpecification());

        // Act
        var terminateResponse =
                terminateInstanceService.terminateInstanceRequests(DUMMY_CUSTOMER_ID,
                                                            userProvidedRequestIds,
                                                            new HashMap<>());

        // Assert
        var expectedInstanceIds = Set.of(nonByocInstanceId, byocInstanceId);
        for (TerminateInstancesResponse.TerminatingInstance terminatingInstance : terminateResponse.getTerminatingInstances()) {
            Assertions.assertTrue(
                    userProvidedRequestIds.contains(terminatingInstance.getRequestId()));
            Assertions.assertTrue(
                    expectedInstanceIds.contains(terminatingInstance.getInstanceId()));
            Assertions.assertEquals(terminatingInstance.getCurrentState().getName(),
                                    SpotInstanceInternalState.SHUTTING_DOWN.getStateName());
        }

        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(nonByocRequestId,
                                                                   DUMMY_CUSTOMER_ID);

        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(byocRequestId,
                                                                   DUMMY_CUSTOMER_ID);

        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null,
                                                                      Set.of(nonByocRequestId));
        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null,
                                                                      Set.of(byocRequestId));

        verify(instanceLifecycleService).terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap),
                                                      any());

        verify(auditService, times(2)).sendAuditEventForInstanceRequest(any(), any(),
                                                                    any());
        verify(telemetryEventClient, times(2)).triggerEvent(any());
        verify(instanceRequestV2Repository, times(2)).updateRequests(any());
        verify(instanceServiceHelper, times(2)).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void terminateInstanceRequests_withClosedAndCanceledRequestIds_returnsEmptyList() {

        // Prepare
        var userProvidedRequestIds = Set.of("r1", "r3");

        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.CLOSED,
                                                             SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                             "r1",
                                                             dummyInstant, ResourceProvider.OCI);

        var byocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.CLOSED,
                                                              SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                              "r3",
                                                              dummyInstant, ResourceProvider.BYOC);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(byocInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(byocInstanceRequestEntity));


        // Act
        var response = terminateInstanceService.terminateInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                   userProvidedRequestIds,
                                                                   new HashMap<>());

        // Assert
        assertNotNull(response);
        assertTrue(response.getTerminatingInstances().isEmpty());
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                   DUMMY_CUSTOMER_ID);
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(byocInstanceRequestEntity.getRequestId(),
                                                                   DUMMY_CUSTOMER_ID);

        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(),
                                                                any());
        verify(byocService, times(0)).terminateInstanceRequests(any(),
                                                             any());
        verify(instanceV2Repository, times(0)).findInstancesByCustomerAndRequestIds(any(),
                                                                                Mockito.anySet());
    }

    @Test
    void terminateInstanceRequests_withOpenRequestIdsAndWithoutRunningInstances_returnsEmptyListByClosingRequestId() {

        // Prepare
        var userProvidedRequestIds = Set.of("r1", "r3");
        ClientRequestDataModel clientRequestDataModel1 =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, "r1",
                        CloudProvider.OCI.toString());
        ClientRequestDataModel clientRequestDataModel2 =
                getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID, "r2",
                        CloudProvider.OCI.toString());

        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                             SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                             "r1",
                                                             dummyInstant, ResourceProvider.OCI);

        var byocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                              SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                              "r3",
                                                              dummyInstant, ResourceProvider.BYOC);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(byocInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(byocInstanceRequestEntity));

        doNothing().when(instanceRequestV2Repository).updateRequests(List.of(nonByocInstanceRequestEntity));
        doNothing().when(instanceRequestV2Repository).updateRequests(List.of(byocInstanceRequestEntity));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                        clientRequestDataModel1.getLaunchSpecification())
                .thenReturn(clientRequestDataModel2.getLaunchSpecification());

        // Act
        var response = terminateInstanceService.terminateInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                   userProvidedRequestIds,
                                                                   new HashMap<>());

        // Assert
        assertNotNull(response);
        assertTrue(response.getTerminatingInstances().isEmpty());
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                   DUMMY_CUSTOMER_ID);
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(byocInstanceRequestEntity.getRequestId(),
                                                                   DUMMY_CUSTOMER_ID);
        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(),
                                                                any());
        verify(byocService, times(0)).terminateInstanceRequests(any(),
                                                             any());
        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null,
                                                                      Set.of("r1"));
        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null,
                                                                      Set.of("r3"));

        verify(instanceRequestV2Repository).updateRequests(List.of(nonByocInstanceRequestEntity));
        verify(instanceRequestV2Repository).updateRequests(List.of(byocInstanceRequestEntity));
        verify(instanceServiceHelper, times(2)).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void terminateInstanceRequests_withOpenRequestWithoutInstanceIds_returnsEmptyListByClosingRequestId() {

        // Prepare
        var userProvidedRequestIds = Set.of("r1", "r3");
        ClientRequestDataModel clientRequestDataModel1 =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, "r1",
                        CloudProvider.OCI.toString());
        ClientRequestDataModel clientRequestDataModel2 =
                getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID, "r2",
                        CloudProvider.OCI.toString());

        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                             SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                             "r1",
                                                             dummyInstant, ResourceProvider.OCI);


        var byocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                              SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                              "r3",
                                                              dummyInstant, ResourceProvider.BYOC);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                  DUMMY_CUSTOMER_ID)).thenReturn(
                Optional.of(nonByocInstanceRequestEntity));
        when(instanceRequestV2Repository.findRequestByIdAndCustomer(byocInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(
                Optional.of(byocInstanceRequestEntity));


        doNothing().when(instanceRequestV2Repository).updateRequests(List.of(nonByocInstanceRequestEntity));
        doNothing().when(instanceRequestV2Repository).updateRequests(List.of(byocInstanceRequestEntity));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                        clientRequestDataModel1.getLaunchSpecification())
                .thenReturn(clientRequestDataModel2.getLaunchSpecification());

        // Act
        var response = terminateInstanceService.terminateInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                   userProvidedRequestIds,
                                                                   new HashMap<>());

        // Assert
        assertNotNull(response);
        assertTrue(response.getTerminatingInstances().isEmpty());
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                             DUMMY_CUSTOMER_ID);
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(byocInstanceRequestEntity.getRequestId(),
                                                                   DUMMY_CUSTOMER_ID);

        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(),
                                                                any());
        verify(byocService, times(0)).terminateInstanceRequests(any(), any());
        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null,
                                                                      Set.of("r1"));
        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null,
                                                                      Set.of("r3"));

        verify(instanceRequestV2Repository).updateRequests(List.of(nonByocInstanceRequestEntity));
        verify(instanceRequestV2Repository).updateRequests(List.of(byocInstanceRequestEntity));
        verify(instanceServiceHelper, times(2)).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void terminateInstanceRequests_withOpenRequestWithoutInstanceIdsAndRequestUpdateFails_returnsException() {

        // Prepare
        var userProvidedRequestIds = Set.of("r1", "r3");

        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                             SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                             "r1",
                                                             dummyInstant, ResourceProvider.OCI);

        var byocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                              SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                              "r3",
                                                              dummyInstant, ResourceProvider.BYOC);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(byocInstanceRequestEntity.getRequestId(),
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(byocInstanceRequestEntity));

        doThrow(new IcmsInternalServerException("dummy-exception")).when(instanceRequestV2Repository)
                .updateRequests(List.of(nonByocInstanceRequestEntity));

        // Act
        var exception = Assertions.assertThrows(IcmsInternalServerException.class, () -> {
            terminateInstanceService.terminateInstanceRequests(DUMMY_CUSTOMER_ID, userProvidedRequestIds,
                                                        new HashMap<>());
        });

        // Assert
        Assertions.assertEquals("dummy-exception",
                                exception.getBody().getDetail());
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(nonByocInstanceRequestEntity.getRequestId(),
                                                                   DUMMY_CUSTOMER_ID);
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(byocInstanceRequestEntity.getRequestId(),
                                                                   DUMMY_CUSTOMER_ID);

        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(),
                                                                any());
        verify(byocService, times(0)).terminateInstanceRequests(any(), any());
        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null, Set.of("r1"));
        verify(instanceV2Repository, times(0)).findInstancesByCustomerAndRequestIds(DUMMY_CUSTOMER_ID,
                                                                                Set.of("r3"));

        verify(instanceRequestV2Repository).updateRequests(List.of(nonByocInstanceRequestEntity));
        verify(instanceRequestV2Repository, times(0)).updateRequests(List.of(byocInstanceRequestEntity));
    }

    @Test
    void terminateInstanceRequests_withAllRequestIdsInvalid_throwsException() {

        // Prepare
        String requestId = "r1";
        var userProvidedRequestIds = Set.of(requestId);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(requestId,
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.empty());

        // Act
        var exception = Assertions.assertThrows(IcmsNotFoundException.class, () -> {
            terminateInstanceService.terminateInstanceRequests(DUMMY_CUSTOMER_ID, userProvidedRequestIds,
                                                        new HashMap<>());
        });

        // Assert
        Assertions.assertEquals("Invalid request ids - [r1]", exception.getBody().getDetail());

        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(requestId,
                                                                             DUMMY_CUSTOMER_ID);
        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(),
                                                                any());
    }

    @Test
    void terminateInstanceRequests_withFewRequestIdsInvalid_throwsException() {

        // Prepare
        var userProvidedRequestIds = Set.of("r1", "r2");

        var nonByocNewInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                                                SpotRequestStatusCode.PENDING_FULFILLMENT,
                                                                "r2",
                                                                dummyInstant, ResourceProvider.OCI);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocNewInstanceRequestEntity.getRequestId(),
                                                                          DUMMY_CUSTOMER_ID)).thenReturn(Optional.of(nonByocNewInstanceRequestEntity));

        when(instanceRequestV2Repository.findRequestByIdAndCustomer("r1",
                                                                DUMMY_CUSTOMER_ID)).thenReturn(Optional.empty());

        // Act
        var exception = Assertions.assertThrows(IcmsNotFoundException.class, () -> {
            terminateInstanceService.terminateInstanceRequests(DUMMY_CUSTOMER_ID, userProvidedRequestIds,
                                                        new HashMap<>());
        });

        // Assert
        Assertions.assertEquals("Invalid requestIds provided - [r1]",
                                exception.getBody().getDetail());

        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(),
                                                                any());
    }

    @Test
    void updateRequestStateToClosedFromAsyncTerminateTask_success() {
        // Prepare
        var instanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                                          SpotRequestStatusCode.FULFILLED, "r1",
                                                          dummyInstant, ResourceProvider.OCI);
        ClientRequestDataModel clientRequestDataModel1 =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, "r1",
                        CloudProvider.OCI.toString());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                clientRequestDataModel1.getLaunchSpecification());

        // Act
        terminateInstanceService.updateRequestStateToClosedFromAsyncTerminateTask(instanceRequestEntity);
        // Assert
        Assertions.assertEquals(SpotRequestStatusCode.INSTANCE_TERMINATED_BY_SERVICE.toString(),
                                instanceRequestEntity.getStatusCode());
        Assertions.assertEquals(SpotInstanceRequestState.CLOSED,
                                instanceRequestEntity.getState());
        Assertions.assertEquals(
                "Your instance request is closed as lifetime of request has been expired",
                instanceRequestEntity.getStatusMessage());
        verify(instanceRequestV2Repository).updateRequests(any());
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    private TerminateInstancesResponse.TerminatingInstance getTerminateInstanceResponse(
            String requestId, String instanceId) {
        return TerminateInstancesResponse.TerminatingInstance.builder()
                .instanceId(instanceId)
                .previousState(SpotInstanceState.builder()
                                       .code(SpotInstanceInternalState.getStateCode(
                                               SpotInstanceInternalState.RUNNING))
                                       .name(SpotInstanceInternalState.RUNNING.getStateName())
                                       .build())
                .currentState(SpotInstanceState.builder()
                                      .code(SpotInstanceInternalState.getStateCode(
                                              SpotInstanceInternalState.SHUTTING_DOWN))
                                      .name(SpotInstanceInternalState.SHUTTING_DOWN.getStateName())
                                      .build())
                .requestId(requestId)
                .build();
    }

    /**
     * Test: Feature flag ENABLED - Request in ACTIVE state with FULFILLED status
     * Instance has requestState=ACTIVE (normal case)
     * Expected: Instance should be terminated
     */
    @Test
    void terminateInstanceRequests_featureFlagEnabled_activeRequestWithFulfilledStatus_instanceWithActiveState_terminatesInstance() {
        // Prepare
        var nonByocInstanceId = "i1";
        var nonByocRequestId = "r1";

        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, nonByocRequestId,
                        CloudProvider.OCI.toString());

        // Request in ACTIVE state with FULFILLED status (after state transition)
        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                SpotRequestStatusCode.FULFILLED,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        // Instance with requestState=ACTIVE
        var nonByocInstanceEntity = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                SpotInstanceRequestState.ACTIVE,
                dummyInstant, ResourceProvider.OCI);
        nonByocInstanceEntity.setInstanceId(nonByocInstanceId);
        nonByocInstanceEntity.setRequestId(nonByocRequestId);

        var nonByocEntityMap = Map.of(nonByocInstanceId, nonByocInstanceEntity);
        var nonByocTerminateResponse = getTerminateInstanceResponse(nonByocRequestId, nonByocInstanceId);

        // Feature flag enabled
        when(icmsConfigurationProperties.isRequestStateTransitionToActiveEnabled()).thenReturn(true);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID))
                .thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceV2Repository.findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId)))
                .thenReturn(List.of(nonByocInstanceEntity));

        when(instanceLifecycleService.terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap), any()))
                .thenReturn(new TerminateInstancesResponse(List.of(nonByocTerminateResponse)));

        doNothing().when(auditService).sendAuditEventForInstanceRequest(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(instanceRequestV2Repository).updateRequests(any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(any()))
                .thenReturn(clientRequestDataModel.getLaunchSpecification());

        // Act
        var terminateResponse = terminateInstanceService.terminateInstanceRequests(
                DUMMY_CUSTOMER_ID, Set.of(nonByocRequestId), new HashMap<>());

        // Assert
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertEquals(1, terminateResponse.getTerminatingInstances().size());
        Assertions.assertEquals(nonByocInstanceId,
                terminateResponse.getTerminatingInstances().get(0).getInstanceId());

        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId));
        verify(instanceLifecycleService).terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap), any());
    }

    /**
     * Test: Feature flag DISABLED - Request in ACTIVE state should NOT be processed
     * Expected: Request should not be added to termination list
     */
    @Test
    void terminateInstanceRequests_featureFlagDisabled_activeRequest_shouldNotProcessRequest() {
        // Prepare
        var nonByocRequestId = "r1";

        // Request in ACTIVE state (should be skipped when flag is disabled)
        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                SpotRequestStatusCode.FULFILLED,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        // Feature flag disabled
        when(icmsConfigurationProperties.isRequestStateTransitionToActiveEnabled()).thenReturn(false);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID))
                .thenReturn(Optional.of(nonByocInstanceRequestEntity));

        // Act
        var terminateResponse = terminateInstanceService.terminateInstanceRequests(
                DUMMY_CUSTOMER_ID, Set.of(nonByocRequestId), new HashMap<>());

        // Assert - No instances should be terminated
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertTrue(terminateResponse.getTerminatingInstances().isEmpty());

        // Should not query for instances since request is not in valid state
        verify(instanceV2Repository, times(0)).findInstancesByCustomerAndRequestIds(any(), any());
        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(), any());
    }

    /**
     * Test: Feature flag DISABLED - Request in OPEN state with PENDING_FULFILLMENT
     * Instance has requestState=ACTIVE
     * Expected: Instance should be terminated (backward compatibility)
     */
    @Test
    void terminateInstanceRequests_featureFlagDisabled_openRequestWithPendingFulfillment_instanceWithActiveState_terminatesInstance() {
        // Prepare
        var nonByocInstanceId = "i1";
        var nonByocRequestId = "r1";

        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, nonByocRequestId,
                        CloudProvider.OCI.toString());

        // Request in OPEN state with PENDING_FULFILLMENT (legacy flow)
        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        // Instance with requestState=ACTIVE
        var nonByocInstanceEntity = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                SpotInstanceRequestState.ACTIVE,
                dummyInstant, ResourceProvider.OCI);
        nonByocInstanceEntity.setInstanceId(nonByocInstanceId);
        nonByocInstanceEntity.setRequestId(nonByocRequestId);

        var nonByocEntityMap = Map.of(nonByocInstanceId, nonByocInstanceEntity);
        var nonByocTerminateResponse = getTerminateInstanceResponse(nonByocRequestId, nonByocInstanceId);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID))
                .thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceV2Repository.findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId)))
                .thenReturn(List.of(nonByocInstanceEntity));

        when(instanceLifecycleService.terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap), any()))
                .thenReturn(new TerminateInstancesResponse(List.of(nonByocTerminateResponse)));

        doNothing().when(auditService).sendAuditEventForInstanceRequest(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(instanceRequestV2Repository).updateRequests(any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(any()))
                .thenReturn(clientRequestDataModel.getLaunchSpecification());

        // Act
        var terminateResponse = terminateInstanceService.terminateInstanceRequests(
                DUMMY_CUSTOMER_ID, Set.of(nonByocRequestId), new HashMap<>());

        // Assert
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertEquals(1, terminateResponse.getTerminatingInstances().size());

        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId));
        verify(instanceLifecycleService).terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap), any());
    }

    /**
     * Test: Instance with CLOSED requestState should NOT be terminated
     */
    @Test
    void terminateInstanceRequests_instanceWithClosedRequestState_shouldNotTerminate() {
        // Prepare
        var nonByocInstanceId = "i1";
        var nonByocRequestId = "r1";

        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, nonByocRequestId,
                        CloudProvider.OCI.toString());

        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        // Instance with requestState=CLOSED (should not be considered for termination)
        var nonByocInstanceEntity = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                SpotInstanceRequestState.CLOSED,
                dummyInstant, ResourceProvider.OCI);
        nonByocInstanceEntity.setInstanceId(nonByocInstanceId);
        nonByocInstanceEntity.setRequestId(nonByocRequestId);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID))
                .thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceV2Repository.findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId)))
                .thenReturn(List.of(nonByocInstanceEntity));

        doNothing().when(auditService).sendAuditEventForInstanceRequest(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(instanceRequestV2Repository).updateRequests(any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(any()))
                .thenReturn(clientRequestDataModel.getLaunchSpecification());

        // Act
        var terminateResponse = terminateInstanceService.terminateInstanceRequests(
                DUMMY_CUSTOMER_ID, Set.of(nonByocRequestId), new HashMap<>());

        // Assert - No instances should be terminated
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertTrue(terminateResponse.getTerminatingInstances().isEmpty());

        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId));
        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(), any());
    }

    /**
     * Test: Instance with TERMINATED internal state should NOT be terminated
     */
    @Test
    void terminateInstanceRequests_instanceWithTerminatedInternalState_shouldNotTerminate() {
        // Prepare
        var nonByocInstanceId = "i1";
        var nonByocRequestId = "r1";

        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, nonByocRequestId,
                        CloudProvider.OCI.toString());

        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        // Instance already TERMINATED
        var nonByocInstanceEntity = getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                SpotInstanceRequestState.ACTIVE,
                dummyInstant, ResourceProvider.OCI);
        nonByocInstanceEntity.setInstanceId(nonByocInstanceId);
        nonByocInstanceEntity.setRequestId(nonByocRequestId);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID))
                .thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceV2Repository.findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId)))
                .thenReturn(List.of(nonByocInstanceEntity));

        doNothing().when(auditService).sendAuditEventForInstanceRequest(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(instanceRequestV2Repository).updateRequests(any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(any()))
                .thenReturn(clientRequestDataModel.getLaunchSpecification());

        // Act
        var terminateResponse = terminateInstanceService.terminateInstanceRequests(
                DUMMY_CUSTOMER_ID, Set.of(nonByocRequestId), new HashMap<>());

        // Assert - No instances should be terminated (already terminated)
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertTrue(terminateResponse.getTerminatingInstances().isEmpty());

        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId));
        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(), any());
    }

    /**
     * Test: Instance with STARTING internal state should be terminated
     */
    @Test
    void terminateInstanceRequests_instanceWithStartingInternalState_shouldTerminate() {
        // Prepare
        var nonByocInstanceId = "i1";
        var nonByocRequestId = "r1";

        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, nonByocRequestId,
                        CloudProvider.OCI.toString());

        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        // Instance in STARTING state
        var nonByocInstanceEntity = getDummyInstanceEntity(SpotInstanceInternalState.STARTING,
                SpotInstanceRequestState.ACTIVE,
                dummyInstant, ResourceProvider.OCI);
        nonByocInstanceEntity.setInstanceId(nonByocInstanceId);
        nonByocInstanceEntity.setRequestId(nonByocRequestId);

        var nonByocEntityMap = Map.of(nonByocInstanceId, nonByocInstanceEntity);
        var nonByocTerminateResponse = getTerminateInstanceResponse(nonByocRequestId, nonByocInstanceId);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID))
                .thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceV2Repository.findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId)))
                .thenReturn(List.of(nonByocInstanceEntity));

        when(instanceLifecycleService.terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap), any()))
                .thenReturn(new TerminateInstancesResponse(List.of(nonByocTerminateResponse)));

        doNothing().when(auditService).sendAuditEventForInstanceRequest(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(instanceRequestV2Repository).updateRequests(any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(any()))
                .thenReturn(clientRequestDataModel.getLaunchSpecification());

        // Act
        var terminateResponse = terminateInstanceService.terminateInstanceRequests(
                DUMMY_CUSTOMER_ID, Set.of(nonByocRequestId), new HashMap<>());

        // Assert - STARTING instance should be terminated
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertEquals(1, terminateResponse.getTerminatingInstances().size());

        verify(instanceLifecycleService).terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap), any());
    }

    /**
     * Test: Request with PENDING_EVALUATION status should NOT have instances queried
     */
    @Test
    void terminateInstanceRequests_requestWithPendingEvaluationStatus_shouldCloseWithoutQueryingInstances() {
        // Prepare
        var nonByocRequestId = "r1";

        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, nonByocRequestId,
                        CloudProvider.OCI.toString());

        // Request in OPEN state with PENDING_EVALUATION (no instances yet)
        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_EVALUATION,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID))
                .thenReturn(Optional.of(nonByocInstanceRequestEntity));

        // Mock returns empty list for PENDING_EVALUATION (status doesn't match PENDING_FULFILLMENT or FULFILLED)
        when(instanceV2Repository.findInstancesByCustomerAndRequestIds(null, Set.of()))
                .thenReturn(List.of());

        doNothing().when(auditService).sendAuditEventForInstanceRequest(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(instanceRequestV2Repository).updateRequests(any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(any()))
                .thenReturn(clientRequestDataModel.getLaunchSpecification());

        // Act
        var terminateResponse = terminateInstanceService.terminateInstanceRequests(
                DUMMY_CUSTOMER_ID, Set.of(nonByocRequestId), new HashMap<>());

        // Assert - Request should be closed but no instances terminated
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertTrue(terminateResponse.getTerminatingInstances().isEmpty());

        // Should query with empty set since PENDING_EVALUATION doesn't match the status code filter
        verify(instanceV2Repository).findInstancesByCustomerAndRequestIds(null, Set.of());
        verify(instanceLifecycleService, times(0)).terminateInstanceRequests(any(), any(), any());
        verify(instanceRequestV2Repository).updateRequests(any()); // Request should still be closed
    }

    /**
     * Test: Request in OPEN state should work regardless of feature flag (backward compatibility)
     * Note: For OPEN state requests, the feature flag is not checked due to short-circuit evaluation
     * in isRequestInOpenOrActiveState(): OPEN || (flag && ACTIVE) - OPEN is true, so flag is never evaluated
     */
    @Test
    void terminateInstanceRequests_openRequestWorksRegardlessOfFeatureFlag() {
        // Prepare
        var nonByocInstanceId = "i1";
        var nonByocRequestId = "r1";

        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, nonByocRequestId,
                        CloudProvider.OCI.toString());

        // Request in OPEN state (before first instance transitions request to ACTIVE)
        var nonByocInstanceRequestEntity = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT,
                nonByocRequestId,
                dummyInstant, ResourceProvider.OCI);

        var nonByocInstanceEntity = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                SpotInstanceRequestState.ACTIVE,
                dummyInstant, ResourceProvider.OCI);
        nonByocInstanceEntity.setInstanceId(nonByocInstanceId);
        nonByocInstanceEntity.setRequestId(nonByocRequestId);

        var nonByocEntityMap = Map.of(nonByocInstanceId, nonByocInstanceEntity);
        var nonByocTerminateResponse = getTerminateInstanceResponse(nonByocRequestId, nonByocInstanceId);

        // Note: No stubbing for isRequestStateTransitionToActiveEnabled() needed
        // because OPEN state short-circuits the evaluation in isRequestInOpenOrActiveState()

        when(instanceRequestV2Repository.findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID))
                .thenReturn(Optional.of(nonByocInstanceRequestEntity));

        when(instanceV2Repository.findInstancesByCustomerAndRequestIds(null, Set.of(nonByocRequestId)))
                .thenReturn(List.of(nonByocInstanceEntity));

        when(instanceLifecycleService.terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap), any()))
                .thenReturn(new TerminateInstancesResponse(List.of(nonByocTerminateResponse)));

        doNothing().when(auditService).sendAuditEventForInstanceRequest(any(), any(), any());
        doNothing().when(telemetryEventClient).triggerEvent(any());
        doNothing().when(instanceRequestV2Repository).updateRequests(any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(any()))
                .thenReturn(clientRequestDataModel.getLaunchSpecification());

        // Act
        var terminateResponse = terminateInstanceService.terminateInstanceRequests(
                DUMMY_CUSTOMER_ID, Set.of(nonByocRequestId), new HashMap<>());

        // Assert - OPEN request should work regardless of feature flag state
        Assertions.assertNotNull(terminateResponse);
        Assertions.assertEquals(1, terminateResponse.getTerminatingInstances().size());

        verify(instanceLifecycleService).terminateInstanceRequests(eq(DUMMY_CUSTOMER_ID), eq(nonByocEntityMap), any());
    }
}
