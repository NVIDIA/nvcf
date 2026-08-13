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
package com.nvidia.icms.service.internal;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.RUNNING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.STARTING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.TERMINATED;
import static com.nvidia.icms.service.internal.InstanceUpdateService.ignoreSensitiveInformation;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FAILED_CONTAINER_LOG;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_VERSION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_STARTING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_TERMINATION_CAUSE;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static com.nvidia.icms.util.TestUtil.INSTANCE_TYPE_KEY;
import static com.nvidia.icms.util.TestUtil.getDummyInstancePlacementValidationResponse;
import static com.nvidia.icms.util.TestUtil.getDummyReservation;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceEntity;
import static com.nvidia.icms.util.TestUtil.getInstanceUpdateRequestForActiveInstance;
import static com.nvidia.icms.util.TestUtil.getInstanceUpdateRequestForActiveInstanceWithReservationId;
import static com.nvidia.icms.util.TestUtil.getInstanceUpdateRequestForTerminatedState;
import static com.nvidia.icms.util.TestUtil.getInstanceEntityForRunningInstance;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.any;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.eq;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.byoc.ClusterRepository;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.FunctionDeploymentStagesService;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.platform.commons.util.StringUtils;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class InstanceUpdateServiceTest {

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    @Mock
    private InstanceV2Repository instanceV2Repository;

    private InstanceUpdateService instanceUpdateService;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private AppAuditService auditService;

    @Mock
    InternalInstanceServiceHelper internalInstanceServiceHelper;

    @Mock
    InstanceServiceHelper instanceServiceHelper;

    @Mock
    InstanceLifecycleHelper instanceLifecycleHelper;

    @Mock
    ClusterRepository clusterRepository;

    @Mock
    FunctionDeploymentStagesService functionDeploymentStagesService;

    @Mock
    ReservationCapacityValidationHelper reservationCapacityValidationHelper;

    @Mock
    IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    com.nvidia.icms.service.workers.WorkerIdentifierService workerIdentifierService;

    @Captor
    private ArgumentCaptor<InstanceV2Entity> instanceEntityArgumentCaptor;

    private Map<String, Object> auditProps;

    @BeforeEach
    void setup() {
        auditProps = new HashMap<>();
        instanceUpdateService = new InstanceUpdateService(instanceRequestV2Repository,
                instanceV2Repository,
                auditService,
                telemetryEventClient,
                internalInstanceServiceHelper,
                instanceServiceHelper,
                instanceLifecycleHelper,
                functionDeploymentStagesService,
                clusterRepository,
                reservationCapacityValidationHelper,
                icmsConfigurationProperties,
                workerIdentifierService);
    }

    @Test
    void handelInstanceUpdateForOpenRequest_withNewInstanceRegistration_returnsSuccess() {

        // mock request entity to find customer
        var requestEntity = getInstanceRequestV2Entity(
                SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT);
        var requestModel = getDummyClientRequestModel();
        var launchSpecification = requestModel.getLaunchSpecification();
        doReturn(Optional.of(requestEntity)).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);

        doNothing().when(instanceV2Repository).insert(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.STARTED_RUNNING_INSTANCE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID)
                        .withInstanceState(SpotInstanceInternalState.RUNNING.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withMetadata(Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE))
                        .withResourceProvider(ResourceProvider.BYOC)
                        .withRequestState(SpotInstanceRequestState.ACTIVE.toString())
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).withInstanceWaitingTime(0)
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null)
                        .withFunctionName(null)
                        .withTaskName(null)
                        .withGpuName(DUMMY_GPU)
                        .withCapacityType(CapacityType.SPOT.toString())
                        .withZoneName(DUMMY_ZONE));
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(requestModel);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                launchSpecification);

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).insert(instanceEntityArgumentCaptor.capture());
        assertThat(instanceEntityArgumentCaptor.getValue().getDeploymentId())
                .isEqualTo(launchSpecification.getDeploymentId());
        assertThat(instanceEntityArgumentCaptor.getValue().getGpuSpecificationId())
                .isEqualTo(launchSpecification.getGpuSpecificationId());
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).parseRequestInfo(Mockito.any());
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void handelInstanceUpdateForOpenRequest_withStartingStateUpdate_returnsSuccess() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                STARTING);

        doNothing().when(instanceV2Repository).insert(Mockito.any());
        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                  DUMMY_INSTANCE_ID)).thenReturn(
                Optional.empty());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.STARTING_INSTANCE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID)
                        .withInstanceState(SpotInstanceInternalState.STARTING.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withMetadata(Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE))
                        .withRequestState(SpotInstanceRequestState.ACTIVE.toString())
                        .withResourceProvider(ResourceProvider.BYOC).withInstanceWaitingTime(0)
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null)
                        .withFunctionName(null)
                        .withTaskName(null)
                        .withGpuName(DUMMY_GPU)
                        .withCapacityType(CapacityType.SPOT.toString())
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).withZoneName(DUMMY_ZONE));
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).insert(Mockito.any());
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                     DUMMY_INSTANCE_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).parseRequestInfo(Mockito.any());
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @ParameterizedTest
    @EnumSource(value = SpotInstanceInternalState.class, names = {"STARTING", "RUNNING"})
    void handelInstanceUpdateForOpenRequest_withFulfilledRequest_instanceAlreadyExists(
            SpotInstanceInternalState state) {

        // mock request entity to find customer
        InstanceRequestV2Entity instanceRequestEntity = getInstanceRequestV2Entity(
                SpotInstanceRequestState.OPEN, SpotRequestStatusCode.PENDING_FULFILLMENT);
        instanceRequestEntity.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        doReturn(Optional.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                STARTING);

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getDummyInstanceEntity(state, SpotInstanceRequestState.ACTIVE,
                                                       Instant.now(), ResourceProvider.BYOC)));

        doNothing().when(instanceV2Repository).update(Mockito.any(InstanceV2Entity.class));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                        ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        instanceUpdateService.updateInstanceStatus(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                       DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(instanceV2Repository).update(Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @ParameterizedTest
    @EnumSource(value = SpotInstanceInternalState.class, names = {"STARTING", "RUNNING"})
    void handelInstanceUpdateForOpenRequest_withFulfilledRequest_instanceAlreadyTerminated(
            SpotInstanceInternalState state) {

        // mock request entity to find customer
        InstanceRequestV2Entity instanceRequestEntity = getInstanceRequestV2Entity(
                SpotInstanceRequestState.OPEN, SpotRequestStatusCode.PENDING_FULFILLMENT);
        instanceRequestEntity.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        doReturn(Optional.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                state);

        InstanceV2Entity instanceEntity = getDummyInstanceEntity(TERMINATED,
                                                                           SpotInstanceRequestState.ACTIVE,
                                                                           Instant.now(),
                                                                           ResourceProvider.BYOC);

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(instanceEntity));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                        ResourceProvider.BYOC));

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                      () -> instanceUpdateService.updateInstanceStatus(
                                                              DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                              DUMMY_INSTANCE_ID,
                                                              instanceStatusUpdateRequest,
                                                              DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(String.format(
                "Invalid instance state transition from terminated to %s for updating instance status",
                state.getStateName()));

        verify(instanceRequestV2Repository).findRequestById(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
    }

    @Test
    void handelInstanceUpdateForOpenRequest_withRunningStateUpdate_returnsSuccess() {
        // Prepare
        String instanceId = DUMMY_STARTING_INSTANCE_ID;
        String requestId = DUMMY_REQUEST_ID;
        String customer = DUMMY_CUSTOMER_ID;

        InstanceRequestV2Entity instanceRequestEntity = getInstanceRequestV2Entity(
                SpotInstanceRequestState.OPEN, SpotRequestStatusCode.PENDING_FULFILLMENT);
        instanceRequestEntity.setRequestId(requestId);
        instanceRequestEntity.setCustomer(customer);

        InstanceV2Entity dummyInstanceEntity = getDummyInstanceEntity(
                SpotInstanceInternalState.STARTING, SpotInstanceRequestState.ACTIVE, Instant.now(),
                ResourceProvider.BYOC);
        dummyInstanceEntity.setInstanceId(instanceId);
        dummyInstanceEntity.setRequestId(requestId);
        dummyInstanceEntity.setCustomer(customer);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        instanceStatusUpdateRequest.setInstanceIps(Set.of("ip1", ""));

        doReturn(Optional.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestById(requestId);
        doReturn(Optional.of(dummyInstanceEntity)).when(instanceV2Repository)
                .findInstanceByCustomerAndId(customer, instanceId);

        doNothing().when(instanceV2Repository).update(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.STARTED_RUNNING_INSTANCE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(customer)
                        .withInstanceId(instanceId).withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .withInstanceState(SpotInstanceInternalState.RUNNING.getStateName())
                        .withRequestId(requestId).withMetadata(
                                Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE,
                                       EventMetaData.RESOURCE_IPS.getName(), "ip1"))
                        .withRequestState(SpotInstanceRequestState.ACTIVE.toString())
                        .withResourceProvider(ResourceProvider.BYOC).withInstanceWaitingTime(0)
                        .withZoneName(DUMMY_ZONE)
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null));
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                requestId)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act
        instanceUpdateService.updateInstanceStatus(requestId, instanceId,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(instanceRequestV2Repository).findRequestById(requestId);
        verify(instanceV2Repository).findInstanceByCustomerAndId(customer, instanceId);
        verify(instanceV2Repository).update(Mockito.any());
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, requestId);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void updateInstanceStatus_forBYOC_forInstanceCreation_returnsSuccess() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        instanceStatusUpdateRequest.setPlacement(null);

        doNothing().when(instanceV2Repository).insert(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        Map<String, Object> metadata = new HashMap<>();
        metadata.put(EventMetaData.INSTANCE_TYPE.getName(),
                     DUMMY_NON_BYOC_INSTANCE_TYPE);
        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.STARTED_RUNNING_INSTANCE.toString())
                        .withCloudProvider(CloudProvider.GDN)
                        .withResourceProvider(ResourceProvider.BYOC).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID)
                        .withInstanceState(SpotInstanceInternalState.RUNNING.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withMetadata(metadata)
                        .withRequestState(SpotInstanceRequestState.ACTIVE.toString())
                        .withInstanceWaitingTime(0)
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID)
                        .withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .withZoneName(DUMMY_CLUSTER_ID)
                        .withDeploymentId(null)
                        .withCapacityType(CapacityType.SPOT.toString())
                        .withFunctionName(null)
                        .withTaskName(null)
                        .withGpuName(DUMMY_GPU)
                        .withGpuSpecificationId(null));
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        when(internalInstanceServiceHelper.validateInstancePlacement(null, DUMMY_CLUSTER_ID,
                                                                 DUMMY_REQUEST_ID)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        new SpotInstanceStatusUpdateRequest.InstancePlacement(DUMMY_CLUSTER_ID),
                        CloudProvider.GDN, ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).insert(Mockito.any());
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(null, DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).parseRequestInfo(Mockito.any());
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }


    @Test
    void updateInstanceStatus_forInstanceTerminationByZoneForNoCapacity_returnsSuccess() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();
        instanceStatusUpdateRequest.setStatus(
                SpotInstanceStatus.INSTANCE_TERMINATED_NO_CAPACITY);
        instanceStatusUpdateRequest.setHealthInfo(null);

        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.INSTANCE_TERMINATED_BY_ZONE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID).withReasonForTermination(
                                SpotInstanceStatus.INSTANCE_TERMINATED_NO_CAPACITY.toString())
                        .withInstanceState(TERMINATED.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withRequestState(SpotInstanceRequestState.CLOSED.toString())
                        .withInstanceLifeTime(0)
                        .withZoneName(DUMMY_ZONE)
                        .withResourceProvider(ResourceProvider.BYOC)
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID)
                        .withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .withMetadata(Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE,
                                       EventMetaData.TERMINATION_CAUSE.getName(),
                                       DUMMY_TERMINATION_CAUSE))
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null));
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getInstanceEntityForRunningInstance()));
        doNothing().when(instanceV2Repository).update(Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).update(Mockito.argThat(
                instanceEntity -> validateUpdatedInstanceEntity(instanceEntity,
                                                                        DUMMY_INSTANCE_ID,
                                                                        DUMMY_REQUEST_ID,
                                                                        TERMINATED,
                                                                        SpotInstanceRequestState.CLOSED,
                                                                        null)));
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void updateInstanceStatus_forInstanceTerminationByUser_returnsSuccess() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();

        instanceStatusUpdateRequest.setStatus(SpotInstanceStatus.INSTANCE_TERMINATED_BY_USER);
        instanceStatusUpdateRequest.setHealthInfo(null);
        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.INSTANCE_TERMINATED_BY_USER.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID).withReasonForTermination(
                                SpotInstanceStatus.INSTANCE_TERMINATED_BY_USER.toString())
                        .withInstanceState(TERMINATED.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withRequestState(SpotInstanceRequestState.CLOSED.toString())
                        .withInstanceLifeTime(0).withZoneName(DUMMY_ZONE)
                        .withResourceProvider(ResourceProvider.BYOC)
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).withMetadata(
                                Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE,
                                       EventMetaData.TERMINATION_CAUSE.getName(),
                                       DUMMY_TERMINATION_CAUSE))
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null));
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getInstanceEntityForRunningInstance()));
        doNothing().when(instanceV2Repository).update(Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).update(Mockito.argThat(
                instanceEntity -> validateUpdatedInstanceEntity(instanceEntity,
                                                                        DUMMY_INSTANCE_ID,
                                                                        DUMMY_REQUEST_ID,
                                                                        TERMINATED,
                                                                        SpotInstanceRequestState.CLOSED,
                                                                        null)));
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void updateInstanceStatus_withInstanceTerminationByUserStatusButInstanceAlreadyTerminatedByCloudHealthCheck_returnsSuccessWithoutUpdating() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();

        instanceStatusUpdateRequest.setStatus(SpotInstanceStatus.INSTANCE_TERMINATED_BY_USER);

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getDummyInstanceEntity(TERMINATED, SpotInstanceRequestState.CLOSED,
                                                       Instant.now(), ResourceProvider.BYOC)));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);

        verify(instanceV2Repository, times(0)).update(Mockito.any());
        verify(telemetryEventClient, times(0)).triggerEvent(Mockito.any());
        verify(auditService, times(0)).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                                     Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceStatus_forInstanceTerminationByZoneForOtherReason_returnsSuccess() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();
        instanceStatusUpdateRequest.setSystemFailure("system_failure1");

        instanceStatusUpdateRequest.setStatus(
                SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE);
        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.INSTANCE_TERMINATED_BY_ZONE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID).withReasonForTermination(
                                SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE.toString())
                        .withInstanceState(TERMINATED.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withRequestState(SpotInstanceRequestState.CLOSED.toString())
                        .withInstanceLifeTime(0).withZoneName(DUMMY_ZONE)
                        .withResourceProvider(ResourceProvider.BYOC)
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).withMetadata(
                                Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE,
                                       EventMetaData.TERMINATION_CAUSE.getName(),
                                       DUMMY_TERMINATION_CAUSE, EventMetaData.ERROR_LOG.getName(),
                                       DUMMY_FAILED_CONTAINER_LOG,
                                       EventMetaData.DOWNSTREAM_SYSTEM_FAILURE.getName(),
                                       "system_failure1"))
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null));

        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getInstanceEntityForRunningInstance()));
        doNothing().when(instanceV2Repository).update(Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).update(Mockito.argThat(
                instanceEntity -> validateUpdatedInstanceEntity(instanceEntity,
                                                                        DUMMY_INSTANCE_ID,
                                                                        DUMMY_REQUEST_ID,
                                                                        TERMINATED,
                                                                        SpotInstanceRequestState.CLOSED,
                                                                        DUMMY_FAILED_CONTAINER_LOG)));
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void updateInstanceStatus_withInvalidRequestId_returnsNotFoundException() {

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);

        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                "invalid-request-id")).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        IcmsNotFoundException exception = assertThrows(IcmsNotFoundException.class,
                                                      () -> instanceUpdateService.updateInstanceStatus(
                                                              "invalid-request-id",
                                                              DUMMY_INSTANCE_ID,
                                                              instanceStatusUpdateRequest,
                                                              DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Cannot find request with id invalid-request-id");
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                "invalid-request-id");
    }

    @Test
    void updateInstanceStatus_withInvalidRequestState_returnsPreConditionFailedException() {
        // Test with CANCELED state - which is an invalid state for instance updates
        // Note: OPEN and ACTIVE states are now both valid for instance updates
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.CANCELED,
                                                    SpotRequestStatusCode.CANCELED_BEFORE_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                             () -> instanceUpdateService.updateInstanceStatus(
                                                                     DUMMY_REQUEST_ID,
                                                                     DUMMY_INSTANCE_ID,
                                                                     instanceStatusUpdateRequest,
                                                                     DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Invalid state " + SpotInstanceRequestState.CANCELED + " for instance request with id "
                        + DUMMY_REQUEST_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceStatus_withInvalidRequestStatus_returnsPreConditionFailedException() {

        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_EVALUATION))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                             () -> instanceUpdateService.updateInstanceStatus(
                                                                     DUMMY_REQUEST_ID,
                                                                     DUMMY_INSTANCE_ID,
                                                                     instanceStatusUpdateRequest,
                                                                     DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                String.format("Invalid request status %s for instance request with id %s",
                              SpotRequestStatusCode.PENDING_EVALUATION, DUMMY_REQUEST_ID));
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceStatus_invalidActionForNewInstance_returnsConflictException() {

        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        instanceStatusUpdateRequest.setAction(
                SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                      () -> instanceUpdateService.updateInstanceStatus(
                                                              DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                              instanceStatusUpdateRequest,
                                                              DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                String.format("Invalid action %s for updating instance status",
                              SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS.getRequestAction()));
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceStatus_withInvalidRequestStateForNewInstance_returnsConflictException() {

        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        instanceStatusUpdateRequest.setRequestState(SpotInstanceRequestState.CLOSED);
        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                             () -> instanceUpdateService.updateInstanceStatus(
                                                                     DUMMY_REQUEST_ID,
                                                                     DUMMY_INSTANCE_ID,
                                                                     instanceStatusUpdateRequest,
                                                                     DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Invalid request state closed for updating instance status, provided state closed expected state active");
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceStatus_withInvalidInstanceStateForNewInstance_returnsConflictException() {

        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        instanceStatusUpdateRequest.setInstanceState(TERMINATED);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                      () -> instanceUpdateService.updateInstanceStatus(
                                                              DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                              instanceStatusUpdateRequest,
                                                              DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                String.format("Invalid instance state %s for updating instance status",
                              TERMINATED.getStateName()));
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceStatus_forInstanceCreation_errorInDBInsertion_throwsException() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);

        doThrow(new RuntimeException("dummy-error")).when(instanceV2Repository)
                .insert(Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        RuntimeException exception = assertThrows(RuntimeException.class,
                                                  () -> instanceUpdateService.updateInstanceStatus(
                                                          DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                          instanceStatusUpdateRequest,
                                                          DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getMessage()).isEqualTo("dummy-error");

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).insert(Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).parseRequestInfo(Mockito.any());
    }

    @Test
    void updateInstanceStatus_forInstanceTermination_instanceNotFound_insertEntryInDb() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.CANCELED,
                                                    SpotRequestStatusCode.SCHEDULE_EXPIRED))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.empty());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        doNothing().when(instanceV2Repository).insert(Mockito.any());
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                DUMMY_INSTANCE_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceV2Repository).insert(Mockito.any());
        verify(instanceServiceHelper).parseRequestInfo(Mockito.any());
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void updateInstanceStatus_forInstanceTermination_invalidRequestId_throwsException() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();

        InstanceV2Entity instanceEntity = getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId("invalid-request-id");
        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(instanceEntity));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                             () -> instanceUpdateService.updateInstanceStatus(
                                                                     DUMMY_REQUEST_ID,
                                                                     DUMMY_INSTANCE_ID,
                                                                     instanceStatusUpdateRequest,
                                                                     DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                String.format("Invalid requestId %s for updating instance status",
                              DUMMY_REQUEST_ID));

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceStatus_forInstanceTermination_invalidZoneName_throwsException() {
        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();
        instanceStatusUpdateRequest.getPlacement().setAvailabilityZone("abc");

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getInstanceEntityForRunningInstance()));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                             () -> instanceUpdateService.updateInstanceStatus(
                                                                     DUMMY_REQUEST_ID,
                                                                     DUMMY_INSTANCE_ID,
                                                                     instanceStatusUpdateRequest,
                                                                     DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                String.format("Invalid zone %s for updating instance status", "abc"));

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceStatus_forInstanceTermination_errorUpdatingDatabase_throwsException() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getInstanceEntityForRunningInstance()));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                        ResourceProvider.BYOC));
        doThrow(new RuntimeException("dummy-error")).when(instanceV2Repository)
                .update(Mockito.any());

        RuntimeException exception = assertThrows(RuntimeException.class,
                                                  () -> instanceUpdateService.updateInstanceStatus(
                                                          DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                          instanceStatusUpdateRequest,
                                                          DUMMY_CLUSTER_ID, auditProps));

        assertThat(exception.getMessage()).isEqualTo("dummy-error");

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).update(Mockito.any());
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    @ParameterizedTest
    @EnumSource(value = SpotInstanceInternalState.class, names = {"STARTING", "RUNNING"})
    void handelInstanceUpdateForClosedRequest_withRunningInstanceRegistered_throwsException(
            SpotInstanceInternalState instanceInternalState) {
        String instanceId = DUMMY_STARTING_INSTANCE_ID;
        String requestId = DUMMY_REQUEST_ID;
        String customer = DUMMY_CUSTOMER_ID;
        String expectedError = "Instance request with id dummy_request_id is closed";

        InstanceRequestV2Entity instanceRequestEntity = getInstanceRequestV2Entity(
                SpotInstanceRequestState.CLOSED, SpotRequestStatusCode.REQUEST_TERMINATED_BY_USER);
        instanceRequestEntity.setRequestId(requestId);
        instanceRequestEntity.setCustomer(customer);

        SpotInstanceStatusUpdateRequest updateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        updateRequest.setInstanceState(instanceInternalState);
        InstanceV2Entity dummyInstanceEntity = getDummyInstanceEntity(
                instanceInternalState, SpotInstanceRequestState.ACTIVE, Instant.now(),
                ResourceProvider.BYOC);
        dummyInstanceEntity.setInstanceId(instanceId);
        dummyInstanceEntity.setRequestId(requestId);
        dummyInstanceEntity.setCustomer(customer);

        GenericMetric genericMetric = new GenericMetric().withEventName(
                        Events.PRE_CONDITION_FAILED.toString()).withInstanceId(instanceId)
                .withRequestId(requestId).withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .withClusterId(updateRequest.getPlacement().getAvailabilityZone())
                .withFunctionName(null)
                .withTaskName(null)
                .withGpuName(DUMMY_GPU)
                .withCustomer(instanceRequestEntity.getCustomer())
                .withInstanceCount(instanceRequestEntity.getInstanceCount())
                .withResourceProvider(instanceRequestEntity.getResourceProvider())
                .withRequestState(instanceRequestEntity.getState().toString())
                .withCapacityType(updateRequest.getCapacityType())
                .withCloudProvider(CloudProvider.AWS)
                .withError("Trying to register new instance but request is closed")
                .withDeploymentId(instanceRequestEntity.getDeploymentId())
                .withGpuSpecificationId(instanceRequestEntity.getGpuSpecificationId())
                .withMetadata(Map.of(EventMetaData.REQUEST_BODY.getName(),
                               GsonCompatMapper.toJson(ignoreSensitiveInformation(updateRequest)),
                               EventMetaData.REQUEST_STATUS.getName(),
                               instanceRequestEntity.getStatusCode()));

        when(instanceRequestV2Repository.findRequestById(requestId)).thenReturn(
                Optional.of(instanceRequestEntity));
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric));
        when(internalInstanceServiceHelper.validateInstancePlacement(updateRequest.getPlacement(),
                                                                 DUMMY_CLUSTER_ID,
                                                                 DUMMY_REQUEST_ID)).thenReturn(
                getDummyInstancePlacementValidationResponse(updateRequest.getPlacement(),
                                                            CloudProvider.AWS,
                                                            ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        PreConditionFailedException exception = Assertions.assertThrows(
                PreConditionFailedException.class, () -> {
                    instanceUpdateService.updateInstanceStatus(requestId, instanceId,
                                                                   updateRequest, DUMMY_CLUSTER_ID,
                                                                   new HashMap<>());
                });

        assertEquals(expectedError, exception.getBody().getDetail());

        verify(instanceRequestV2Repository).findRequestById(requestId);
        verify(instanceV2Repository, times(0)).update(Mockito.any());
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric));
        verifyNoInteractions(auditService);
        verify(internalInstanceServiceHelper).validateInstancePlacement(updateRequest.getPlacement(),
                                                                    DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void handelInstanceUpdateForClosedRequest_withTerminatedInstanceRegistered_returnsSuccess() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.CLOSED,
                                                    SpotRequestStatusCode.REQUEST_TERMINATED_BY_USER))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();

        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.INSTANCE_TERMINATED_BY_ZONE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID).withReasonForTermination(
                                SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE.toString())
                        .withInstanceState(TERMINATED.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withRequestState(SpotInstanceRequestState.CLOSED.toString())
                        .withInstanceLifeTime(0).withZoneName(DUMMY_ZONE)
                        .withResourceProvider(ResourceProvider.BYOC)
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).withMetadata(
                                Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE,
                                       EventMetaData.TERMINATION_CAUSE.getName(),
                                       DUMMY_TERMINATION_CAUSE, EventMetaData.ERROR_LOG.getName(),
                                       DUMMY_FAILED_CONTAINER_LOG))
                .withDeploymentId(null)
                .withGpuSpecificationId(null));
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getInstanceEntityForRunningInstance()));
        doNothing().when(instanceV2Repository).update(Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).update(Mockito.any());
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }


    @Test
    void handelInstanceUpdateForClosedRequest_withTerminatedInstanceRegisteredAndDbUpdateFailed_throwsException() {

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.CLOSED,
                                                    SpotRequestStatusCode.REQUEST_TERMINATED_BY_USER))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForTerminatedState();

        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(
                Optional.of(getInstanceEntityForRunningInstance()));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));

        doThrow(new IcmsConflictException("dummy_message")).when(instanceV2Repository)
                .update(Mockito.any());

        IcmsConflictException icmsConflictException = assertThrows(IcmsConflictException.class, () -> {
            instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                           instanceStatusUpdateRequest,
                                                           DUMMY_CLUSTER_ID, auditProps);
        });

        assertThat(icmsConflictException.getBody().getDetail()).isEqualTo("dummy_message");
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).update(Mockito.any());
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    InstanceRequestV2Entity getInstanceRequestV2Entity(
            SpotInstanceRequestState state, SpotRequestStatusCode statusCode) {

        return InstanceRequestV2Entity.builder()
                .request(GsonCompatMapper.toJson(getDummyClientRequestModel().getLaunchSpecification()))
                .customer(DUMMY_CUSTOMER_1).requestId(DUMMY_REQUEST_ID)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES).state(state)
                .statusCode(statusCode.toString()).statusMessage("dummy_message")
                .statusUpdateTime(Instant.now()).build();
    }


    private ClientRequestDataModel getDummyClientRequestModel() {
        ClientRequestDataModel.LaunchSpecification launchSpecification = ClientRequestDataModel.LaunchSpecification.builder()
                .gpu(DUMMY_GPU).backend(ResourceProvider.BYOC.toString())
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).containerImage(DUMMY_CONTAINER_IMAGE)
                .ncaId(DUMMY_NON_BYOC_NCA_ID).versionId(DUMMY_FUNCTION_VERSION_ID)
                .functionId(DUMMY_FUNCTION_ID)
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();

        ClientRequestDataModel clientRequestDataModel = ClientRequestDataModel.builder()
                .instanceCount(2).sub(DUMMY_CUSTOMER_1)
                .spotInstanceRequestAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .requestId(DUMMY_REQUEST_ID).launchSpecification(launchSpecification).build();
        return clientRequestDataModel;
    }

    @Test
    void updateInstanceStatus_forBYOC_clusterIsNotRegisteredAndZoneNotProvided_throwsException() {

        // Prepare
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest = getInstanceUpdateRequestForActiveInstance(
                RUNNING);
        instanceStatusUpdateRequest.setPlacement(null);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenThrow(
                new IcmsBadRequestException("availabilityZone is not provided"));

        // Act
        IcmsBadRequestException exception = Assertions.assertThrows(IcmsBadRequestException.class,
                                                                   () -> {
                                                                       instanceUpdateService.updateInstanceStatus(
                                                                               DUMMY_REQUEST_ID,
                                                                               DUMMY_INSTANCE_ID,
                                                                               instanceStatusUpdateRequest,
                                                                               DUMMY_CLUSTER_ID,
                                                                               auditProps);
                                                                   });

        // Verify
        assertEquals("availabilityZone is not provided", exception.getBody().getDetail());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
    }

    // RESERVATION validation tests
    @ParameterizedTest
    @MethodSource("provideActiveStatesAndCapacities")
    void updateInstanceStatus_withReservationIdForNewInstance_returnsSuccess(SpotInstanceInternalState state, CapacityType capacityType) {
        // Prepare
        UUID reservationId = UUID.randomUUID();
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstanceWithReservationId(state, reservationId, capacityType);
        InstanceRequestV2Entity instanceRequestV2Entity = getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN, SpotRequestStatusCode.PENDING_FULFILLMENT);

        // Mock for reservation validation
        Instant instanceExpirationTime = Instant.now().plusSeconds(300L);
        ReservationEntity reservationEntity = getDummyReservation(reservationId);
        when(reservationCapacityValidationHelper.validateAndGetReservationEntity(reservationId)).thenReturn(Optional.of(reservationEntity));
        // instanceExpirationTime will be set only for RESERVED_BACKUP instances
        if (capacityType.equals(CapacityType.RESERVED_BACKUP)) {
            doNothing().when(reservationCapacityValidationHelper).validateReservationBackupCapacityForInstanceStateUpdate(eq(reservationEntity), any(InstanceRequestV2Entity.class));
            doReturn(instanceExpirationTime).when(instanceLifecycleHelper).getReservationTtl(reservationEntity.getEndTime());
        }

        // mock request entity to find customer
        doReturn(Optional.of(instanceRequestV2Entity)).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        doNothing().when(instanceV2Repository).insert(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        // Determine expected event name and instance state based on the parameter
        String expectedEventName = state == STARTING ? Events.STARTING_INSTANCE.toString()
                                                     : Events.STARTED_RUNNING_INSTANCE.toString();
        String expectedInstanceState = state.getStateName();

        GenericMetric genericMetric = new GenericMetric().withEventName(expectedEventName)
                .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                .withInstanceId(DUMMY_INSTANCE_ID)
                .withInstanceState(expectedInstanceState)
                .withRequestId(DUMMY_REQUEST_ID)
                .withMetadata(Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE))
                .withRequestState(SpotInstanceRequestState.ACTIVE.toString())
                .withResourceProvider(ResourceProvider.BYOC).withInstanceWaitingTime(0)
                .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).withZoneName(DUMMY_ZONE)
                .withDeploymentId(null)
                .withGpuSpecificationId(null)
                .withCapacityType(capacityType.toString())
                .withFunctionName(null)
                .withTaskName(null)
                .withGpuName(DUMMY_GPU)
                .withReservationId(reservationId.toString());

        // instanceExpirationTime will be set only for RESERVED_BACKUP instances
        if (capacityType.equals(CapacityType.RESERVED_BACKUP)) {
            genericMetric.withInstanceExpirationTime(instanceExpirationTime);
        }
        
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act
        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric));
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).insert(Mockito.argThat(
                entity -> {
                    Assertions.assertNotNull(entity.getReservationId());
                    Assertions.assertNotNull(entity.getCapacityType());
                    return entity.getCapacityType().equals(capacityType.toString()) &&
                            entity.getReservationId().equals(reservationId);
                }));
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).parseRequestInfo(Mockito.any());
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @ParameterizedTest
    @MethodSource("provideTerminalStatesAndCapacities")
    void updateInstanceStatus_withReservationIdForInstanceTermination_returnsSuccess(SpotInstanceInternalState state, CapacityType capacityType) {
        // Prepare
        UUID reservationId = UUID.randomUUID();
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstanceWithReservationId(state, reservationId, capacityType);
        InstanceRequestV2Entity instanceRequestV2Entity = getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN, SpotRequestStatusCode.PENDING_FULFILLMENT);

        // Mock for reservation validation
        Instant instanceExpirationTime = Instant.now().plusSeconds(300L);
        ReservationEntity reservationEntity = getDummyReservation(reservationId);
        when(reservationCapacityValidationHelper.validateAndGetReservationEntity(reservationId)).thenReturn(Optional.of(reservationEntity));

        // instanceExpirationTime will be set only for RESERVED_BACKUP instances
        if (capacityType.equals(CapacityType.RESERVED_BACKUP)) {
            doReturn(instanceExpirationTime).when(instanceLifecycleHelper).getReservationTtl(reservationEntity.getEndTime());
            doNothing().when(reservationCapacityValidationHelper).validateReservationBackupCapacityForInstanceStateUpdate(eq(reservationEntity), any(InstanceRequestV2Entity.class));
        }

        // mock request entity to find customer
        doReturn(Optional.of(instanceRequestV2Entity)).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        
        GenericMetric genericMetric = 
                new GenericMetric().withEventName(Events.INSTANCE_TERMINATED_BY_ZONE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID).withReasonForTermination(
                                SpotInstanceStatus.INSTANCE_TERMINATED_BY_SERVICE.toString())
                        .withInstanceState(TERMINATED.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withRequestState(SpotInstanceRequestState.CLOSED.toString())
                        .withInstanceLifeTime(0).withZoneName(DUMMY_ZONE)
                        .withResourceProvider(ResourceProvider.BYOC)
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).withMetadata(
                                Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE,
                                       EventMetaData.TERMINATION_CAUSE.getName(),
                                       DUMMY_TERMINATION_CAUSE, EventMetaData.ERROR_LOG.getName(),
                                       DUMMY_FAILED_CONTAINER_LOG))
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null)
                        .withFunctionName(null)
                        .withTaskName(null)
                        .withGpuName(DUMMY_GPU)
                        .withCapacityType(capacityType.toString())
                        .withReservationId(reservationId.toString());

        // instanceExpirationTime will be set only for RESERVED_BACKUP instances
        if (capacityType.equals(CapacityType.RESERVED_BACKUP)) {
            genericMetric.withInstanceExpirationTime(instanceExpirationTime);
        }
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric));
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        InstanceV2Entity runningInstance = getInstanceEntityForRunningInstance();
        runningInstance.setReservationId(reservationId); // Set reservationId on existing instance
        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                DUMMY_INSTANCE_ID)).thenReturn(Optional.empty());
        doNothing().when(instanceV2Repository).insert(Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act
        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).insert(Mockito.argThat(
                entity -> {
                    Assertions.assertNotNull(entity.getReservationId());
                    Assertions.assertNotNull(entity.getCapacityType());
                    return entity.getCapacityType().equals(capacityType.toString()) &&
                            entity.getReservationId().equals(reservationId);
                }));
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                                                                   DUMMY_INSTANCE_ID);
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric));
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @ParameterizedTest
    @EnumSource(value = SpotInstanceInternalState.class, names = {"STARTING", "RUNNING", "TERMINATED"})
    void updateInstanceStatus_withReservationIdNotPresent_throwException(SpotInstanceInternalState state) {
        // Prepare
        UUID reservationId = UUID.randomUUID();
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstanceWithReservationId(state, reservationId, CapacityType.RESERVED);

        // Mock for reservation validation
        when(reservationCapacityValidationHelper.validateAndGetReservationEntity(reservationId))
                .thenThrow(new PreConditionFailedException(String.format("Invalid %s reservationId provided", reservationId)));

        // mock request entity to find customer
        InstanceRequestV2Entity instanceRequestV2Entity = getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT);
        doReturn(Optional.of(instanceRequestV2Entity)).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        GenericMetric genericMetric = new GenericMetric()
                .withEventName(Events.PRE_CONDITION_FAILED.toString())
                .withInstanceId(DUMMY_INSTANCE_ID)
                .withRequestId(DUMMY_REQUEST_ID)
                .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                .withFunctionId(DUMMY_FUNCTION_ID)
                .withNcaId(DUMMY_NON_BYOC_NCA_ID)
                .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .withClusterId(instanceStatusUpdateRequest.getPlacement().getAvailabilityZone())
                .withError(String.format("Invalid %s reservationId provided", reservationId))
                .withDeploymentId(instanceRequestV2Entity.getDeploymentId())
                .withGpuSpecificationId(instanceRequestV2Entity.getGpuSpecificationId())
                .withFunctionName(null)
                .withTaskName(null)
                .withGpuName(DUMMY_GPU)
                .withCustomer(instanceRequestV2Entity.getCustomer())
                .withInstanceCount(instanceRequestV2Entity.getInstanceCount())
                .withResourceProvider(instanceRequestV2Entity.getResourceProvider())
                .withRequestState(instanceRequestV2Entity.getState().toString())
                .withCapacityType(instanceStatusUpdateRequest.getCapacityType())
                .withCloudProvider(CloudProvider.AWS)
                .withMetadata(Map.of(EventMetaData.REQUEST_BODY.getName(),
                        GsonCompatMapper.toJson(ignoreSensitiveInformation(instanceStatusUpdateRequest)),
                        EventMetaData.REQUEST_STATUS.getName(),
                        instanceRequestV2Entity.getStatusCode()));

        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());
        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                DUMMY_INSTANCE_ID)).thenReturn(Optional.empty());

        // Act
       PreConditionFailedException preConditionFailedException = Assertions.assertThrows(PreConditionFailedException.class, () ->{
            instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                    instanceStatusUpdateRequest,
                    DUMMY_CLUSTER_ID, auditProps);
        });

        // Assert
        assertEquals(String.format("Invalid %s reservationId provided", reservationId), preConditionFailedException.getBody().getDetail());
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric));
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                DUMMY_INSTANCE_ID);
        verifyNoMoreInteractions(instanceV2Repository);
        verifyNoInteractions(auditService);
    }

    @ParameterizedTest
    @EnumSource(value = SpotInstanceInternalState.class, names = {"STARTING", "RUNNING", "TERMINATED"})
    void updateInstanceStatus_withReservationFullyUtilized_throwException(SpotInstanceInternalState state) {
        // Prepare
        UUID reservationId = UUID.randomUUID();
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstanceWithReservationId(state, reservationId, CapacityType.RESERVED_BACKUP);

        // mock request entity to find customer
        InstanceRequestV2Entity instanceRequestV2Entity = getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                SpotRequestStatusCode.PENDING_FULFILLMENT);
        doReturn(Optional.of(instanceRequestV2Entity)).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        // Mock for reservation validation
        ReservationEntity reservationEntity = getDummyReservation(reservationId);
        when(reservationCapacityValidationHelper.validateAndGetReservationEntity(reservationId))
                .thenReturn(Optional.of(reservationEntity));
        doThrow(new PreConditionFailedException(String.format("Reservation %s is fully utilized", reservationEntity.getReservationId()))).when(reservationCapacityValidationHelper)
                .validateReservationBackupCapacityForInstanceStateUpdate(reservationEntity, instanceRequestV2Entity);

        GenericMetric genericMetric = new GenericMetric()
                .withEventName(Events.PRE_CONDITION_FAILED.toString())
                .withInstanceId(DUMMY_INSTANCE_ID)
                .withRequestId(DUMMY_REQUEST_ID)
                .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                .withFunctionId(DUMMY_FUNCTION_ID)
                .withNcaId(DUMMY_NON_BYOC_NCA_ID)
                .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .withClusterId(instanceStatusUpdateRequest.getPlacement().getAvailabilityZone())
                .withError(String.format("Reservation %s is fully utilized", reservationId))
                .withDeploymentId(instanceRequestV2Entity.getDeploymentId())
                .withGpuSpecificationId(instanceRequestV2Entity.getGpuSpecificationId())
                .withFunctionName(null)
                .withTaskName(null)
                .withGpuName(DUMMY_GPU)
                .withCustomer(instanceRequestV2Entity.getCustomer())
                .withInstanceCount(instanceRequestV2Entity.getInstanceCount())
                .withResourceProvider(instanceRequestV2Entity.getResourceProvider())
                .withRequestState(instanceRequestV2Entity.getState().toString())
                .withCapacityType(instanceStatusUpdateRequest.getCapacityType())
                .withCloudProvider(CloudProvider.AWS)
                .withMetadata(Map.of(EventMetaData.REQUEST_BODY.getName(),
                        GsonCompatMapper.toJson(ignoreSensitiveInformation(instanceStatusUpdateRequest)),
                        EventMetaData.REQUEST_STATUS.getName(),
                        instanceRequestV2Entity.getStatusCode()));

        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric));
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());
        when(instanceV2Repository.findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                DUMMY_INSTANCE_ID)).thenReturn(Optional.empty());

        // Act
        PreConditionFailedException preConditionFailedException = Assertions.assertThrows(PreConditionFailedException.class,
                () -> instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                instanceStatusUpdateRequest,
                DUMMY_CLUSTER_ID, auditProps));

        // Assert
        assertEquals(String.format("Reservation %s is fully utilized", reservationId), preConditionFailedException.getBody().getDetail());
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric));
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1,
                DUMMY_INSTANCE_ID);
        verifyNoMoreInteractions(instanceV2Repository);
        verifyNoInteractions(auditService);
    }

    @Test
    void updateInstanceStatus_withNullReservationId_returnsSuccess() {
        // Prepare - Test that null reservationId is handled properly
        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstance(RUNNING);
        instanceStatusUpdateRequest.setReservationId(null); // Explicitly set to null

        // mock request entity to find customer
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);

        doNothing().when(instanceV2Repository).insert(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.STARTED_RUNNING_INSTANCE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(DUMMY_CUSTOMER_1)
                        .withInstanceId(DUMMY_INSTANCE_ID)
                        .withInstanceState(SpotInstanceInternalState.RUNNING.getStateName())
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withMetadata(Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE))
                        .withRequestState(SpotInstanceRequestState.ACTIVE.toString())
                        .withResourceProvider(ResourceProvider.BYOC).withInstanceWaitingTime(0)
                        .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE).withZoneName(DUMMY_ZONE)
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null)
                        .withCapacityType(CapacityType.SPOT.toString())
                        .withFunctionName(null)
                        .withTaskName(null)
                        .withGpuName(DUMMY_GPU)
                        .withReservationId((String) null)); // null reservationId in telemetry
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                DUMMY_REQUEST_ID)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act
        instanceUpdateService.updateInstanceStatus(DUMMY_REQUEST_ID, DUMMY_INSTANCE_ID,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceV2Repository).insert(Mockito.argThat(
                entity -> entity.getReservationId() == null));
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        verify(instanceServiceHelper).parseRequestInfo(Mockito.any());
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void updateInstanceStatus_withReservationIdForExistingInstanceUpdate_returnsSuccess() {
        // Prepare
        UUID reservationId = UUID.randomUUID();
        String instanceId = DUMMY_STARTING_INSTANCE_ID;
        String requestId = DUMMY_REQUEST_ID;
        String customer = DUMMY_CUSTOMER_ID;

        InstanceRequestV2Entity instanceRequestEntity = getInstanceRequestV2Entity(
                SpotInstanceRequestState.OPEN, SpotRequestStatusCode.PENDING_FULFILLMENT);
        instanceRequestEntity.setRequestId(requestId);
        instanceRequestEntity.setCustomer(customer);

        InstanceV2Entity dummyInstanceEntity = getDummyInstanceEntity(
                SpotInstanceInternalState.STARTING, SpotInstanceRequestState.ACTIVE, Instant.now(),
                ResourceProvider.BYOC);
        dummyInstanceEntity.setInstanceId(instanceId);
        dummyInstanceEntity.setRequestId(requestId);
        dummyInstanceEntity.setCustomer(customer);
        dummyInstanceEntity.setReservationId(reservationId); // Set existing reservationId

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstanceWithReservationId(RUNNING, reservationId, CapacityType.RESERVED_BACKUP);
        instanceStatusUpdateRequest.setInstanceIps(Set.of("ip1", ""));

        doReturn(Optional.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestById(requestId);
        doReturn(Optional.of(dummyInstanceEntity)).when(instanceV2Repository)
                .findInstanceByCustomerAndId(customer, instanceId);

        doNothing().when(instanceV2Repository).update(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());

        List<GenericMetric> genericMetricsList = List.of(
                new GenericMetric().withEventName(Events.STARTED_RUNNING_INSTANCE.toString())
                        .withCloudProvider(CloudProvider.AWS).withCustomer(customer)
                        .withInstanceId(instanceId).withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                        .withFunctionId(DUMMY_FUNCTION_ID).withNcaId(DUMMY_NON_BYOC_NCA_ID)
                        .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .withInstanceState(SpotInstanceInternalState.RUNNING.getStateName())
                        .withRequestId(requestId).withMetadata(
                                Map.of(INSTANCE_TYPE_KEY, DUMMY_NON_BYOC_INSTANCE_TYPE,
                                       EventMetaData.RESOURCE_IPS.getName(), "ip1"))
                        .withRequestState(SpotInstanceRequestState.ACTIVE.toString())
                        .withResourceProvider(ResourceProvider.BYOC).withInstanceWaitingTime(0)
                        .withZoneName(DUMMY_ZONE)
                        .withDeploymentId(null)
                        .withGpuSpecificationId(null)
                        .withReservationId(reservationId.toString()));
        doNothing().when(telemetryEventClient).triggerEvent(genericMetricsList);
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                requestId)).thenReturn(getDummyInstancePlacementValidationResponse(
                instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act
        instanceUpdateService.updateInstanceStatus(requestId, instanceId,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert
        verify(telemetryEventClient).triggerEvent(genericMetricsList);
        verify(instanceRequestV2Repository).findRequestById(requestId);
        verify(instanceV2Repository).findInstanceByCustomerAndId(customer, instanceId);
        verify(instanceV2Repository).update(Mockito.argThat(
                entity -> entity.getReservationId() != null && 
                         entity.getReservationId().equals(reservationId)));
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, requestId);
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    private boolean validateUpdatedInstanceEntity(
            InstanceV2Entity instanceEntity, String instanceId, String requestId,
            SpotInstanceInternalState instanceInternalState,
            SpotInstanceRequestState instanceRequestState, String errorLog) {
        return instanceEntity.getInstanceId().equals(instanceId)
                && instanceEntity.getRequestId().equals(requestId)
                && instanceEntity.getInstanceStateName().equals(instanceInternalState)
                && instanceEntity.getRequestState().equals(instanceRequestState)
                && validateErrorLog(instanceEntity, errorLog);
    }

    private boolean validateErrorLog(InstanceV2Entity instanceEntity, String errorLog) {
        if (StringUtils.isNotBlank(instanceEntity.getErrorLog())) {
            return instanceEntity.getErrorLog().equals(errorLog);
        }
        return true;
    }

    private static Stream<Arguments> provideActiveStatesAndCapacities() {
        return Stream.of(
                Arguments.of(SpotInstanceInternalState.STARTING, CapacityType.RESERVED),
                Arguments.of(SpotInstanceInternalState.STARTING, CapacityType.RESERVED_BACKUP),
                Arguments.of(SpotInstanceInternalState.RUNNING, CapacityType.RESERVED),
                Arguments.of(SpotInstanceInternalState.RUNNING, CapacityType.RESERVED_BACKUP)
        );
    }
    private static Stream<Arguments> provideTerminalStatesAndCapacities() {
        return Stream.of(
                Arguments.of(TERMINATED, CapacityType.RESERVED),
                Arguments.of(TERMINATED, CapacityType.RESERVED_BACKUP)
        );
    }

    @Test
    void handelInstanceUpdateForActiveRequest_withNewInstanceRegistration_returnsSuccess() {
        // Arrange
        String instanceId = DUMMY_STARTING_INSTANCE_ID;
        String requestId = DUMMY_REQUEST_ID;

        // mock request entity in ACTIVE state
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.ACTIVE,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(requestId);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstance(STARTING);

        doNothing().when(instanceV2Repository).insert(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                requestId)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                        ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act
        instanceUpdateService.updateInstanceStatus(requestId, instanceId,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert
        verify(instanceRequestV2Repository).findRequestById(requestId);
        verify(instanceV2Repository).insert(instanceEntityArgumentCaptor.capture());
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(),
                                                           Mockito.any());
    }

    @Test
    void handelInstanceUpdateForActiveRequest_withRunningInstanceUpdate_returnsSuccess() {
        // Arrange
        String instanceId = DUMMY_STARTING_INSTANCE_ID;
        String requestId = DUMMY_REQUEST_ID;

        // mock request entity in ACTIVE state
        doReturn(Optional.of(getInstanceRequestV2Entity(SpotInstanceRequestState.ACTIVE,
                                                    SpotRequestStatusCode.PENDING_FULFILLMENT))).when(
                instanceRequestV2Repository).findRequestById(requestId);

        InstanceV2Entity existingInstance = getInstanceEntityForRunningInstance();
        existingInstance.setInstanceId(instanceId);
        existingInstance.setRequestId(requestId);
        existingInstance.setInstanceStateName(STARTING);
        existingInstance.setInstanceStateCode(SpotInstanceInternalState.getStateCode(STARTING));
        existingInstance.setRequestState(SpotInstanceRequestState.ACTIVE);

        doReturn(Optional.of(existingInstance)).when(instanceV2Repository)
                .findInstanceByCustomerAndId(DUMMY_CUSTOMER_1, instanceId);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstance(RUNNING);

        doNothing().when(instanceV2Repository).update(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                requestId)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                        ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act
        instanceUpdateService.updateInstanceStatus(requestId, instanceId,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert
        verify(instanceRequestV2Repository).findRequestById(requestId);
        verify(instanceV2Repository).findInstanceByCustomerAndId(DUMMY_CUSTOMER_1, instanceId);
        verify(instanceV2Repository).update(instanceEntityArgumentCaptor.capture());
        
        // Verify the core state transition: STARTING -> RUNNING
        InstanceV2Entity updatedInstance = instanceEntityArgumentCaptor.getValue();
        assertThat(updatedInstance.getInstanceStateName()).isEqualTo(RUNNING);
        assertThat(updatedInstance.getInstanceStateCode()).isEqualTo(
                SpotInstanceInternalState.getStateCode(RUNNING));
        assertThat(updatedInstance.getInstanceId()).isEqualTo(instanceId);
        assertThat(updatedInstance.getRequestId()).isEqualTo(requestId);
    }

    @Test
    void handelInstanceUpdateForActiveRequest_withInvalidStatusCode_throwsException() {
        // Arrange
        String instanceId = DUMMY_STARTING_INSTANCE_ID;
        String requestId = DUMMY_REQUEST_ID;

        // mock request entity in ACTIVE state with invalid status code (e.g., PENDING_EVALUATION)
        InstanceRequestV2Entity requestEntity = getInstanceRequestV2Entity(SpotInstanceRequestState.ACTIVE,
                                                                   SpotRequestStatusCode.PENDING_EVALUATION);
        doReturn(Optional.of(requestEntity)).when(instanceRequestV2Repository).findRequestById(requestId);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstance(STARTING);

        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                requestId)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                        ResourceProvider.BYOC));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act & Assert
        assertThrows(PreConditionFailedException.class, () -> {
            instanceUpdateService.updateInstanceStatus(requestId, instanceId,
                                                           instanceStatusUpdateRequest,
                                                           DUMMY_CLUSTER_ID, auditProps);
        });
    }

    @Test
    void handelInstanceUpdateForOpenRequest_transitionsRequestToActiveState_onFirstInstance() {
        // Arrange
        String instanceId = DUMMY_STARTING_INSTANCE_ID;
        String requestId = DUMMY_REQUEST_ID;

        // Enable feature flag for request state transition
        when(icmsConfigurationProperties.isRequestStateTransitionToActiveEnabled()).thenReturn(true);

        InstanceRequestV2Entity requestEntity = getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                                   SpotRequestStatusCode.PENDING_FULFILLMENT);
        doReturn(Optional.of(requestEntity)).when(instanceRequestV2Repository).findRequestById(requestId);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstance(STARTING);

        doNothing().when(instanceV2Repository).insert(Mockito.any());
        doNothing().when(instanceRequestV2Repository).update(Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(auditService)
                .sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(), Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                requestId)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                        ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act
        instanceUpdateService.updateInstanceStatus(requestId, instanceId,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert - Verify request state was updated
        verify(instanceRequestV2Repository).update(Mockito.argThat(entity ->
                entity.getState() == SpotInstanceRequestState.ACTIVE
        ));
        verify(instanceV2Repository).insert(Mockito.any());
        // Verify audit event for request state transition was sent
        verify(auditService).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(), Mockito.any());
    }

    @Test
    void handelInstanceUpdateForOpenRequest_stateTransitionFails_butInstanceCreationSucceeds() {
        // Arrange
        String instanceId = DUMMY_STARTING_INSTANCE_ID;
        String requestId = DUMMY_REQUEST_ID;

        // Enable feature flag for request state transition
        when(icmsConfigurationProperties.isRequestStateTransitionToActiveEnabled()).thenReturn(true);

        InstanceRequestV2Entity requestEntity = getInstanceRequestV2Entity(SpotInstanceRequestState.OPEN,
                                                                   SpotRequestStatusCode.PENDING_FULFILLMENT);
        doReturn(Optional.of(requestEntity)).when(instanceRequestV2Repository).findRequestById(requestId);

        SpotInstanceStatusUpdateRequest instanceStatusUpdateRequest =
                getInstanceUpdateRequestForActiveInstance(STARTING);

        // Instance insertion succeeds
        doNothing().when(instanceV2Repository).insert(Mockito.any());
        
        // Request state update fails
        doThrow(new RuntimeException("Database update failed"))
                .when(instanceRequestV2Repository).update(Mockito.any());
        
        doNothing().when(auditService)
                .sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        when(internalInstanceServiceHelper.validateInstancePlacement(
                instanceStatusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                requestId)).thenReturn(
                getDummyInstancePlacementValidationResponse(
                        instanceStatusUpdateRequest.getPlacement(), CloudProvider.AWS,
                        ResourceProvider.BYOC));
        when(instanceServiceHelper.parseRequestInfo(Mockito.any())).thenReturn(
                getDummyClientRequestModel());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestModel().getLaunchSpecification());

        // Act - Should NOT throw exception despite state transition failure
        instanceUpdateService.updateInstanceStatus(requestId, instanceId,
                                                       instanceStatusUpdateRequest,
                                                       DUMMY_CLUSTER_ID, auditProps);

        // Assert
        // Verify instance was successfully inserted
        verify(instanceV2Repository).insert(instanceEntityArgumentCaptor.capture());
        InstanceV2Entity insertedInstance = instanceEntityArgumentCaptor.getValue();
        assertThat(insertedInstance.getInstanceId()).isEqualTo(instanceId);
        assertThat(insertedInstance.getInstanceStateName()).isEqualTo(STARTING);
        
        // Verify request state update was attempted
        verify(instanceRequestV2Repository).update(Mockito.any());
        
        // Verify instance creation audit event was sent (state transition audit NOT sent due to failure)
        verify(auditService).sendAuditEventForInstance(Mockito.any(), Mockito.any(), Mockito.any());
        verify(auditService, times(0)).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(), Mockito.any());
        
        // Verify telemetry was sent for instance creation
        verify(telemetryEventClient, times(2)).triggerEvent(Mockito.any());
    }
}
