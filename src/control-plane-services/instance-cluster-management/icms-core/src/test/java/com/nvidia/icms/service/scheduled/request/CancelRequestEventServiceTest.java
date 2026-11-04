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

import static com.nvidia.icms.service.scheduled.request.CancelRequestEventService.NO_CAPACITY_USER_VISIBLE_MSG;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.MESSAGE_BATCH_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.PREVIOUS_REQUEST_STATE;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.PREVIOUS_REQUEST_STATUS;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData.REQUEST_STATUS;
import static com.nvidia.icms.util.TestUtil.DUMMY_CANCELED_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_VERSION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_MESSAGE_BATCH_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NCA_ID_ACCOUNT_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static com.nvidia.icms.util.TestUtil.getDummyClientRequestDataModel;
import static java.util.Collections.emptyList;
import static java.util.Collections.emptySet;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.atLeastOnce;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.factory.InstanceEntityFactory;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.instance.InstanceTestBase;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.SqsMessageRepository;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageKey;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.service.AppAuditService;
import com.nvidia.icms.service.InstanceServiceHelper;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TestUtil;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class CancelRequestEventServiceTest extends InstanceTestBase {

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    @Mock
    private AppAuditService auditService;

    private CancelRequestEventService cancelRequestEventService;

    @Mock
    IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    InstanceV2Repository instanceV2Repository;

    @Mock
    TelemetryEventClient telemetryEventClient;

    @Mock
    SqsMessageRepository sqsMessageRepository;

    @Mock
    CloudHealthRepository cloudHealthRepository;

    @Mock
    InstanceServiceHelper instanceServiceHelper;

    ObjectMapper objectMapper;

    private final Integer CANCEL_REQUEST_OLDER_THAN_DAYS = 0;

    private final Integer REQUEST_CANCEL_DURATION_IN_MIN = 30;

    @BeforeEach
    void init() {
        this.objectMapper = TestUtil.customObjectMapper();
        this.cancelRequestEventService =
                new CancelRequestEventService(icmsConfigurationProperties,
                                              instanceRequestV2Repository,
                                              instanceV2Repository,
                                              auditService, telemetryEventClient, objectMapper,
                                              cloudHealthRepository,
                                              sqsMessageRepository, instanceServiceHelper,
                                              ComputePlatformTestFixtures.nonByocComputePlatformService());
    }

    private IcmsConfigurationProperties.MessageBatchIdConfig getDummyMessageBatchConfig() {
        return IcmsConfigurationProperties.MessageBatchIdConfig.builder()
                .cancelRequestValidationEnabled(true)
                .validationDurationInMin(35)
                .validationDurationForByocWithModelInMin(160)
                .validationDurationForByocWithoutModelInMin(35)
                .build();
    }

    @Test
    void execute_withValidRequests_cancelsRequestsSuccessfully() {
        doReturn(REQUEST_CANCEL_DURATION_IN_MIN).when(icmsConfigurationProperties)
                .getRequestCancelDurationInMin();
        doReturn(1).when(icmsConfigurationProperties).getCancelRequestUpToPastMonths();

        ClientRequestDataModel clientRequestDataModel =
                getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                        DUMMY_REQUEST_ID, CloudProvider.AWS.toString());
        String request = GsonCompatMapper.toJson(clientRequestDataModel);

        // This request was created on same day
        InstanceRequestV2Entity instanceRequestEntityWithCanceledState = InstanceRequestV2Entity.builder()
                .customer(DUMMY_CUSTOMER_1)
                .requestId(DUMMY_REQUEST_ID + "_3")
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()
                        .minus(REQUEST_CANCEL_DURATION_IN_MIN + 10, ChronoUnit.HOURS)))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(SpotInstanceRequestState.CANCELED)
                .statusCode(SpotRequestStatusCode.SCHEDULE_EXPIRED.toString())
                .statusMessage("dummy_message")
                .statusUpdateTime(Instant.now())
                .request(request)
                .checkBatchwiseInfo(true)
                .deploymentId(clientRequestDataModel.getLaunchSpecification().getDeploymentId())
                .gpuSpecificationId(clientRequestDataModel.getLaunchSpecification().getGpuSpecificationId())
                .build();

        InstanceRequestV2Entity instanceRequestEntityWithClsoedStateState = InstanceRequestV2Entity.builder()
                .customer(DUMMY_CUSTOMER_1)
                .requestId(DUMMY_REQUEST_ID + "_4")
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()
                        .minus(REQUEST_CANCEL_DURATION_IN_MIN + 10, ChronoUnit.HOURS)))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(SpotInstanceRequestState.CLOSED)
                .statusCode(SpotRequestStatusCode.INSTANCE_TERMINATED_BY_SERVICE.toString())
                .statusMessage("dummy_message")
                .statusUpdateTime(Instant.now())
                .request(request)
                .checkBatchwiseInfo(true)
                .deploymentId(clientRequestDataModel.getLaunchSpecification().getDeploymentId())
                .gpuSpecificationId(clientRequestDataModel.getLaunchSpecification().getGpuSpecificationId())
                .build();

        // This request was created on same day
        InstanceRequestV2Entity instanceRequestEntity1 = InstanceRequestV2Entity.builder()
                .customer(DUMMY_CUSTOMER_1)
                .requestId(DUMMY_REQUEST_ID + "_1")
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()
                                    .minus(REQUEST_CANCEL_DURATION_IN_MIN + 10, ChronoUnit.MINUTES)))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(SpotInstanceRequestState.OPEN)
                .statusCode(SpotRequestStatusCode.PENDING_EVALUATION.toString())
                .statusMessage("dummy_message")
                .resourceProvider(ResourceProvider.BYOC)
                .statusUpdateTime(Instant.now())
                .request(request)
                .checkBatchwiseInfo(true)
                .deploymentId(clientRequestDataModel.getLaunchSpecification().getDeploymentId())
                .gpuSpecificationId(clientRequestDataModel.getLaunchSpecification().getGpuSpecificationId())
                .build();

        // This request was created on previous day and 0 acked instances
        InstanceRequestV2Entity instanceRequestEntity2 = InstanceRequestV2Entity.builder()
                .customer(DUMMY_CUSTOMER_1)
                .requestId(DUMMY_REQUEST_ID + "_2")
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()
                                    .minus(CANCEL_REQUEST_OLDER_THAN_DAYS + 1, ChronoUnit.DAYS)))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(SpotInstanceRequestState.OPEN)
                .statusCode(SpotRequestStatusCode.PENDING_FULFILLMENT.toString())
                .statusMessage("dummy_message")
                .statusUpdateTime(Instant.now())
                .resourceProvider(ResourceProvider.BYOC)
                .request(request)
                .checkBatchwiseInfo(true)
                .deploymentId(clientRequestDataModel.getLaunchSpecification().getDeploymentId())
                .gpuSpecificationId(clientRequestDataModel.getLaunchSpecification().getGpuSpecificationId())
                .build();

        doReturn(emptySet()).when(cloudHealthRepository).finalAllHealthyZones();
        doReturn(emptyList()).when(sqsMessageRepository).findByRequestId(DUMMY_REQUEST_ID + "_2");
        doReturn(getDummyMessageBatchConfig()).when(icmsConfigurationProperties).getMessageBatchIdConfig();
        doReturn(List.of(instanceRequestEntity1, instanceRequestEntity2,
                instanceRequestEntityWithClsoedStateState, instanceRequestEntityWithCanceledState)).when(
                instanceRequestV2Repository).findRequestsInLastMonths(1);

        List<InstanceV2Entity> instanceEntities = new ArrayList<>();
        instanceEntities.add(getInstanceEntityForRunningInstance());
        doReturn(instanceEntities).when(instanceV2Repository)
                .findAllByCustomer(DUMMY_CUSTOMER_1);

        doReturn(clientRequestDataModel.getLaunchSpecification()).when(instanceServiceHelper)
                .getLaunchSpecificationForTelemetry(Mockito.any());
        Map<String, Object> metadata = new HashMap<>();
        metadata.put(MESSAGE_BATCH_STATUS.getName(), instanceRequestEntity1.getCheckBatchwiseInfo());
        metadata.put(REQUEST_STATUS.getName(), SpotRequestStatusCode.SCHEDULE_EXPIRED.toString());
        metadata.put(PREVIOUS_REQUEST_STATUS.getName(), SpotRequestStatusCode.PENDING_EVALUATION.toString());
        metadata.put(PREVIOUS_REQUEST_STATE.getName(), SpotInstanceRequestState.OPEN.toString());

        Map<String, Object> metadata2 = new HashMap<>();
        metadata2.put(MESSAGE_BATCH_STATUS.getName(), instanceRequestEntity1.getCheckBatchwiseInfo());
        metadata2.put(REQUEST_STATUS.getName(), SpotRequestStatusCode.SCHEDULE_EXPIRED.toString());
        metadata2.put(PREVIOUS_REQUEST_STATUS.getName(), SpotRequestStatusCode.PENDING_FULFILLMENT.toString());
        metadata2.put(PREVIOUS_REQUEST_STATE.getName(), SpotInstanceRequestState.OPEN.toString());

        GenericMetric genericMetric1 = new GenericMetric()
                .withRequestId(DUMMY_REQUEST_ID + "_1")
                .withEventName(Events.ASYNC_TASK_CANCEL_INSTANCE_REQUEST.toString())
                .withCustomer(DUMMY_CUSTOMER_1)
                .withMetadata(metadata)
                .withResourceProvider(ResourceProvider.BYOC)
                .withNcaId(clientRequestDataModel.getLaunchSpecification().getNcaId())
                .withFunctionId(clientRequestDataModel.getLaunchSpecification().getFunctionId())
                .withFunctionVersionId(clientRequestDataModel.getLaunchSpecification().getVersionId())
                .withInstanceType(clientRequestDataModel.getLaunchSpecification().getInstanceType())
                .withRequestState(SpotInstanceRequestState.CANCELED.toString())
                .withDeploymentId(clientRequestDataModel.getLaunchSpecification().getDeploymentId())
                .withGpuSpecificationId(clientRequestDataModel.getLaunchSpecification().getGpuSpecificationId());

        GenericMetric genericMetric2 = new GenericMetric()
                .withRequestId(DUMMY_REQUEST_ID + "_2")
                .withEventName(Events.ASYNC_TASK_CANCEL_INSTANCE_REQUEST.toString())
                .withCustomer(DUMMY_CUSTOMER_1)
                .withMetadata(metadata2)
                .withResourceProvider(ResourceProvider.BYOC)
                .withNcaId(clientRequestDataModel.getLaunchSpecification().getNcaId())
                .withFunctionId(clientRequestDataModel.getLaunchSpecification().getFunctionId())
                .withFunctionVersionId(clientRequestDataModel.getLaunchSpecification().getVersionId())
                .withInstanceType(clientRequestDataModel.getLaunchSpecification().getInstanceType())
                .withRequestState(SpotInstanceRequestState.CANCELED.toString())
                .withDeploymentId(clientRequestDataModel.getLaunchSpecification().getDeploymentId())
                .withGpuSpecificationId(clientRequestDataModel.getLaunchSpecification().getGpuSpecificationId());


        ClientRequestDataModel clientRequestModel =
                ClientRequestDataModel.builder().sub(DUMMY_CUSTOMER_ID).instanceCount(2)
                        .requestId(DUMMY_CANCELED_REQUEST_ID)
                        .spotInstanceRequestAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                        .launchSpecification(new ClientRequestDataModel.LaunchSpecification(
                                DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE, DUMMY_GPU,
                                CloudProvider.AWS.toString(), DUMMY_NON_BYOC_NCA_ID, null,
                                DUMMY_FUNCTION_ID, DUMMY_FUNCTION_VERSION_ID,
                                null, null, null,
                                clientRequestDataModel.getLaunchSpecification().getDeploymentId(),
                                clientRequestDataModel.getLaunchSpecification().getGpuSpecificationId(),
                                null,
                                DUMMY_FUNCTION_NAME,
                                null,
                                DUMMY_NCA_ID_ACCOUNT_NAME)).build();
        doReturn(clientRequestModel).when(instanceServiceHelper).parseRequestInfo(any());

        cancelRequestEventService.execute();

        verify(instanceRequestV2Repository).findRequestsInLastMonths(1);

        verify(instanceV2Repository).findAllByCustomer(DUMMY_CUSTOMER_1);

        instanceRequestEntity1.setState(SpotInstanceRequestState.CANCELED);
        instanceRequestEntity1.setStatusCode(SpotRequestStatusCode.SCHEDULE_EXPIRED.toString());
        instanceRequestEntity1.setStatusMessage("Your instance request has been canceled");
        instanceRequestEntity1.setStatusUpdateTime(Instant.now());
        verify(instanceRequestV2Repository).update(instanceRequestEntity1);

        instanceRequestEntity2.setState(SpotInstanceRequestState.CANCELED);
        instanceRequestEntity2.setStatusCode(SpotRequestStatusCode.SCHEDULE_EXPIRED.toString());
        instanceRequestEntity2.setStatusMessage("Your instance request has been canceled");
        instanceRequestEntity2.setStatusUpdateTime(Instant.now());
        verify(instanceRequestV2Repository).update(instanceRequestEntity2);

        verify(auditService, times(2)).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(),
                                                                    Mockito.any());
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric1));
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric2));
        verify(cloudHealthRepository).finalAllHealthyZones();
        verify(sqsMessageRepository).findByRequestId(DUMMY_REQUEST_ID + "_2");
        verify(icmsConfigurationProperties).getCancelRequestUpToPastMonths();
        verify(instanceServiceHelper, times(2)).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void execute_withNoInstancesCreatedButAckedInstancesPresent_ignoreCancellation()
            throws JacksonException {
        doReturn(REQUEST_CANCEL_DURATION_IN_MIN).when(icmsConfigurationProperties)
                .getRequestCancelDurationInMin();
        doReturn(1).when(icmsConfigurationProperties).getCancelRequestUpToPastMonths();
        doReturn(getDummyMessageBatchConfig()).when(icmsConfigurationProperties).getMessageBatchIdConfig();

        String sqsRequest = getSqsRequest(DUMMY_NON_BYOC_INSTANCE_TYPE);

        // This request was created on previous day and 0 acked instances
        InstanceRequestV2Entity instanceRequestEntity = InstanceRequestV2Entity.builder()
                .customer(DUMMY_CUSTOMER_1)
                .requestId(DUMMY_REQUEST_ID)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()
                                    .minus(CANCEL_REQUEST_OLDER_THAN_DAYS + 1, ChronoUnit.DAYS)))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(SpotInstanceRequestState.OPEN)
                .statusCode(SpotRequestStatusCode.PENDING_FULFILLMENT.toString())
                .statusMessage("dummy_message")
                .statusUpdateTime(Instant.now())
                .request(sqsRequest)
                .checkBatchwiseInfo(true)
                .resourceProvider(ResourceProvider.BYOC)
                .build();

        doReturn(List.of(instanceRequestEntity)).when(
                instanceRequestV2Repository).findRequestsInLastMonths(1);

        // Mocking 0 reported instances
        doReturn(emptyList()).when(instanceV2Repository)
                .findAllByCustomer(DUMMY_CUSTOMER_1);

        // Mocking 2 acked instances
        doReturn(Set.of(DUMMY_ZONE)).when(cloudHealthRepository).finalAllHealthyZones();
        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID)
                             .requestId(DUMMY_REQUEST_ID)
                             .build())

                .creationTime(Instant.now().minus(10, ChronoUnit.SECONDS))
                .acknowledgedInstances(2)
                .status(SpotRequestStatusCode.PENDING_FULFILLMENT)
                .zone(DUMMY_ZONE)
                .build();
        doReturn(List.of(sqsMessageEntity)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_REQUEST_ID);
        ClientRequestDataModel clientRequestModel =
                ClientRequestDataModel.builder().sub(DUMMY_CUSTOMER_ID).instanceCount(2)
                        .requestId(DUMMY_CANCELED_REQUEST_ID)
                        .spotInstanceRequestAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                        .launchSpecification(new ClientRequestDataModel.LaunchSpecification(
                                DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE, DUMMY_GPU,
                                CloudProvider.AWS.toString(), DUMMY_NON_BYOC_NCA_ID, null,
                                DUMMY_FUNCTION_ID, DUMMY_FUNCTION_VERSION_ID,
                                null, null, null, null, null,
                                null, DUMMY_FUNCTION_NAME, null, DUMMY_NCA_ID_ACCOUNT_NAME)).build();
        doReturn(clientRequestModel).when(instanceServiceHelper).parseRequestInfo(any());

        cancelRequestEventService.execute();

        verify(instanceRequestV2Repository).findRequestsInLastMonths(1);
        verifyNoMoreInteractions(instanceRequestV2Repository);

        verify(instanceV2Repository).findAllByCustomer(DUMMY_CUSTOMER_1);
        verify(cloudHealthRepository).finalAllHealthyZones();
        verify(sqsMessageRepository).findByRequestId(DUMMY_REQUEST_ID);
        verifyNoInteractions(telemetryEventClient);
        verifyNoInteractions(auditService);
        verify(icmsConfigurationProperties).getCancelRequestUpToPastMonths();
        verify(icmsConfigurationProperties, atLeastOnce()).getMessageBatchIdConfig();
    }

    @Test
    void execute_withPendingEvaluationRequest_cancelsRequestsSuccessfully()
            throws JacksonException {
        doReturn(REQUEST_CANCEL_DURATION_IN_MIN).when(icmsConfigurationProperties)
                .getRequestCancelDurationInMin();
        doReturn(1).when(icmsConfigurationProperties).getCancelRequestUpToPastMonths();

        ClientRequestDataModel clientRequestModel =
                ClientRequestDataModel.builder().sub(DUMMY_CUSTOMER_ID).instanceCount(2)
                        .requestId(DUMMY_CANCELED_REQUEST_ID)
                        .spotInstanceRequestAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                        .launchSpecification(new ClientRequestDataModel.LaunchSpecification(
                                DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE, DUMMY_GPU,
                                CloudProvider.AWS.toString(), DUMMY_NON_BYOC_NCA_ID, null,
                                DUMMY_FUNCTION_ID,
                                DUMMY_FUNCTION_VERSION_ID,
                                null, null, null,
                                UUID.randomUUID(),
                                UUID.randomUUID(),
                                null,
                                DUMMY_FUNCTION_NAME,
                                 null,
                                DUMMY_NCA_ID_ACCOUNT_NAME)).build();
        // This request was created on same day
        InstanceRequestV2Entity instanceRequestEntity1 = InstanceRequestV2Entity.builder()
                .customer(DUMMY_CUSTOMER_1)
                .requestId(DUMMY_REQUEST_ID + "_1")
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()
                                    .minus(REQUEST_CANCEL_DURATION_IN_MIN + 10, ChronoUnit.MINUTES)))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(SpotInstanceRequestState.OPEN)
                .statusCode(SpotRequestStatusCode.PENDING_EVALUATION.toString())
                .statusMessage("dummy_message")
                .statusUpdateTime(Instant.now())
                .request(objectMapper.writeValueAsString(clientRequestModel))
                .resourceProvider(ResourceProvider.BYOC)
                .deploymentId(clientRequestModel.getLaunchSpecification().getDeploymentId())
                .gpuSpecificationId(clientRequestModel.getLaunchSpecification().getGpuSpecificationId())
                .build();

        doReturn(List.of(instanceRequestEntity1)).when(
                instanceRequestV2Repository).findRequestsInLastMonths(1);
        when(instanceServiceHelper.getLaunchSpecificationForTelemetry(Mockito.any())).thenReturn(
                clientRequestModel.getLaunchSpecification());

        Map<String, Object> metadata = new HashMap<>();
        metadata.put(MESSAGE_BATCH_STATUS.getName(), instanceRequestEntity1.getCheckBatchwiseInfo());
        metadata.put(REQUEST_STATUS.getName(), SpotRequestStatusCode.SCHEDULE_EXPIRED.toString());
        metadata.put(PREVIOUS_REQUEST_STATUS.getName(), SpotRequestStatusCode.PENDING_EVALUATION.toString());
        metadata.put(PREVIOUS_REQUEST_STATE.getName(), SpotInstanceRequestState.OPEN.toString());

        GenericMetric genericMetric1 = new GenericMetric()
                .withRequestId(DUMMY_REQUEST_ID + "_1")
                .withEventName(Events.ASYNC_TASK_CANCEL_INSTANCE_REQUEST.toString())
                .withCustomer(DUMMY_CUSTOMER_1)
                .withResourceProvider(ResourceProvider.BYOC)
                .withMetadata(metadata)
                .withFunctionId(DUMMY_FUNCTION_ID)
                .withFunctionVersionId(DUMMY_FUNCTION_VERSION_ID)
                .withNcaId(DUMMY_NON_BYOC_NCA_ID)
                .withNcaIdPartnerName(DUMMY_NCA_ID_ACCOUNT_NAME)
                .withInstanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .withRequestState(SpotInstanceRequestState.CANCELED.toString())
                .withDeploymentId(clientRequestModel.getLaunchSpecification().getDeploymentId())
                .withGpuSpecificationId(clientRequestModel.getLaunchSpecification().getGpuSpecificationId());

        doNothing().when(telemetryEventClient).triggerEvent(List.of(genericMetric1));

        cancelRequestEventService.execute();

        verify(instanceRequestV2Repository).findRequestsInLastMonths(1);
        verify(instanceRequestV2Repository).update(instanceRequestEntity1);

        assertEquals(SpotRequestStatusCode.SCHEDULE_EXPIRED.toString(),
                     instanceRequestEntity1.getStatusCode());
        assertEquals(NO_CAPACITY_USER_VISIBLE_MSG.formatted(
                             CloudProvider.AWS.toString(), DUMMY_GPU, DUMMY_NON_BYOC_INSTANCE_TYPE, REQUEST_CANCEL_DURATION_IN_MIN),
                     instanceRequestEntity1.getStatusMessage());
        verify(auditService, times(1)).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(),
                                                                    Mockito.any());
        verify(telemetryEventClient).triggerEvent(List.of(genericMetric1));
        verify(icmsConfigurationProperties).getCancelRequestUpToPastMonths();
        verify(instanceServiceHelper).getLaunchSpecificationForTelemetry(Mockito.any());
    }

    @Test
    void execute_withValidRequestsAndDBUpdateFailure_throwsException()
            throws JacksonException {
        InstanceRequestV2Entity instanceRequestEntity1 =
                getDummyOlderInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                               SpotRequestStatusCode.PENDING_EVALUATION,
                                               DUMMY_NON_BYOC_INSTANCE_TYPE);
        doReturn(List.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestsInLastMonths(1);
        doReturn(1).when(icmsConfigurationProperties).getCancelRequestUpToPastMonths();

        Mockito.doThrow(new RuntimeException("dummy_error"))
                .when(instanceRequestV2Repository).update(Mockito.any());

        RuntimeException exception = assertThrows(RuntimeException.class,
                                                  () -> cancelRequestEventService.execute());

        Assertions.assertEquals("dummy_error", exception.getMessage());
        verify(instanceRequestV2Repository).findRequestsInLastMonths(1);

        instanceRequestEntity1.setState(SpotInstanceRequestState.CANCELED);
        instanceRequestEntity1.setStatusCode(SpotRequestStatusCode.SCHEDULE_EXPIRED.toString());
        instanceRequestEntity1.setStatusMessage("Your instance request has been canceled");
        instanceRequestEntity1.setStatusUpdateTime(Instant.now());
        verify(instanceRequestV2Repository).update(instanceRequestEntity1);
        verify(auditService, never()).sendAuditEventForInstanceRequest(Mockito.any(), Mockito.any(),
                                                                   Mockito.any());
        verify(icmsConfigurationProperties).getCancelRequestUpToPastMonths();
    }

    private InstanceRequestV2Entity getDummyOlderInstanceRequestEntity(
            SpotInstanceRequestState state,
            SpotRequestStatusCode statusCode,
            String instanceType) throws JacksonException {
        return InstanceRequestV2Entity.builder()
                .customer(DUMMY_CUSTOMER_1)
                .requestId(DUMMY_REQUEST_ID)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(Instant.now()
                                    .minus(CANCEL_REQUEST_OLDER_THAN_DAYS + 3, ChronoUnit.DAYS)))
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                .state(state)
                .statusCode(statusCode.toString())
                .statusMessage("dummy_message")
                .statusUpdateTime(Instant.now())
                .request(getSqsRequest(instanceType))
                .build();
    }

    InstanceV2Entity getInstanceEntityForRunningInstance() {

        InstanceV2Entity instanceEntity = InstanceEntityFactory.createDefaultInstanceV2(DUMMY_INSTANCE_ID,
                                                                                                        DUMMY_REQUEST_ID,
                                                                                                        Instant.now().truncatedTo(ChronoUnit.DAYS),
                                                                                                        DUMMY_CUSTOMER_1,
                                                                                                        null);
        instanceEntity.setInstanceUpdateTime(Instant.now());
        instanceEntity.setZone(DUMMY_ZONE);
        instanceEntity.setInstanceStateCode(16);
        instanceEntity.setInstanceStateName(SpotInstanceInternalState.RUNNING);
        instanceEntity.setRequestState(SpotInstanceRequestState.ACTIVE);
        instanceEntity.setRequestStatusCode(SpotInstanceStatus.FULFILLED);
        instanceEntity.setRequestStatusMessage("message");
        instanceEntity.setImageId(DUMMY_CONTAINER_IMAGE);
        return instanceEntity;
    }

    private String getSqsRequest(String instanceType) throws JacksonException {
        ByocSqsMessageModel sqsMessageModel =
                ByocSqsMessageModel.builder().sub(DUMMY_CUSTOMER_1).instanceCount(2)
                        .requestId(DUMMY_REQUEST_ID)
                        .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES.getRequestAction())
                        .launchSpecification(ByocSqsMessageModel.ByocLaunchSpecification.builder()
                                .instanceType(instanceType)
                                .containerImage(DUMMY_CONTAINER_IMAGE)
                                .environment(DUMMY_ENVIRONMENT_VALUE)
                                .instanceTypeName(instanceType)
                                .instanceTypeValue(instanceType)
                                .instanceCount(2)
                                .gpuType(DUMMY_GPU)
                                .build())
                        .build();
        return objectMapper.writeValueAsString(sqsMessageModel);

    }
}
