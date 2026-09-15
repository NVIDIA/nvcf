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

import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.CANNOT_FULFILL;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_EVALUATION;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_FULFILLMENT;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.SCHEDULE_EXPIRED;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.MESSAGE_BATCH_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REQUEST_BODY;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REQUEST_STATUS;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CANCELED_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CLUSTER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_VERSION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_MESSAGE_BATCH_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NCA_ID_ACCOUNT_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static com.nvidia.icms.util.TestUtil.getDummyClientRequestDataModel;
import static com.nvidia.icms.util.TestUtil.getDummyInstancePlacement;
import static com.nvidia.icms.util.TestUtil.getDummyInstancePlacementValidationResponse;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatusUpdateRequest;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.reservation.entity.ReservationEntity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.SqsMessageRepository;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageKey;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.ByocService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class InternalInstanceServiceTest {

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    private InternalInstanceService internalInstanceService;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private AppAuditService auditService;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private InstanceUpdateService instanceUpdateService;

    @Mock
    private ByocService byocService;

    @Mock
    private SqsMessageRepository sqsMessageRepository;

    @Mock
    private InternalInstanceServiceHelper internalInstanceServiceHelper;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    @Mock
    private ReservationCapacityValidationHelper reservationCapacityValidationHelper;

    private Map<String, Object> auditProps;

    @BeforeEach
    void setup() {
        auditProps = new HashMap<>();
        internalInstanceService =
                new InternalInstanceService(instanceRequestV2Repository,
                                        telemetryEventClient, auditService,
                                        icmsConfigurationProperties,
                                        instanceUpdateService, byocService, sqsMessageRepository,
                                        internalInstanceServiceHelper, instanceServiceHelper,
                                        reservationCapacityValidationHelper,
                                        ComputePlatformTestFixtures.nonByocComputePlatformService());
    }

    @Test
    void updateInstanceRequestStatus_withPendingFulfillmentState_returnsSuccess() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);
        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, null, null,
                        getDummyInstancePlacement(), null, null);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doNothing().when(instanceRequestV2Repository).update(instanceRequestEntity1);
        doNothing().when(auditService)
                .sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(), Mockito.any());
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();

        HashMap<String, Object> metadata = new HashMap<>();
        metadata.put(REQUEST_BODY.getName(), GsonCompatMapper.toJson(statusUpdateRequest));
        metadata.put(MESSAGE_BATCH_STATUS.getName(), null);
        metadata.put(REQUEST_STATUS.getName(), PENDING_FULFILLMENT.toString());

        GenericMetric genericMetric =
                getDummyMetricForStartedProcessingRequest(metadata, CloudProvider.OCI,
                        ResourceProvider.OCI, DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                                                          instanceRequestEntity1.getDeploymentId(),
                                                          instanceRequestEntity1.getGpuSpecificationId());
        genericMetric.setMessageBatchId(null);
        genericMetric.setSqsMessageAcknowledgeInstanceCount(null);

        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceRequestV2Repository).update(instanceRequestEntity1);
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);

        verify(telemetryEventClient).triggerEvent(List.of(genericMetric));
        verify(auditService).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(),
                                                          Mockito.any());
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                statusUpdateRequest.getStatus().toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo(
                "Instance request status set to pending-fulfillment");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
        
        // Verify SPOT capacity (null reservationId, null capacityType) skips reservation validation
        verify(reservationCapacityValidationHelper, Mockito.never())
                .validateAndGetReservationEntity(Mockito.any());
        verify(reservationCapacityValidationHelper, Mockito.never())
                .validateReservationBackupCapacityForRequestStateUpdate(Mockito.any(), Mockito.any(), Mockito.any());
    }

    // Legacy flow for pending-fulfillment
    @Test
    void updateInstanceRequestStatus_withPendingFulfillmentStateAndSameStatePresentInDb_returnsSuccess() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT, ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, null, null, getDummyInstancePlacement(), null, null);
        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        
        // Verify SPOT capacity with null reservationId skips validation
        verify(reservationCapacityValidationHelper, Mockito.never())
                .validateAndGetReservationEntity(Mockito.any());

        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                PENDING_FULFILLMENT.toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo("dummy_message");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
    }


    @Test
    void updateInstanceRequestStatus_withInvalidRequestId_returnsNotFoundException() {

        doReturn(Optional.empty()).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, null, null,
                        getDummyInstancePlacement(), null, null);
        IcmsNotFoundException exception =
                assertThrows(IcmsNotFoundException.class, () ->
                        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID,
                                                                    statusUpdateRequest,
                                                                    auditProps));

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Cannot find request with id " + DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceRequestStatus_withInvalidFieldStatusInRequest_returnsBadRequestException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(SpotRequestStatusCode.BAD_PARAMETERS,
                                                           null, null, getDummyInstancePlacement(), null, null);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        IcmsBadRequestException exception =
                assertThrows(IcmsBadRequestException.class, () ->
                        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID,
                                                                    statusUpdateRequest,
                                                                    auditProps));

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        assertThat(exception.getBody().getDetail()).isEqualTo(
                "'status' field in request body must be '[schedule-expired, pending-fulfillment, cannot-fulfill]'");
    }

    @Test
    void updateInstanceRequestStatus_withRequestNotOpen_returnsPreConditionFailedException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.CANCELED, SpotRequestStatusCode.SCHEDULE_EXPIRED, ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 1, getDummyInstancePlacement(), null, null);
        PreConditionFailedException exception =
                assertThrows(PreConditionFailedException.class, () ->
                        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID,
                                                                    statusUpdateRequest,
                                                                    auditProps));

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Request associated with given id is not in 'open' or 'active' state, existing request state is 'canceled'");
    }

    // Legacy flow for pending-fulfillment
    @Test
    void updateInstanceRequestStatus_withPendingFulfillmentStateAndRequestNotReceivedInWaitingTime_returnsPreConditionFailedException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(0).when(icmsConfigurationProperties).getRequestCancelDurationInMin();

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, null, null, getDummyInstancePlacement(), null, null);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        PreConditionFailedException exception =
                assertThrows(PreConditionFailedException.class, () ->
                        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID,
                                                                    statusUpdateRequest,
                                                                    auditProps));

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Request is not fulfilled within 0 min");
    }

    // Legacy flow for pending-fulfillment
    @Test
    void updateInstanceRequestStatus_withPendingFulfillmentStateAndDBUpdateFailed_returnsInternalServerException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doThrow(new IcmsInternalServerException(
                        String.format("Failed to update state of requestId %s", DUMMY_REQUEST_ID)))
                .when(instanceRequestV2Repository).update(instanceRequestEntity1);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, null, null, getDummyInstancePlacement(), null, null);
        IcmsInternalServerException exception =
                assertThrows(IcmsInternalServerException.class, () ->
                        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID,
                                                                    statusUpdateRequest,
                                                                    auditProps));

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceRequestV2Repository).update(instanceRequestEntity1);
        assertThat(exception.getBody().getDetail()).isEqualTo(
                String.format("Failed to update state of requestId %s", DUMMY_REQUEST_ID));
    }

    @Test
    void updateInstanceRequestStatus_withScheduleExpiredState_returnsSuccess() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT, ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        SpotRequestStatusCode.SCHEDULE_EXPIRED, null, null, null, null, CapacityType.SPOT);

        GenericMetric genericMetric = new GenericMetric()
                .withEventName(Events.SCHEDULE_EXPIRED_STATE_UPDATE.toString())
                .withCloudProvider(CloudProvider.OCI)
                .withCustomer(DUMMY_CUSTOMER_1)
                .withRequestState(instanceRequestEntity1.getState().toString())
                .withNcaId(DUMMY_NON_BYOC_NCA_ID)
                .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                .withFunctionId(DUMMY_FUNCTION_ID)
                .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .withRequestId(DUMMY_REQUEST_ID)
                .withDeploymentId(instanceRequestEntity1.getDeploymentId())
                .withCapacityType(CapacityType.SPOT)
                .withGpuSpecificationId(instanceRequestEntity1.getGpuSpecificationId());
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest,
                                                    auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric));
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);

        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                PENDING_FULFILLMENT.toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo("dummy_message");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
    }

    @Test
    void updateInstanceRequestStatus_withPendingFulfillmentStateAndMessageBatchIdForNonByoc_returnsSuccess() {
        var placement = getDummyInstancePlacement();
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 1, placement, null, null);

        HashMap<String, Object> metadata = new HashMap<>();
        metadata.put(REQUEST_BODY.getName(), GsonCompatMapper.toJson(statusUpdateRequest));
        metadata.put(MESSAGE_BATCH_STATUS.getName(), true);
        metadata.put(REQUEST_STATUS.getName(), PENDING_FULFILLMENT.toString());

        GenericMetric genericMetric1 =
                getDummyMetricForStartedProcessingRequest(metadata, CloudProvider.OCI,
                        ResourceProvider.OCI, DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                                                          instanceRequestEntity1.getDeploymentId(),
                                                          instanceRequestEntity1.getGpuSpecificationId());
        GenericMetric genericMetric2 =
                getDummyMetricForSqsBatchStatusUpdate(metadata, CloudProvider.OCI,
                        ResourceProvider.OCI, DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                                                      instanceRequestEntity1.getDeploymentId(),
                                                      instanceRequestEntity1.getGpuSpecificationId());

        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(statusUpdateRequest.getMessageBatchId())
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(statusUpdateRequest.getInstanceCount())
                .status(PENDING_FULFILLMENT)
                .zone(DUMMY_ZONE)
                .build();

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doNothing().when(instanceRequestV2Repository).update(instanceRequestEntity1);
        doNothing().when(auditService)
                .sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(), Mockito.any());
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric1));
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric2));
        doNothing().when(sqsMessageRepository).update(Mockito.argThat(
                entity -> validateSqsMessageEntityMock(entity, sqsMessageEntity)));
        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);

        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceRequestV2Repository).update(instanceRequestEntity1);
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);

        verify(telemetryEventClient).triggerEvent(List.of(genericMetric1));
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric2));
        verify(auditService).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(),
                                                          Mockito.any());
        verify(sqsMessageRepository).update(Mockito.argThat(
                entity -> validateSqsMessageEntityMock(entity, sqsMessageEntity)));
        verify(internalInstanceServiceHelper).validateInstancePlacement(placement, DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                statusUpdateRequest.getStatus().toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo(
                "Instance request status set to pending-fulfillment");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
        verify(reservationCapacityValidationHelper).validateAndGetReservationEntity(null);
        verify(reservationCapacityValidationHelper, Mockito.never())
                .validateReservationBackupCapacityForRequestStateUpdate(Mockito.any(), Mockito.any(), Mockito.any());
    }

    @Test
    void updateInstanceRequestStatus_withPendingFulfillmentStateAndMessageBatchIdForBYOC_returnsSuccess() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.BYOC);

        var instancePlacement = new SpotInstanceStatusUpdateRequest.InstancePlacement(DUMMY_ZONE);
        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 1, instancePlacement, null, CapacityType.SPOT);

        HashMap<String, Object> metadata = new HashMap<>();
        // if zone is not provided then client id is injected as zone
        var statusUpdateRequest1 = CopyUtil.deepCopy(statusUpdateRequest);
        statusUpdateRequest1.setPlacement(
                new SpotInstanceStatusUpdateRequest.InstancePlacement(DUMMY_ZONE));
        metadata.put(REQUEST_BODY.getName(), GsonCompatMapper.toJson(statusUpdateRequest1));
        metadata.put(MESSAGE_BATCH_STATUS.getName(), true);
        metadata.put(REQUEST_STATUS.getName(), PENDING_FULFILLMENT.toString());

        GenericMetric genericMetric1 =
                getDummyMetricForStartedProcessingRequest(metadata, CloudProvider.AZURE,
                        ResourceProvider.BYOC, DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                                                          instanceRequestEntity1.getDeploymentId(),
                                                          instanceRequestEntity1.getGpuSpecificationId());

        GenericMetric genericMetric2 =
                getDummyMetricForSqsBatchStatusUpdate(metadata, CloudProvider.AZURE,
                        ResourceProvider.BYOC, DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                                                      instanceRequestEntity1.getDeploymentId(),
                                                      instanceRequestEntity1.getGpuSpecificationId());

        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(statusUpdateRequest.getMessageBatchId())
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(statusUpdateRequest.getInstanceCount())
                .status(PENDING_FULFILLMENT)
                .zone(DUMMY_ZONE)
                .build();

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doNothing().when(instanceRequestV2Repository).update(instanceRequestEntity1);
        doNothing().when(auditService)
                .sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(), Mockito.any());
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric1));
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric2));

        doNothing().when(sqsMessageRepository).update(Mockito.argThat(
                entity -> validateSqsMessageEntityMock(entity, sqsMessageEntity)));
        doReturn(getDummyInstancePlacementValidationResponse(statusUpdateRequest1.getPlacement(),
                                                             CloudProvider.AZURE,
                                                             ResourceProvider.BYOC)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(instancePlacement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.AZURE.toString()).getLaunchSpecification());

        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(instanceRequestV2Repository).update(instanceRequestEntity1);
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);

        verify(telemetryEventClient).triggerEvent(List.of(genericMetric1));
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric2));
        verify(auditService).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(),
                                                          Mockito.any());
        verify(sqsMessageRepository).update(Mockito.argThat(
                entity -> validateSqsMessageEntityMock(entity, sqsMessageEntity)));
        verify(internalInstanceServiceHelper).validateInstancePlacement(instancePlacement,
                                                                    DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                statusUpdateRequest.getStatus().toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo(
                "Instance request status set to pending-fulfillment");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
    }

    @Test
    void updateInstanceRequestStatus_withByocAndSsaClientIdNotRegistered_throwsException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.BYOC);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, "", 1, null, null, null);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        String errMsg = "Cloud not find any cluster with cluster_id clusterId";
        doThrow(new IcmsNotFoundException(errMsg)).when(byocService)
                .validateAndGetClusterEntityFromByocClusterId(DUMMY_CLUSTER_ID);

        IcmsNotFoundException icmsNotFoundException = assertThrows(IcmsNotFoundException.class, () -> {
            internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                        statusUpdateRequest, auditProps);
        });

        // Assert
        assertEquals(errMsg, icmsNotFoundException.getBody().getDetail());

        // Verify
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(byocService).validateAndGetClusterEntityFromByocClusterId(DUMMY_CLUSTER_ID);
    }

    @Test
    void updateInstanceRequestStatus_withPendingFulfillmentStateAndZoneNotConfigured_throwsException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                     PENDING_EVALUATION, ResourceProvider.BYOC);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 1, null, null, null);

        when(internalInstanceServiceHelper.validateInstancePlacement(null, DUMMY_CLUSTER_ID,
                                                                 DUMMY_REQUEST_ID)).thenThrow(
                new IcmsBadRequestException("availabilityZone is not provided"));

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        IcmsBadRequestException icmsBadRequestException =
                assertThrows(IcmsBadRequestException.class, () -> {
                    internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                                statusUpdateRequest, auditProps);
                });

        assertEquals("availabilityZone is not provided",
                     icmsBadRequestException.getBody().getDetail());
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verify(internalInstanceServiceHelper).validateInstancePlacement(null, DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID);
    }

    @Test
    void updateInstanceRequestStatus_withMultiplePendingFulfillmentStateDifferentWithMessageId_returnsSuccess() {
        // Prepare
        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT, ResourceProvider.OCI);
        instanceRequestEntity1.setCheckBatchwiseInfo(true);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 1, placement, null, null);

        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(statusUpdateRequest.getMessageBatchId())
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(statusUpdateRequest.getInstanceCount())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .build();

        // Already reported message-batch-id
        SqsMessageEntity sqsMessageEntity1 = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID + "_1")
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(statusUpdateRequest.getInstanceCount())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .build();
        when(sqsMessageRepository.findByRequestId(DUMMY_REQUEST_ID)).thenReturn(List.of(sqsMessageEntity1));

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);

        doNothing().when(sqsMessageRepository).update(Mockito.argThat(
                entity -> validateSqsMessageEntityMock(entity, sqsMessageEntity)));
        doNothing().when(telemetryEventClient).triggerEvent(Mockito.anyList());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        // Act
        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        // Assert
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verifyNoMoreInteractions(instanceRequestV2Repository);
        verify(sqsMessageRepository).update(
                Mockito.argThat(entity -> validateSqsMessageEntityMock(entity, sqsMessageEntity)));
        verify(telemetryEventClient).triggerEvent(Mockito.anyList());
        verify(internalInstanceServiceHelper).validateInstancePlacement(placement, DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID);
        verify(sqsMessageRepository).findByRequestId(DUMMY_REQUEST_ID);

        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                PENDING_FULFILLMENT.toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo("dummy_message");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
    }

    @Test
    void updateInstanceRequestStatus_withMultiplePendingFulfillmentStateForSameBatchId_returnsSuccess() {
        // Prepare
        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT, ResourceProvider.OCI);
        instanceRequestEntity1.setCheckBatchwiseInfo(true);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 1, placement, null, null);

        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(statusUpdateRequest.getMessageBatchId())
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(statusUpdateRequest.getInstanceCount())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .build();

        HashMap<String, Object> metadata = new HashMap<>();
        metadata.put(REQUEST_BODY.getName(), GsonCompatMapper.toJson(statusUpdateRequest));
        metadata.put(MESSAGE_BATCH_STATUS.getName(), true);
        metadata.put(REQUEST_STATUS.getName(), PENDING_FULFILLMENT.toString());
        GenericMetric genericMetric =
                getDummyMetricForSqsBatchStatusUpdate(metadata, CloudProvider.OCI,
                                                      ResourceProvider.OCI, DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                                                      instanceRequestEntity1.getDeploymentId(),
                                                      instanceRequestEntity1.getGpuSpecificationId());
        genericMetric.setEventName(Events.RECEIVED_MULTIPLE_SQS_BATCH_STATUS_UPDATE.toString());

        doReturn(List.of(sqsMessageEntity)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_REQUEST_ID);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);

        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                                               DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());
        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric));

        // Act
        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        // Assert
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verifyNoMoreInteractions(instanceRequestV2Repository);
        verify(internalInstanceServiceHelper).validateInstancePlacement(placement, DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID);
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric));

        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                PENDING_FULFILLMENT.toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo("dummy_message");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
    }

    @Test
    void updateInstanceRequestStatus_withSqsBatchStatusUpdateFailed_throwsException() {
        // Prepare
        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT, ResourceProvider.OCI);
        instanceRequestEntity1.setCheckBatchwiseInfo(true);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 1, placement, null, null);
        SqsMessageEntity sqsMessageEntity = getDummySqsMessageEntity(statusUpdateRequest);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doThrow(new IcmsInternalServerException("dummy_exception")).when(sqsMessageRepository)
                .update(Mockito.argThat(
                        entity -> validateSqsMessageEntityMock(entity, sqsMessageEntity)));
        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> {
                                                                internalInstanceService.updateInstanceRequestStatus(
                                                                        DUMMY_CLUSTER_ID,
                                                                        DUMMY_REQUEST_ID,
                                                                        statusUpdateRequest,
                                                                        auditProps);
                                                            });

        // Assert
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verifyNoMoreInteractions(instanceRequestV2Repository);
        verify(sqsMessageRepository).update(Mockito.argThat(
                entity -> validateSqsMessageEntityMock(entity, sqsMessageEntity)));
        verifyNoInteractions(telemetryEventClient);
        verify(internalInstanceServiceHelper).validateInstancePlacement(placement, DUMMY_CLUSTER_ID,
                                                                    DUMMY_REQUEST_ID);

        assertEquals("dummy_exception", exception.getBody().getDetail());
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                PENDING_FULFILLMENT.toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo("dummy_message");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
    }

    @Test
    void updateInstanceRequestStatus_withSecondPendingFulfillmentStateWithMessageIdAndBatchWiseCheckNotEnabled_returnsSuccess() {
        // Prepare
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT, ResourceProvider.OCI);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 1,
                        getDummyInstancePlacement(), null, null);
        doReturn(getDummyInstancePlacementValidationResponse(statusUpdateRequest.getPlacement(),
                                                             CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(statusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID,
                                           DUMMY_REQUEST_ID);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        // Act
        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        // Assert
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verifyNoMoreInteractions(instanceRequestV2Repository);
        verifyNoInteractions(telemetryEventClient);
        verify(sqsMessageRepository).findByRequestId(DUMMY_REQUEST_ID);
        verifyNoMoreInteractions(sqsMessageRepository);
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                statusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);

        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                PENDING_FULFILLMENT.toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo("dummy_message");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
    }

    // cannot-fulfill
    @Test
    void updateInstanceRequestStatus_withCannotFullFillWithoutMessageBatchId_throwsException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        CANNOT_FULFILL, null, null, getDummyInstancePlacement(), null, null);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        IcmsBadRequestException icmsBadRequestException =
                assertThrows(IcmsBadRequestException.class, () -> {
                    internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                                statusUpdateRequest, auditProps);
                });

        assertEquals("messageBatchId must be provided for cannot-fulfill status update",
                     icmsBadRequestException.getBody().getDetail());

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(PENDING_EVALUATION.toString());
    }

    @Test
    void updateInstanceRequestStatus_withCanNotFulFill_returnsSuccess() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);
        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        CANNOT_FULFILL, DUMMY_MESSAGE_BATCH_ID, 1, placement, null, null);

        doReturn(Optional.of(getDummySqsMessageEntity(statusUpdateRequest))).when(
                        sqsMessageRepository)
                .findByRequestIdAndMessageBatchId(DUMMY_REQUEST_ID, DUMMY_MESSAGE_BATCH_ID);

        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);

        // Acknowledgment count must be preserved on cannot-fulfill so the deployment
        // GET API can surface accurate per-batch TERMINATED placeholders. Only the
        // batch status flips to CANNOT_FULFILL.
        SqsMessageEntity updatedSqsMessageEntity = getDummySqsMessageEntity(statusUpdateRequest);
        updatedSqsMessageEntity.setStatus(CANNOT_FULFILL);
        doNothing().when(sqsMessageRepository).update(updatedSqsMessageEntity);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(PENDING_EVALUATION.toString());
        verify(sqsMessageRepository).update(Mockito.argThat(entity ->
                entity.getStatus() == CANNOT_FULFILL
                        && entity.getAcknowledgedInstances() != null
                        && Objects.equals(entity.getAcknowledgedInstances(),
                                statusUpdateRequest.getInstanceCount())));
    }

    @Test
    void updateInstanceRequestStatus_withCannotFullFillMessageBatchIdNotFound_throwsException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);
        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        CANNOT_FULFILL, DUMMY_MESSAGE_BATCH_ID, 1, placement, null, null);

        doReturn(Optional.empty()).when(sqsMessageRepository)
                .findByRequestIdAndMessageBatchId(DUMMY_REQUEST_ID, DUMMY_MESSAGE_BATCH_ID);

        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        IcmsNotFoundException icmsNotFoundException =
                assertThrows(IcmsNotFoundException.class, () -> {
                    internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                                statusUpdateRequest, auditProps);
                });

        assertEquals(
                "RequestId dummy_request_id or messageBatchId dummy_message_batch_id doesn't exist",
                icmsNotFoundException.getBody().getDetail());

        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(PENDING_EVALUATION.toString());
    }

    @Test
    void updateInstanceRequestStatus_fulfilment_already_complete_throw_exception() {
        // Prepare
        ClientRequestDataModel clientRequestModel =
                ClientRequestDataModel.builder().sub(DUMMY_CUSTOMER_ID).instanceCount(2)
                        .requestId(DUMMY_CANCELED_REQUEST_ID)
                        .spotInstanceRequestAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                        .launchSpecification(new ClientRequestDataModel.LaunchSpecification(
                                DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE, DUMMY_GPU,
                                null, DUMMY_NON_BYOC_NCA_ID, null,
                                DUMMY_FUNCTION_ID, DUMMY_FUNCTION_VERSION_ID,
                                null, null, null, null, null, null, DUMMY_FUNCTION_NAME, null, DUMMY_NCA_ID_ACCOUNT_NAME)).build();
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(
                        SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT, ResourceProvider.OCI);
        instanceRequestEntity1.setRequest(GsonCompatMapper.toJson(clientRequestModel));

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID, 101,
                        getDummyInstancePlacement(), null, null);

        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest1 =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID + "_1", 101,
                        getDummyInstancePlacement(), null, null);

        doReturn(getDummyInstancePlacementValidationResponse(
                    statusUpdateRequest.getPlacement(), CloudProvider.OCI, ResourceProvider.OCI))
                .when(internalInstanceServiceHelper)
                .validateInstancePlacement(statusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();

        SqsMessageEntity dummySqsMessageEntity = getDummySqsMessageEntity(statusUpdateRequest1);
        dummySqsMessageEntity.setAcknowledgedInstances(100);
        doReturn(List.of(dummySqsMessageEntity)).when(sqsMessageRepository).findByRequestId(DUMMY_REQUEST_ID);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        // Act
        PreConditionFailedException preConditionFailedException =
                assertThrows(PreConditionFailedException.class, () -> {
                    internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                                statusUpdateRequest, auditProps);
                });

        assertEquals("The request is dummy_request_id is already fulfilled.",
                     preConditionFailedException.getBody().getDetail());

        // Assert
        verify(instanceRequestV2Repository).findRequestById(DUMMY_REQUEST_ID);
        verifyNoMoreInteractions(instanceRequestV2Repository);
        verify(sqsMessageRepository).findByRequestId(DUMMY_REQUEST_ID);
        verify(telemetryEventClient).triggerEvent(Mockito.anyList());;
        verify(internalInstanceServiceHelper).validateInstancePlacement(
                statusUpdateRequest.getPlacement(), DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);

        assertThat(instanceRequestEntity1.getState()).isEqualTo(SpotInstanceRequestState.OPEN);
        assertThat(instanceRequestEntity1.getStatusCode()).isEqualTo(
                PENDING_FULFILLMENT.toString());
        assertThat(instanceRequestEntity1.getStatusMessage()).isEqualTo("dummy_message");
        assertThat(instanceRequestEntity1.getStatusUpdateTime()).isNotNull();
    }


    InstanceRequestV2Entity getInstanceRequestEntity(
            SpotInstanceRequestState state,
            SpotRequestStatusCode statusCode,
            ResourceProvider resourceProvider) {
        String instanceType = DUMMY_NON_BYOC_INSTANCE_TYPE ;
        String ncaId = DUMMY_NON_BYOC_NCA_ID;
        String cloudProvider = CloudProvider.OCI.toString();

        if (resourceProvider == ResourceProvider.BYOC) {
            instanceType = DUMMY_BYOC_INSTANCE_TYPE;
            ncaId = DUMMY_BYOC_NCA_ID;
            cloudProvider = CloudProvider.AZURE.toString();
        }

        return InstanceRequestV2Entity.builder()
                .request(GsonCompatMapper.toJson(
                        getDummyClientRequestDataModel(instanceType, ncaId,
                                DUMMY_REQUEST_ID, cloudProvider)))
                .customer(DUMMY_CUSTOMER_1)
                .requestId(DUMMY_REQUEST_ID)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(state)
                .statusCode(statusCode.toString())
                .statusMessage("dummy_message")
                .statusUpdateTime(Instant.now())
                .instanceCount(100)
                .resourceProvider(resourceProvider)
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .build();
    }

    private GenericMetric getDummyMetricForStartedProcessingRequest(
            HashMap<String, Object> metadata, CloudProvider cloudProvider,
            ResourceProvider resourceProvider, String instanceType, String ncaId,
            UUID deploymentId, UUID gpuSpecificationId) {
        return new GenericMetric()
                .withEventName(Events.STARTED_PROCESSING_INSTANCE_REQUEST.toString())
                .withCloudProvider(CloudProvider.OCI)
                .withCustomer(DUMMY_CUSTOMER_1)
                .withRequestId(DUMMY_REQUEST_ID)
                .withInstanceRequestAcceptanceTime(0)
                .withSqsMessageAcknowledgeInstanceCount(1)
                .withMessageBatchId(DUMMY_MESSAGE_BATCH_ID)
                .withMetadata(metadata)
                .withFunctionId(DUMMY_FUNCTION_ID)
                .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                .withCloudProvider(cloudProvider)
                .withResourceProvider(resourceProvider)
                .withInstanceType(instanceType)
                .withNcaId(ncaId)
                .withDeploymentId(deploymentId)
                .withGpuSpecificationId(gpuSpecificationId)
                .withCapacityType(CapacityType.SPOT)
                .withClusterId(DUMMY_ZONE)
                .withRequestState(SpotInstanceRequestState.OPEN.toString());
    }

    private GenericMetric getDummyMetricForSqsBatchStatusUpdate(HashMap<String, Object> metadata,
                                                                CloudProvider cloudProvider,
                                                                ResourceProvider resourceProvider,
                                                                String instanceType, String ncaId,
                                                                UUID deploymentId,
                                                                UUID gpuSpecificationId) {
        return new GenericMetric()
                .withEventName(Events.RECEIVED_SQS_BATCH_STATUS_UPDATE.toString())
                .withCloudProvider(CloudProvider.OCI)
                .withCustomer(DUMMY_CUSTOMER_1)
                .withRequestId(DUMMY_REQUEST_ID)
                .withInstanceRequestAcceptanceTime(0)
                .withSqsMessageAcknowledgeInstanceCount(1)
                .withMessageBatchId(DUMMY_MESSAGE_BATCH_ID)
                .withMetadata(metadata)
                .withClusterId(DUMMY_ZONE)
                .withFunctionId(DUMMY_FUNCTION_ID)
                .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                .withCloudProvider(cloudProvider)
                .withResourceProvider(resourceProvider)
                .withInstanceType(instanceType)
                .withNcaId(ncaId)
                .withDeploymentId(deploymentId)
                .withGpuSpecificationId(gpuSpecificationId)
                .withCapacityType(CapacityType.SPOT)
                .withRequestState(SpotInstanceRequestState.OPEN.toString());
    }

    private boolean validateSqsMessageEntityMock(
            SqsMessageEntity actual,
            SqsMessageEntity expected) {
        return actual.getKey().getRequestId().equals(expected.getKey().getRequestId())
                && actual.getStatus().equals(expected.getStatus())
                && actual.getZone().equals(expected.getZone())
                && actual.getAcknowledgedInstances()
                .equals(expected.getAcknowledgedInstances());
    }

    private SqsMessageEntity getDummySqsMessageEntity(
            SpotInstanceRequestStatusUpdateRequest statusUpdateRequest) {
        return SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(statusUpdateRequest.getMessageBatchId())
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(statusUpdateRequest.getInstanceCount())
                .status(PENDING_FULFILLMENT)
                .zone(DUMMY_ZONE)
                .build();
    }

    @Test
    void testValidateReservationIdForRequestStateUpdate_InvalidReservationId_ThrowsPreConditionFailedException() {
        // Given
        UUID invalidReservationId = UUID.randomUUID();
        SpotInstanceRequestStatusUpdateRequest updateRequest = 
                new SpotInstanceRequestStatusUpdateRequest();
        updateRequest.setStatus(PENDING_FULFILLMENT);
        updateRequest.setInstanceCount(1);
        updateRequest.setPlacement(getDummyInstancePlacement());
        updateRequest.setReservationId(invalidReservationId);
        updateRequest.setCapacityType(CapacityType.RESERVED_BACKUP);
        updateRequest.setMessageBatchId(DUMMY_MESSAGE_BATCH_ID);

        InstanceRequestV2Entity instanceRequestEntity = getInstanceRequestEntity(
                SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);

        // Mock repository and other dependencies
        doReturn(Optional.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doReturn(getDummyInstancePlacementValidationResponse(getDummyInstancePlacement(), CloudProvider.OCI, ResourceProvider.OCI))
                .when(internalInstanceServiceHelper).validateInstancePlacement(Mockito.any(), Mockito.any(), Mockito.any());

        // Mock invalid reservationId scenario
        when(reservationCapacityValidationHelper.validateAndGetReservationEntity(invalidReservationId))
                .thenThrow(new PreConditionFailedException("Invalid reservationId " + invalidReservationId + " provided"));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                        updateRequest, auditProps)
        );

        assertTrue(exception.getMessage().contains("Invalid reservationId"));
        assertTrue(exception.getMessage().contains(invalidReservationId.toString()));

        // Verify telemetry was sent
        verify(telemetryEventClient).triggerEvent(Mockito.any());
    }

    @Test 
    void testValidateReservationIdForRequestStateUpdate_FullyUtilizedReservation_ThrowsPreConditionFailedException() {
        // Given
        UUID testReservationId = UUID.randomUUID();
        SpotInstanceRequestStatusUpdateRequest updateRequest = 
                new SpotInstanceRequestStatusUpdateRequest();
        updateRequest.setStatus(PENDING_FULFILLMENT);
        updateRequest.setReservationId(testReservationId);
        updateRequest.setMessageBatchId(DUMMY_MESSAGE_BATCH_ID);
        updateRequest.setCapacityType(CapacityType.RESERVED_BACKUP);
        updateRequest.setInstanceCount(2);
        updateRequest.setPlacement(getDummyInstancePlacement());

        InstanceRequestV2Entity instanceRequestEntity = getInstanceRequestEntity(
                SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);

        // Mock repository and other dependencies
        doReturn(Optional.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doReturn(getDummyInstancePlacementValidationResponse(getDummyInstancePlacement(), CloudProvider.OCI, ResourceProvider.OCI))
                .when(internalInstanceServiceHelper).validateInstancePlacement(Mockito.any(), Mockito.any(), Mockito.any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        // Mock valid reservation but capacity validation failure
        ReservationEntity mockReservation = ReservationEntity.builder()
                .ncaId(DUMMY_NON_BYOC_NCA_ID)
                .clusterId(DUMMY_ZONE)
                .reservationId(testReservationId)
                .reservedGpuCount(4)
                .build();
        
        when(reservationCapacityValidationHelper.validateAndGetReservationEntity(testReservationId))
                .thenReturn(Optional.of(mockReservation));
        
        // Mock fully utilized scenario
        doThrow(new PreConditionFailedException("Reservation " + testReservationId + " is fully utilized"))
                .when(reservationCapacityValidationHelper)
                .validateReservationBackupCapacityForRequestStateUpdate(
                        Mockito.eq(mockReservation), 
                        Mockito.eq(instanceRequestEntity), 
                        Mockito.eq(2));

        // When & Then
        PreConditionFailedException exception = assertThrows(
                PreConditionFailedException.class,
                () -> internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                        updateRequest, auditProps)
        );

        assertTrue(exception.getMessage().contains("fully utilized"));
        assertTrue(exception.getMessage().contains(testReservationId.toString()));

        // Verify telemetry was sent
        verify(telemetryEventClient).triggerEvent(Mockito.any());
    }

    @Test
    void testValidateReservationIdForRequestStateUpdate_ValidReservation_Success() {
        // Given
        UUID testReservationId = UUID.randomUUID();
        SpotInstanceRequestStatusUpdateRequest updateRequest = 
                new SpotInstanceRequestStatusUpdateRequest();
        updateRequest.setStatus(PENDING_FULFILLMENT);
        updateRequest.setReservationId(testReservationId);
        updateRequest.setCapacityType(CapacityType.RESERVED_BACKUP);
        updateRequest.setInstanceCount(1);
        updateRequest.setPlacement(getDummyInstancePlacement());
        updateRequest.setMessageBatchId(DUMMY_MESSAGE_BATCH_ID);

        InstanceRequestV2Entity instanceRequestEntity = getInstanceRequestEntity(
                SpotInstanceRequestState.OPEN, PENDING_EVALUATION, ResourceProvider.OCI);

        // Mock successful reservation validation
        ReservationEntity mockReservation = ReservationEntity.builder()
                .ncaId(DUMMY_NON_BYOC_NCA_ID)
                .clusterId(DUMMY_ZONE)
                .reservationId(testReservationId)
                .reservedGpuCount(4)
                .build();
        
        when(reservationCapacityValidationHelper.validateAndGetReservationEntity(testReservationId))
                .thenReturn(Optional.of(mockReservation));
        
        // Mock successful capacity validation
        doNothing().when(reservationCapacityValidationHelper)
                .validateReservationBackupCapacityForRequestStateUpdate(
                        Mockito.eq(mockReservation), 
                        Mockito.eq(instanceRequestEntity), 
                        Mockito.eq(1));

        // Mock other dependencies
        doReturn(Optional.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doReturn(getDummyInstancePlacementValidationResponse(getDummyInstancePlacement(), CloudProvider.OCI, ResourceProvider.OCI))
                .when(internalInstanceServiceHelper).validateInstancePlacement(Mockito.any(), Mockito.any(), Mockito.any());
        doNothing().when(instanceRequestV2Repository).update(instanceRequestEntity);
        doNothing().when(auditService).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(), Mockito.any());
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        // When & Then - Should complete without exceptions
        assertDoesNotThrow(() -> 
            internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                    updateRequest, auditProps)
        );

        // Verify reservation validation was called
        verify(reservationCapacityValidationHelper).validateAndGetReservationEntity(testReservationId);
        verify(reservationCapacityValidationHelper).validateReservationBackupCapacityForRequestStateUpdate(
                mockReservation, instanceRequestEntity, 1);
    }

    @Test
    void updateInstanceRequestStatus_withScheduleExpired_existingBatch_persistsStatusAndPreservesAck() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT,
                                     ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();
        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        SCHEDULE_EXPIRED, DUMMY_MESSAGE_BATCH_ID, 5, placement, null,
                        CapacityType.SPOT);

        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);

        SqsMessageEntity existingBatch = getDummySqsMessageEntity(statusUpdateRequest);
        existingBatch.setAcknowledgedInstances(5);
        existingBatch.setStatus(PENDING_FULFILLMENT);

        doReturn(Optional.of(existingBatch)).when(sqsMessageRepository)
                .findByRequestIdAndMessageBatchId(DUMMY_REQUEST_ID, DUMMY_MESSAGE_BATCH_ID);
        doNothing().when(sqsMessageRepository).update(Mockito.any(SqsMessageEntity.class));
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        verify(sqsMessageRepository).findByRequestIdAndMessageBatchId(DUMMY_REQUEST_ID,
                                                                     DUMMY_MESSAGE_BATCH_ID);
        verify(sqsMessageRepository).update(Mockito.argThat(entity ->
                entity.getStatus() == SCHEDULE_EXPIRED
                        && entity.getAcknowledgedInstances() != null
                        && entity.getAcknowledgedInstances() == 5));
    }

    @Test
    void updateInstanceRequestStatus_withScheduleExpired_batchNotFound_telemetryOnly() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT,
                                     ResourceProvider.OCI);

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);

        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();
        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        SCHEDULE_EXPIRED, DUMMY_MESSAGE_BATCH_ID, 5, placement, null,
                        CapacityType.SPOT);

        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        doReturn(Optional.empty()).when(sqsMessageRepository)
                .findByRequestIdAndMessageBatchId(DUMMY_REQUEST_ID, DUMMY_MESSAGE_BATCH_ID);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                    statusUpdateRequest, auditProps);

        verify(sqsMessageRepository).findByRequestIdAndMessageBatchId(DUMMY_REQUEST_ID,
                                                                     DUMMY_MESSAGE_BATCH_ID);
        verify(sqsMessageRepository, Mockito.never()).update(Mockito.any(SqsMessageEntity.class));
    }

    @Test
    void pendingFulfillment_afterCannotFulfillFromAnotherBatch_isAcceptedNotOverAllocated() {
        // request capacity = 100. batch1 cannot-fulfill with ack=60 (preserved).
        // Incoming batch2 ack=60: new filter excludes the cannot-fulfill batch from
        // getAlreadyAllocated, so 0+60 <= 100 => accepted.
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT,
                                     ResourceProvider.OCI);
        instanceRequestEntity1.setCheckBatchwiseInfo(true);

        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();
        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID + "_2", 60, placement, null,
                        CapacityType.SPOT);

        SqsMessageEntity cannotFulfillBatch = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID + "_1")
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(60)
                .status(CANNOT_FULFILL)
                .zone(DUMMY_ZONE)
                .build();

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doReturn(List.of(cannotFulfillBatch)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_REQUEST_ID);
        doNothing().when(sqsMessageRepository).update(Mockito.any(SqsMessageEntity.class));
        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        assertDoesNotThrow(() -> internalInstanceService.updateInstanceRequestStatus(
                DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID, statusUpdateRequest, auditProps));

        verify(sqsMessageRepository).update(Mockito.argThat(entity ->
                entity.getKey().getMessageBatchId().equals(DUMMY_MESSAGE_BATCH_ID + "_2")
                        && entity.getStatus() == PENDING_FULFILLMENT
                        && entity.getAcknowledgedInstances() == 60));
    }

    @Test
    void pendingFulfillment_overAllocationGuard_stillRejectsWhenPendingBatchesExceedCapacity() {
        // request capacity = 100. batch1 PENDING with ack=60. Incoming batch2 ack=60.
        // 60+60 > 100 => 412 (filter does NOT skip pending batches).
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT,
                                     ResourceProvider.OCI);
        instanceRequestEntity1.setCheckBatchwiseInfo(true);

        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();
        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID + "_2", 60, placement, null,
                        CapacityType.SPOT);

        SqsMessageEntity pendingBatch = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID + "_1")
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(60)
                .status(PENDING_FULFILLMENT)
                .zone(DUMMY_ZONE + "-other")
                .build();

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doReturn(List.of(pendingBatch)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_REQUEST_ID);
        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        assertThrows(PreConditionFailedException.class, () ->
                internalInstanceService.updateInstanceRequestStatus(DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID,
                                                            statusUpdateRequest, auditProps));
    }

    @Test
    void pendingFulfillment_nullAckOnExistingBatch_doesNotNpeAndIsSkipped() {
        // Defensive: a legacy row with null ack must not NPE in getAlreadyAllocated.
        InstanceRequestV2Entity instanceRequestEntity1 =
                getInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT,
                                     ResourceProvider.OCI);
        instanceRequestEntity1.setCheckBatchwiseInfo(true);

        SpotInstanceStatusUpdateRequest.InstancePlacement placement = getDummyInstancePlacement();
        SpotInstanceRequestStatusUpdateRequest statusUpdateRequest =
                new SpotInstanceRequestStatusUpdateRequest(
                        PENDING_FULFILLMENT, DUMMY_MESSAGE_BATCH_ID + "_2", 50, placement, null,
                        CapacityType.SPOT);

        SqsMessageEntity legacyNullAckBatch = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID + "_legacy")
                             .requestId(DUMMY_REQUEST_ID)
                             .build())
                .acknowledgedInstances(null)
                .status(PENDING_FULFILLMENT)
                .zone(DUMMY_ZONE + "-legacy")
                .build();

        doReturn(Optional.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestById(DUMMY_REQUEST_ID);
        doReturn(30).when(icmsConfigurationProperties).getRequestCancelDurationInMin();
        doReturn(List.of(legacyNullAckBatch)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_REQUEST_ID);
        doNothing().when(sqsMessageRepository).update(Mockito.any(SqsMessageEntity.class));
        doReturn(getDummyInstancePlacementValidationResponse(placement, CloudProvider.OCI,
                                                             ResourceProvider.OCI)).when(
                        internalInstanceServiceHelper)
                .validateInstancePlacement(placement, DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.OCI.toString()).getLaunchSpecification());

        assertDoesNotThrow(() -> internalInstanceService.updateInstanceRequestStatus(
                DUMMY_CLUSTER_ID, DUMMY_REQUEST_ID, statusUpdateRequest, auditProps));
    }
}
