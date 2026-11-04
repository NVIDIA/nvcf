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

import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.CANCELED_BEFORE_FULFILLMENT;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_EVALUATION;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_FULFILLMENT;
import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.SCHEDULE_EXPIRED;
import static com.nvidia.icms.service.scheduled.request.CancelRequestEventService.NO_CAPACITY_USER_VISIBLE_MSG;
import static com.nvidia.icms.service.scheduled.request.CancelRequestEventService.UNABLE_TO_FULFILL_REQUEST_USER_VISIBLE_MSG;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CANCELED_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FAILED_CONTAINER_LOG;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_VERSION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static com.nvidia.icms.util.TestUtil.DUMMY_MESSAGE_BATCH_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NCA_ID_ACCOUNT_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_RUNNING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_STARTING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_TERMINATED_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static com.nvidia.icms.util.TestUtil.getDummyClientRequestDataModel;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceEntity;
import static com.nvidia.icms.util.TestUtil.getDummyInstanceRequestEntity;
import static org.assertj.core.api.AssertionsForClassTypes.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.factory.RandomFactory;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.service.platform.ComputePlatformTestFixtures;
import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.HealthInfo;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstance;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceLaunchSpecification;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.service.extensions.api.InstanceDescriptionHelper;
import com.nvidia.icms.outbound.cassandra.cloudhealth.CloudHealthRepository;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.SqsMessageRepository;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageKey;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import com.nvidia.icms.util.CopyUtil;
import com.nvidia.icms.util.TestUtil;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;


@ExtendWith(MockitoExtension.class)
class DescribeAndCancelInstanceServiceTest {

    @Mock
    private InstanceV2Repository instanceV2Repository;

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    @Mock
    private AppAuditService auditService;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private InstanceDescriptionHelper instanceDescriptionHelper;

    @Mock
    private ByocDescribeHelper byocDescribeHelper;

    @Mock
    private SqsMessageRepository sqsMessageRepository;

    @Mock
    private CloudHealthRepository cloudHealthRepository;

    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    private Instant dummyTime;

    private DescribeAndCancelInstanceService describeAndCancelInstanceService;

    @BeforeEach
    void init() {
        ObjectMapper objectMapper = TestUtil.customObjectMapper();
        describeAndCancelInstanceService =
                new DescribeAndCancelInstanceService(instanceV2Repository, instanceRequestV2Repository,
                                                 icmsConfigurationProperties, objectMapper,
                                                 auditService, instanceDescriptionHelper,
                                                 byocDescribeHelper, sqsMessageRepository,
                                                 cloudHealthRepository, instanceServiceHelper,
                                                 ComputePlatformTestFixtures.nonByocComputePlatformService());
        dummyTime = Instant.now().truncatedTo(ChronoUnit.MILLIS);

        // Default lenient stubs for helpers (not all tests exercise every path)
        Mockito.lenient().doReturn(Optional.of(ZoneInfo.builder()
                .cloudProvider(CloudProvider.OCI)
                .zoneName(DUMMY_ZONE)
                .build())).when(instanceDescriptionHelper).resolveZoneInfo(Mockito.any());
        Mockito.lenient().doReturn(Optional.of(ZoneInfo.builder()
                .cloudProvider(CloudProvider.OCI)
                .zoneName(DUMMY_ZONE)
                .build())).when(byocDescribeHelper).resolveOciZoneInfo(Mockito.any());
        Mockito.lenient().doReturn(false).when(instanceDescriptionHelper).isNonByocBatchExpired(Mockito.any(), Mockito.any());
        Mockito.lenient().doReturn(0).when(byocDescribeHelper).getByocValidationDurationWithoutModel();
        Mockito.lenient().doReturn(0).when(byocDescribeHelper).getByocValidationDurationWithModel();
        Mockito.lenient().doReturn(false).when(byocDescribeHelper).isByocBatchExpired(Mockito.any(), Mockito.any(), anyInt());
    }


    @Test
    void describeInstances_withInstanceIdNotProvided_throwsException() {


        IcmsBadRequestException icmsBadRequestException = assertThrows(IcmsBadRequestException.class,
                () -> describeAndCancelInstanceService.describeInstances(DUMMY_CUSTOMER_ID, null));

        assertEquals("instanceIds must be provided", icmsBadRequestException.getBody().getDetail());
    }

    @Test
    void describeInstances_withDuplicationCheckEnabled_returnsTerminated() {
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                                           SpotInstanceRequestState.CLOSED, dummyTime,
                                           ResourceProvider.OCI);

        InstanceV2Entity instanceEntity2 = CopyUtil.deepCopy(instanceEntity1);

        instanceEntity2.setRequestStatusCode(SpotInstanceStatus.FULFILLED);
        instanceEntity2.setRequestStatusMessage("Instance request fulfilled");
        instanceEntity2.setInstanceStateCode(
                SpotInstanceInternalState.getStateCode(SpotInstanceInternalState.STARTING));
        instanceEntity2.setInstanceStateName(SpotInstanceInternalState.STARTING);
        instanceEntity2.setRequestState(SpotInstanceRequestState.ACTIVE);
        instanceEntity2.setRequestStatusUpdateTime(Instant.now().plusMillis(100000));

        doReturn(List.of(instanceEntity1, instanceEntity2)).when(instanceV2Repository)
                .findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                 Set.of(DUMMY_STARTING_INSTANCE_ID));

        var requestEntity = Optional.of(getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                                    PENDING_FULFILLMENT,
                                                                    DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                    dummyTime,
                                                                    ResourceProvider.OCI));
        when(instanceRequestV2Repository.findRequestByIdAndCustomer(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                          DUMMY_CUSTOMER_ID)).
                thenReturn(requestEntity);

        when(icmsConfigurationProperties.isCheckForDuplicateInstances()).thenReturn(true);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstances(DUMMY_CUSTOMER_ID,
                                                                   List.of(DUMMY_STARTING_INSTANCE_ID));

        verify(instanceV2Repository).findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                                       Set.of(DUMMY_STARTING_INSTANCE_ID));
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                           DUMMY_CUSTOMER_ID);
        assertThat(instanceRequestsResponse.getSpotInstances().size()).isEqualTo(1);
        assertThat(
                instanceRequestsResponse.getSpotInstances().get(0).getState().getName()).isEqualTo(
                SpotInstanceInternalState.TERMINATED.getStateName());
    }

    @Test
    void describeInstances_withDuplicationCheckEnabled_returnLatest() {
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);

        InstanceV2Entity instanceEntity2 = CopyUtil.deepCopy(instanceEntity1);

        instanceEntity2.setRequestStatusCode(SpotInstanceStatus.FULFILLED);
        instanceEntity2.setRequestStatusMessage("Instance request fulfilled");
        instanceEntity2.setInstanceStateCode(
                SpotInstanceInternalState.getStateCode(SpotInstanceInternalState.STARTING));
        instanceEntity2.setInstanceStateName(SpotInstanceInternalState.STARTING);
        instanceEntity2.setRequestState(SpotInstanceRequestState.ACTIVE);
        instanceEntity2.setRequestStatusUpdateTime(Instant.now().plusMillis(100000));

        doReturn(List.of(instanceEntity1, instanceEntity2)).when(instanceV2Repository)
                .findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                 Set.of(DUMMY_STARTING_INSTANCE_ID));

        var requestEntity = Optional.of(getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                                    PENDING_FULFILLMENT,
                                                                    DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                    dummyTime,
                                                                    ResourceProvider.OCI));
        when(instanceRequestV2Repository.findRequestByIdAndCustomer(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                        DUMMY_CUSTOMER_ID)).thenReturn(
                requestEntity);

        when(icmsConfigurationProperties.isCheckForDuplicateInstances()).thenReturn(true);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstances(DUMMY_CUSTOMER_ID,
                                                                   List.of(DUMMY_STARTING_INSTANCE_ID));

        verify(instanceV2Repository).findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                                                                      Set.of(DUMMY_STARTING_INSTANCE_ID));
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                           DUMMY_CUSTOMER_ID);
        assertThat(instanceRequestsResponse.getSpotInstances().size()).isEqualTo(1);
        assertThat(
                instanceRequestsResponse.getSpotInstances().get(0).getState().getName()).isEqualTo(
                SpotInstanceInternalState.STARTING.getStateName());
    }

    @Test
    void describeInstances_withRequestInfoNotPresent_throwsException() {

        doReturn(List.of(getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                                                    SpotInstanceRequestState.CLOSED, dummyTime,
                                                    ResourceProvider.OCI))).when(
                        instanceV2Repository)
                .findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID, Set.of(DUMMY_TERMINATED_INSTANCE_ID));

        doReturn(Optional.empty()).when(instanceRequestV2Repository)
                .findRequestByIdAndCustomer(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                            DUMMY_CUSTOMER_ID);

        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> describeAndCancelInstanceService.describeInstances(
                                                                    DUMMY_CUSTOMER_ID, List.of(DUMMY_TERMINATED_INSTANCE_ID)));

        verify(instanceV2Repository).findInstancesByCustomerAndIds(DUMMY_CUSTOMER_ID,
                Set.of(DUMMY_TERMINATED_INSTANCE_ID));
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                           DUMMY_CUSTOMER_ID);
        assertThat(exception.getBody().getDetail()).startsWith(
                "Failed to get request information");
    }


    @Test
    void describeInstanceRequests_withRequestIds_withDataNotPresent_returnsSuccess() {

        doReturn(new ArrayList<>()).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_1,
                                                                  Set.of(DUMMY_REQUEST_ID),
                                                                  Set.of(SpotInstanceRequestState.ACTIVE.toString()));

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        assertEquals(instanceRequestsResponse.getSpotInstanceRequest().size(), 0);
    }

    @Test
    void describeInstanceRequests_withRequestIds_withoutStateFilter_returnsSuccess() {

        InstanceV2Entity entity1 = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                SpotInstanceRequestState.ACTIVE,
                                                                dummyTime, ResourceProvider.OCI);
        entity1.setCapacityType("RESERVED");

        InstanceV2Entity entity2 =
                getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                                           SpotInstanceRequestState.CLOSED, dummyTime, ResourceProvider.OCI);

        // Providing active and closed requests
        doReturn(Map.of(entity1.getRequestId(), List.of(entity1, entity2))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(Mockito.any(), Mockito.anySet(), Mockito.anyBoolean());

        // Returning
        // 1. one open request without instance ids
        // 2. open request with instance ids
        //    a. 1 active instance
        //    b. 1 closed instance
        // 3. one canceled request
        doReturn(
                List.of(getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                  PENDING_FULFILLMENT,
                                                  DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                                  ResourceProvider.OCI),
                        getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                  PENDING_EVALUATION,
                                                  DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                                                  dummyTime, ResourceProvider.OCI),
                        getDummyInstanceRequestEntity(SpotInstanceRequestState.CANCELED,
                                                  CANCELED_BEFORE_FULFILLMENT,
                                                  DUMMY_CANCELED_REQUEST_ID, dummyTime,
                                                  ResourceProvider.OCI))).when(
                        instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));

        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // Providing request-ids
        // 1. open request without instance ids
        // 2. open request with instance ids
        // 3. canceled request
        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                         DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                                                                         DUMMY_CANCELED_REQUEST_ID),
                                                                  Set.of());

        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(Mockito.any(),
                Mockito.anySet(), Mockito.anyBoolean());
        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();

        // Error log will be present for failed container
        for (SpotInstanceRequest instanceRequest : receivedResponse) {
            if (instanceRequest.getState().equals(SpotInstanceRequestState.CLOSED)) {
                assertEquals(DUMMY_FAILED_CONTAINER_LOG,
                             instanceRequest.getHealthInfo().getErrorLog());
            }
            // Verify capacity type is set in launch specification for active instances
            if (instanceRequest.getState().equals(SpotInstanceRequestState.ACTIVE)) {
                assertNotNull(instanceRequest.getSpotInstanceLaunchSpecification().getCapacityType());
                assertEquals(CapacityType.RESERVED, instanceRequest.getSpotInstanceLaunchSpecification().getCapacityType());
            }
        }

        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.OPEN,
                                            ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.CLOSED,
                                            ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.ACTIVE,
                                            ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.CANCELED,
                                            ResourceProvider.OCI).get(0)));
    }

    @Test
    void describeInstanceRequests_withCanceledAndNoCapacityStateRequestId_withoutStateFilter_returnsSuccess() {

        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.CANCELED,
                                          SCHEDULE_EXPIRED, DUMMY_CANCELED_REQUEST_ID,
                                          dummyTime, ResourceProvider.OCI);

        instanceRequestEntity.setStatusMessage(NO_CAPACITY_USER_VISIBLE_MSG.formatted(
                CloudProvider.OCI.toString(), DUMMY_GPU, DUMMY_NON_BYOC_INSTANCE_TYPE, 0));

        ClientRequestDataModel clientRequestModel =
                ClientRequestDataModel.builder().sub(DUMMY_CUSTOMER_ID).instanceCount(2)
                        .requestId(DUMMY_CANCELED_REQUEST_ID)
                        .spotInstanceRequestAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                        .launchSpecification(new ClientRequestDataModel.LaunchSpecification(
                                DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE, DUMMY_GPU,
                                CloudProvider.OCI.toString(), DUMMY_NON_BYOC_NCA_ID, null,
                                DUMMY_FUNCTION_ID, DUMMY_FUNCTION_VERSION_ID,
                                null, null, null, null, null, null,
                                DUMMY_FUNCTION_NAME, null, DUMMY_NCA_ID_ACCOUNT_NAME)).build();

        instanceRequestEntity.setRequest(GsonCompatMapper.toJson(clientRequestModel));

        doReturn(
                List.of(instanceRequestEntity)).when(
                        instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));

        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_CANCELED_REQUEST_ID),
                                                                  Set.of());
        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID),
                                                                                 Mockito.anySet(),
                                                                                 Mockito.anyBoolean());
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();

        // Assert error log for when request is canceled due to no capacity
        for (SpotInstanceRequest instanceRequest : receivedResponse) {
            if (instanceRequest.getState().equals(SpotInstanceRequestState.CANCELED)) {
                assertEquals(NO_CAPACITY_USER_VISIBLE_MSG.formatted(
                                     CloudProvider.OCI.toString(), DUMMY_GPU, DUMMY_NON_BYOC_INSTANCE_TYPE, 0),
                             instanceRequest.getHealthInfo().getErrorLog());
            }
        }
    }

    @Test
    void describeInstanceRequests_withCanceledAndUnableToFulfilRequest_withoutStateFilter_returnsSuccess() {

        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.CANCELED,
                                          SCHEDULE_EXPIRED, DUMMY_CANCELED_REQUEST_ID,
                                          dummyTime, ResourceProvider.OCI);
        instanceRequestEntity.setStatusMessage(UNABLE_TO_FULFILL_REQUEST_USER_VISIBLE_MSG.formatted(
                CloudProvider.OCI.toString(), DUMMY_GPU,
                DUMMY_NON_BYOC_INSTANCE_TYPE, 30L));
        ClientRequestDataModel clientRequestModel =
                ClientRequestDataModel.builder().sub(DUMMY_CUSTOMER_ID).instanceCount(2)
                        .requestId(DUMMY_CANCELED_REQUEST_ID)
                        .spotInstanceRequestAction(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES)
                        .launchSpecification(new ClientRequestDataModel.LaunchSpecification(
                                DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_CONTAINER_IMAGE, DUMMY_GPU,
                                CloudProvider.OCI.toString(), DUMMY_NON_BYOC_NCA_ID, null,
                                DUMMY_FUNCTION_ID, DUMMY_FUNCTION_VERSION_ID,
                                null, null, null, null, null,
                                null, DUMMY_FUNCTION_NAME, null, DUMMY_NCA_ID_ACCOUNT_NAME)).build();
        instanceRequestEntity.setRequest(GsonCompatMapper.toJson(clientRequestModel));
        mockParseNonByocRequestInfo(DUMMY_CANCELED_REQUEST_ID);

        doReturn(
                List.of(instanceRequestEntity)).when(
                        instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_CANCELED_REQUEST_ID),
                                                                  Set.of());
        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID),
                                                                                 Mockito.anySet(),
                                                                                 Mockito.anyBoolean());
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();

        // Assert error log for when request is canceled due to unable to fulfil request
        for (SpotInstanceRequest instanceRequest : receivedResponse) {
            if (instanceRequest.getState().equals(SpotInstanceRequestState.CANCELED)) {
                assertEquals(UNABLE_TO_FULFILL_REQUEST_USER_VISIBLE_MSG.formatted(
                                     CloudProvider.OCI.toString(), DUMMY_GPU,
                                     DUMMY_NON_BYOC_INSTANCE_TYPE, 30L),
                             instanceRequest.getHealthInfo().getErrorLog());
            }
        }

    }

    @ParameterizedTest
    @ValueSource(strings = {"closed", "active", "canceled", "open"})
    void describeInstanceRequests_withRequestIds_withStateFilter_returnsSuccess(
            String state) {

        InstanceV2Entity entity1 = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                                SpotInstanceRequestState.ACTIVE,
                                                                dummyTime, ResourceProvider.OCI);
        entity1.setCapacityType("RESERVED");
        InstanceV2Entity entity2 =
                getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                                           SpotInstanceRequestState.CLOSED, dummyTime, ResourceProvider.OCI);
        entity2.setCapacityType("SPOT");

        // Providing active and closed requests
        doReturn(Map.of(entity1.getRequestId(), List.of(entity1, entity2))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        // Returning
        // 1. open request without instance ids
        // 2. open request with instance ids
        //    a. 1 active instance
        //    b. 1 closed instance
        // 3. canceled request
        doReturn(List.of(getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                   PENDING_FULFILLMENT,
                                                   DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                                   ResourceProvider.OCI),
                         getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                   PENDING_EVALUATION,
                                                   DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                                                   dummyTime,
                                                   ResourceProvider.OCI),
                         getDummyInstanceRequestEntity(SpotInstanceRequestState.CANCELED,
                                                   CANCELED_BEFORE_FULFILLMENT,
                                                   DUMMY_CANCELED_REQUEST_ID, dummyTime,
                                                   ResourceProvider.OCI))).when(
                        instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // Providing request-ids
        // 1. open request without instance ids
        // 2. open request with instance ids
        // 3. canceled request
        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                         DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                                                                         DUMMY_CANCELED_REQUEST_ID),
                                                                  Set.of(state));

        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID),
                                                                                 Mockito.anySet(),
                                                                                 Mockito.anyBoolean());
        Optional<SpotInstanceRequestState> instanceRequestState =
                SpotInstanceRequestState.toSpotInstanceRequestState(state);

        assertTrue(instanceRequestState.isPresent());
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        assertThat(getDummyInstanceRequestResponse(instanceRequestState.get(),
                                               ResourceProvider.OCI)).isEqualTo(
                instanceRequestsResponse.getSpotInstanceRequest());
    }

    @Test
    void describeInstanceRequests_withoutRequestIds_throwsException() {

        IcmsBadRequestException icmsBadRequestException = assertThrows(IcmsBadRequestException.class,
                () -> describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID, null,
                        Set.of(SpotInstanceRequestState.OPEN.toString())));

        assertEquals("requestIds must be provided", icmsBadRequestException.getBody().getDetail());
    }

    @Test
    void describeInstanceRequests_withRequestIds_withRequestInfoParsingFailed_returnsError() {
        InstanceV2Entity instanceEntity =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime, null);
        doReturn(Map.of(instanceEntity.getRequestId(), List.of(instanceEntity))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(DUMMY_CUSTOMER_ID,
                                                         Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                         false);

        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN, PENDING_FULFILLMENT,
                                          DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime, null);
        instanceRequestEntity.setRequest("dummy_string_value");

        doReturn(List.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                              DUMMY_CUSTOMER_ID);
        doThrow(new IcmsInternalServerException(
                "Failed to get request information - java.lang.IllegalStateException: Expected BEGIN_OBJECT but was STRING at line 1 column 1 path $"))
                .when(instanceServiceHelper).parseRequestInfo(Mockito.any());

        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> describeAndCancelInstanceService.describeInstanceRequests(
                                                                    DUMMY_CUSTOMER_ID,
                                                                    Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                                    Set.of(SpotInstanceRequestState.ACTIVE.toString())));

        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(DUMMY_CUSTOMER_ID,
                                                                                 Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                                                 false);
        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Failed to get request information - java.lang.IllegalStateException: "
                        + "Expected BEGIN_OBJECT but was STRING at line 1 column 1 path $");
    }

    @Test
    void describeInstanceRequests_withRequestIds_withoutStateFilter_withResourceProvider_returnsSuccess() {

        // Providing active and closed requests
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);
        instanceEntity1.setCapacityType("RESERVED");

        InstanceV2Entity instanceEntity2 =
                getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                                           SpotInstanceRequestState.CLOSED, dummyTime,
                                           ResourceProvider.OCI);
        instanceEntity2.setCapacityType("SPOT");

        doReturn(Map.of(instanceEntity1.getRequestId(),
                        List.of(instanceEntity1, instanceEntity2))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        // Returning
        // 1. one open request without instance ids
        // 2. open request with instance ids
        //    a. 1 active instance
        //    b. 1 closed instance
        // 3. one canceled request
        doReturn(
                List.of(getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                  PENDING_FULFILLMENT,
                                                  DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                                  ResourceProvider.OCI),
                        getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                  PENDING_EVALUATION,
                                                  DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                                                  dummyTime, ResourceProvider.OCI),
                        getDummyInstanceRequestEntity(SpotInstanceRequestState.CANCELED,
                                                  CANCELED_BEFORE_FULFILLMENT,
                                                  DUMMY_CANCELED_REQUEST_ID, dummyTime,
                                                  ResourceProvider.OCI))).when(
                        instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));

        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // Providing request-ids
        // 1. open request without instance ids
        // 2. open request with instance ids
        // 3. canceled request
        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                                         DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                                                                         DUMMY_CANCELED_REQUEST_ID),
                                                                  Set.of());

        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID),
                                                                                 Mockito.anySet(),
                                                                                 Mockito.anyBoolean());
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();

        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.OPEN,
                                            ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.CLOSED,
                                            ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.ACTIVE,
                                            ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.CANCELED,
                                            ResourceProvider.OCI).get(0)));
    }

    @Test
    void describeInstanceRequests_withDuplicationCheck_returnUniqueEntries() {

        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.STARTING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);

        InstanceV2Entity instanceEntity2 = CopyUtil.deepCopy(instanceEntity1);
        instanceEntity2.setRequestStatusCode(SpotInstanceStatus.INSTANCE_TERMINATED_BY_USER);
        instanceEntity2.setRequestStatusMessage("Instance request terminated");

        instanceEntity2.setInstanceStateCode(
                SpotInstanceInternalState.getStateCode(SpotInstanceInternalState.TERMINATED));
        instanceEntity2.setInstanceStateName(SpotInstanceInternalState.TERMINATED);
        instanceEntity2.setRequestState(SpotInstanceRequestState.CLOSED);
        instanceEntity2.setRequestStatusUpdateTime(Instant.now().plusMillis(100000));

        doReturn(Map.of(instanceEntity1.getRequestId(),
                        List.of(instanceEntity1, instanceEntity2))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        when(icmsConfigurationProperties.isCheckForDuplicateInstances()).thenReturn(true);

        doReturn(
                List.of(getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                  PENDING_FULFILLMENT,
                                                  DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                                  ResourceProvider.OCI))).when(
                        instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(instanceEntity1.getRequestId()),
                                                                  Set.of());

        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID),
                                                                                 Mockito.anySet(),
                                                                                 Mockito.anyBoolean());
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();

        assertThat(receivedResponse.size()).isEqualTo(1);
        assertThat(receivedResponse.get(0).getInstanceState().getName()).isEqualTo(
                SpotInstanceInternalState.TERMINATED.getStateName());
        verify(icmsConfigurationProperties).isCheckForDuplicateInstances();
    }

    @Test
    void describeInstances_withFetchingInfoForGivenInstances_withResourceProvider_returnsSuccess() {

        var nonByocRequestId = "nonbyoc-request-id";
        var byocRequestId = "byoc-request-id";

        var nonByocInstanceId1 = "nonbyoc-instance-1";
        var byocInstanceId1 = "byoc-instance-1";
        var byocInstanceId2 = "byoc-instance-2";

        var nonByocInstance1 = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                      SpotInstanceRequestState.ACTIVE, dummyTime,
                                                      ResourceProvider.OCI);
        nonByocInstance1.setInstanceId(nonByocInstanceId1);
        nonByocInstance1.setRequestId(nonByocRequestId);
        nonByocInstance1.setCapacityType("SPOT");

        var byocInstance1 = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                       SpotInstanceRequestState.ACTIVE, dummyTime,
                                                       ResourceProvider.BYOC);
        byocInstance1.setInstanceId(byocInstanceId1);
        byocInstance1.setZone("byoc-cluster-id");
        byocInstance1.setRequestId(byocRequestId);
        byocInstance1.setCapacityType("RESERVED");

        var byocInstance2 = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                                       SpotInstanceRequestState.ACTIVE, dummyTime,
                                                       ResourceProvider.BYOC);
        byocInstance2.setInstanceId(byocInstanceId2);
        byocInstance2.setZone("byoc-cluster-id");
        byocInstance2.setRequestId(byocRequestId);

        var nonByocRequest = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                   PENDING_FULFILLMENT, nonByocRequestId, dummyTime,
                                                   ResourceProvider.OCI);
        var byocRequest = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                    PENDING_FULFILLMENT, byocRequestId, dummyTime,
                                                    ResourceProvider.BYOC);

        doReturn(List.of(nonByocInstance1, byocInstance1, byocInstance2)).when(
                        instanceV2Repository)
                .findInstancesByCustomerAndIds(eq(DUMMY_CUSTOMER_ID), Mockito.argThat(
                        list -> list.containsAll(
                                List.of(nonByocInstanceId1, byocInstanceId1,
                                        byocInstanceId2))));

        doReturn(Optional.of(ZoneInfo.builder()
                .cloudProvider(CloudProvider.AWS)
                .zoneName(TestUtil.getDummyClusterEntity().getClusterName())
                .build())).when(byocDescribeHelper).resolveByocZoneInfo(Mockito.any(), Mockito.any());

        doReturn(Optional.of(nonByocRequest)).when(instanceRequestV2Repository)
                .findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID);
        doReturn(Optional.of(byocRequest)).when(instanceRequestV2Repository)
                .findRequestByIdAndCustomer(byocRequestId, DUMMY_CUSTOMER_ID);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstances(DUMMY_CUSTOMER_ID,
                                                                   List.of(nonByocInstanceId1,
                                                                           byocInstanceId1,
                                                                           byocInstanceId2));

        verify(instanceV2Repository).findInstancesByCustomerAndIds(eq(DUMMY_CUSTOMER_ID),
                                                                       Mockito.argThat(
                                                                               list -> list.containsAll(
                                                                                       List.of(nonByocInstanceId1,
                                                                                               byocInstanceId1,
                                                                                               byocInstanceId2))));
        assertThat(instanceRequestsResponse.getSpotInstanceRequest()).isNull();
        assertThat(instanceRequestsResponse.getSpotInstances().size()).isEqualTo(3);
        
        // Verify capacity type is set correctly
        var instances = instanceRequestsResponse.getSpotInstances();
        var nonByocInstanceResult = instances.stream()
                .filter(i -> i.getInstanceId().equals(nonByocInstanceId1))
                .findFirst();
        assertTrue(nonByocInstanceResult.isPresent());
        assertEquals(CapacityType.SPOT, nonByocInstanceResult.get().getCapacityType());
        assertEquals(nonByocRequestId, nonByocInstanceResult.get().getRequestId());
        assertEquals(DUMMY_GPU, nonByocInstanceResult.get().getGpu());
        assertEquals(nonByocInstance1.getRequestStatusUpdateTime(), nonByocInstanceResult.get().getUpdateTime());

        var byocInstance1Result = instances.stream()
                .filter(i -> i.getInstanceId().equals(byocInstanceId1))
                .findFirst();
        assertTrue(byocInstance1Result.isPresent());
        assertEquals(CapacityType.RESERVED, byocInstance1Result.get().getCapacityType());
        assertEquals(byocRequestId, byocInstance1Result.get().getRequestId());
        assertEquals(DUMMY_GPU, byocInstance1Result.get().getGpu());
        assertEquals(byocInstance1.getRequestStatusUpdateTime(), byocInstance1Result.get().getUpdateTime());

        var byocInstance2Result = instances.stream()
                .filter(i -> i.getInstanceId().equals(byocInstanceId2))
                .findFirst();
        assertTrue(byocInstance2Result.isPresent());
        assertEquals(CapacityType.SPOT, byocInstance2Result.get().getCapacityType()); // Default when not set
        assertEquals(byocRequestId, byocInstance2Result.get().getRequestId());
        assertEquals(DUMMY_GPU, byocInstance2Result.get().getGpu());
        assertEquals(byocInstance2.getRequestStatusUpdateTime(), byocInstance2Result.get().getUpdateTime());
        
        verify(instanceRequestV2Repository).findRequestByIdAndCustomer(nonByocRequestId, DUMMY_CUSTOMER_ID);
    }

    @Test
    void describeInstanceRequests_withRequestIds_withResourceProvider_withBYOCAndClusterInfoFetchingFailed_throwsException() {

        // Prepare
        String byocRequestId = "byoc-request-id";

        InstanceV2Entity byocInstance1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.BYOC);
        byocInstance1.setInstanceId("byoc-1");
        byocInstance1.setRequestId(byocRequestId);
        byocInstance1.setZone("byoc-cluster-id");

        InstanceV2Entity byocInstance2 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.BYOC);
        byocInstance2.setInstanceId("byoc-2");
        byocInstance2.setRequestId(byocRequestId);
        byocInstance2.setZone("byoc-cluster-id");

        doReturn(Map.of(byocRequestId, List.of(byocInstance1, byocInstance2))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(DUMMY_CUSTOMER_ID, Set.of(byocRequestId), false);

        doReturn(List.of(getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                   PENDING_FULFILLMENT,
                                                   byocRequestId, dummyTime,
                                                   ResourceProvider.BYOC))).when(
                        instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));

        doThrow(new RuntimeException("dummy-message")).when(byocDescribeHelper)
                .resolveByocZoneInfo(Mockito.any(), Mockito.any());
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // Act
        var exception = assertThrows(IcmsInternalServerException.class, () -> {
            describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                              Set.of(byocRequestId), Set.of());
        });

        // Assert
        assertEquals("Error while framing response for describe instance requests, error: dummy-message",
                     exception.getBody().getDetail());
        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(DUMMY_CUSTOMER_ID,
                                                                                 Set.of(byocRequestId),
                                                                                 false);
    }

    @Test
    void describeInstanceRequests_withRequestIds_withResourceProvider_withBYOCAndClusterInfoNotPresent_returnsEmptyList() {

        // Prepare
        String byocRequestId = "byoc-request-id";

        InstanceV2Entity byocInstance1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.BYOC);
        byocInstance1.setInstanceId("byoc-1");
        byocInstance1.setRequestId(byocRequestId);
        byocInstance1.setZone("byoc-cluster-id");

        InstanceV2Entity byocInstance2 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.BYOC);
        byocInstance2.setInstanceId("byoc-2");
        byocInstance2.setRequestId(byocRequestId);
        byocInstance2.setZone("byoc-cluster-id");

        doReturn(Map.of(byocRequestId, List.of(byocInstance1, byocInstance2))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(DUMMY_CUSTOMER_ID, Set.of(byocRequestId), false);

        doReturn(List.of(getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                                   PENDING_FULFILLMENT,
                                                   byocRequestId, dummyTime,
                                                   ResourceProvider.BYOC))).when(
                        instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.any(), eq(DUMMY_CUSTOMER_ID));

        doReturn(Optional.empty()).when(byocDescribeHelper).resolveByocZoneInfo(Mockito.any(), Mockito.any());
        mockParseByocRequestInfo(byocRequestId);

        // Act
        GetSpotInstanceRequests response =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(byocRequestId), Set.of());

        // Assert
        assertEquals(0, response.getSpotInstanceRequest().size());
        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(DUMMY_CUSTOMER_ID,
                                                                                 Set.of(byocRequestId),
                                                                                 false);
    }

    private void mockParseByocRequestInfo(String requestId) {
        doReturn(getDummyClientRequestDataModel(DUMMY_BYOC_INSTANCE_TYPE, DUMMY_BYOC_NCA_ID,
                requestId, CloudProvider.AZURE.toString())).when(instanceServiceHelper)
                .parseRequestInfo(Mockito.any());
    }

    private void mockParseNonByocRequestInfo(String requestId){
        doReturn(getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                requestId, CloudProvider.OCI.toString())).when(instanceServiceHelper)
                .parseRequestInfo(Mockito.any());
    }

    // Populating acked instances
    @Test
    void describeInstanceRequests_withRequestIds_populatingAckedInstances_returnsSuccess() {

        // 2 acked instances
        doReturn(true).when(icmsConfigurationProperties).isPopulateAcknowledgedInstances();
        doReturn(Set.of(DUMMY_ZONE)).when(cloudHealthRepository).finalAllHealthyZones();
        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID)
                             .build())
                .creationTime(Instant.now())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .acknowledgedInstances(2)
                .build();
        doReturn(List.of(sqsMessageEntity)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // 1 created instances
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);
        instanceEntity1.setCapacityType("RESERVED");

        doReturn(Map.of(instanceEntity1.getRequestId(),
                        List.of(instanceEntity1))).when(instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                          PENDING_FULFILLMENT,
                                          DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                          ResourceProvider.OCI);
        instanceRequestEntity.setCheckBatchwiseInfo(true);

        doReturn(List.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                              DUMMY_CUSTOMER_ID);
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                                  Set.of());

        // Assert
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();
        assertEquals(2, receivedResponse.size());

        validateAckedInstancePopulation(receivedResponse,
                                        Map.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, 1));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.ACTIVE,
                                            ResourceProvider.OCI).get(0)));

        // Verify
        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID),
                                                                                 Mockito.anySet(),
                                                                                 Mockito.anyBoolean());
        verify(icmsConfigurationProperties).isPopulateAcknowledgedInstances();
    }

    @Test
    void describeInstanceRequests_withRequestIds_requestedInstancesCreatedNotPoulateAckedInstances_returnsSuccess() {
        doReturn(true).when(icmsConfigurationProperties).isPopulateAcknowledgedInstances();

        // 2 created instances
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);
        instanceEntity1.setInstanceIps(Set.of("ip1", "ip2"));
        instanceEntity1.setCapacityType("RESERVED");

        InstanceV2Entity instanceEntity2 =
                getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                                           SpotInstanceRequestState.CLOSED, dummyTime,
                                           ResourceProvider.OCI);
        instanceEntity2.setCapacityType("SPOT");

        doReturn(Map.of(instanceEntity1.getRequestId(),
                        List.of(instanceEntity1, instanceEntity2))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID),
                                                         Mockito.anySet(),
                                                         Mockito.anyBoolean());

        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                          PENDING_FULFILLMENT,
                                          DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                          ResourceProvider.OCI);
        instanceRequestEntity.setCheckBatchwiseInfo(true);

        doReturn(List.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                              DUMMY_CUSTOMER_ID);
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                                  Set.of());

        // Assert
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();
        assertEquals(2, receivedResponse.size());

        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.CLOSED,
                                            ResourceProvider.OCI).get(0)));
        SpotInstanceRequest expectedInstanceRequest1 =
                getDummyInstanceRequestResponse(SpotInstanceRequestState.ACTIVE,
                                            ResourceProvider.OCI).get(0);
        expectedInstanceRequest1.setInstanceIps(Set.of("ip1", "ip2"));
        assertTrue(receivedResponse.contains(expectedInstanceRequest1));

        // Verify
        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID),
                                                                                 Mockito.anySet(),
                                                                                 Mockito.anyBoolean());
        verifyNoMoreInteractions(sqsMessageRepository);
    }

    @Test
    void describeAdminInstanceRequests_withRequestIds_withDifferenceCustomer_returnsSuccess() {

        // Returning
        // 1. one open request without instance ids
        // 2. open request with instance ids
        //    a. 1 active instance
        //    b. 1 closed instance
        // 3. one canceled request
        var request1 = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                PENDING_FULFILLMENT,
                DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                ResourceProvider.OCI);
        request1.setCustomer("C1");

        var request2 = getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                PENDING_EVALUATION,
                DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                dummyTime, ResourceProvider.OCI);
        request2.setCustomer("C2");

        var request3 = getDummyInstanceRequestEntity(SpotInstanceRequestState.CANCELED,
                CANCELED_BEFORE_FULFILLMENT,
                DUMMY_CANCELED_REQUEST_ID, dummyTime,
                ResourceProvider.OCI);
        request3.setCustomer("C3");

        doReturn(List.of(request1, request2, request3)).when(instanceRequestV2Repository)
                .findRequestsByIds(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                        DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                        DUMMY_CANCELED_REQUEST_ID));

        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // Mocking instances for provided requests
        InstanceV2Entity entity1 = getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                SpotInstanceRequestState.ACTIVE, dummyTime, ResourceProvider.OCI);
        entity1.setCustomer("C1");
        entity1.setCapacityType("RESERVED");

        InstanceV2Entity entity2 =
                getDummyInstanceEntity(SpotInstanceInternalState.TERMINATED,
                        SpotInstanceRequestState.CLOSED, dummyTime, ResourceProvider.OCI);
        entity2.setCustomer("C1");
        entity2.setCapacityType("SPOT");

        // Providing active and closed requests
        doReturn(Map.of(entity1.getRequestId(), List.of(entity1, entity2))).when(
                        instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(null,
                                                        Set.of(request1.getRequestId(),
                                                             request2.getRequestId(),
                                                             request3.getRequestId()),
                                                         false);

        // Providing request-ids
        // 1. open request without instance ids
        // 2. open request with instance ids
        // 3. canceled request
        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeAdminInstanceRequests(
                        Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                                DUMMY_CANCELED_REQUEST_ID),
                        Set.of());

        verify(instanceV2Repository).findAllInstancesByCustomerAndRequestIds(null,
                                                                                 Set.of(request1.getRequestId(),
                                                                                     request2.getRequestId(),
                                                                                     request3.getRequestId()),
                                                                                 false);
        verify(instanceRequestV2Repository).findRequestsByIds(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID,
                DUMMY_CANCELED_REQUEST_ID));
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();

        // Error log will be present for failed container
        for (SpotInstanceRequest instanceRequest : receivedResponse) {
            if (instanceRequest.getState().equals(SpotInstanceRequestState.CLOSED)) {
                assertEquals(DUMMY_FAILED_CONTAINER_LOG,
                        instanceRequest.getHealthInfo().getErrorLog());
            }
        }

        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.OPEN,
                        ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.CLOSED,
                        ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.ACTIVE,
                        ResourceProvider.OCI).get(0)));
        assertTrue(receivedResponse.contains(
                getDummyInstanceRequestResponse(SpotInstanceRequestState.CANCELED,
                        ResourceProvider.OCI).get(0)));
    }


    private List<SpotInstanceRequest> getDummyInstanceRequestResponse(
            SpotInstanceRequestState state,
            ResourceProvider resourceProvider) {
        HealthInfo healthInfo = null;
        if (state.equals(SpotInstanceRequestState.FAILED)) {
            healthInfo = HealthInfo.builder()
                    .errorLog("dummy_error_log")
                    .build();
        }

        String instanceType = DUMMY_NON_BYOC_INSTANCE_TYPE;
        String backend = CloudProvider.OCI.toString();
        String ncaId = DUMMY_NON_BYOC_NCA_ID;
        if (resourceProvider == ResourceProvider.BYOC) {
            instanceType = DUMMY_BYOC_INSTANCE_TYPE;
            backend = CloudProvider.AZURE.toString();
            ncaId = DUMMY_BYOC_NCA_ID;
        }

        SpotInstanceRequest instanceRequest =
                new SpotInstanceRequest(dummyTime, null,
                                        new SpotInstanceLaunchSpecification(DUMMY_NON_BYOC_INSTANCE_TYPE,
                                                                            DUMMY_CONTAINER_IMAGE,
                                                                            null,
                                                                            DUMMY_GPU, backend,
                                                                            ncaId, null), null,
                                        "", null, null, null, null, healthInfo, "terminate", null, null, null);

        if (state.equals(SpotInstanceRequestState.OPEN)) {
            instanceRequest.setState(SpotInstanceRequestState.OPEN);
            instanceRequest.setSpotInstanceRequestId(
                    DUMMY_OPEN_REQUEST_WITHOUT_HAVING_INSTANCE_ID);
            instanceRequest.setStatus(new SpotInstanceRequestStatus("pending-evaluation",
                                                                        "Instance request status set to pending-evaluation",
                                                                        dummyTime.plusMillis(
                                                                                10000)));
            // Explicitly set capacity type to null for OPEN state
            instanceRequest.getSpotInstanceLaunchSpecification().setCapacityType(null);

        } else if (state.equals(SpotInstanceRequestState.ACTIVE)) {
            instanceRequest.setState(SpotInstanceRequestState.ACTIVE);
            instanceRequest.setSpotInstanceRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
            instanceRequest.setStatus(new SpotInstanceRequestStatus("fulfilled",
                                                                        "Instance request status set to fulfilled",
                                                                        dummyTime.plusMillis(
                                                                                10000)));

            instanceRequest.setLaunchedAvailabilityZone(DUMMY_ZONE);
            instanceRequest.setInstanceId(DUMMY_RUNNING_INSTANCE_ID);
            instanceRequest.setSpotCloudProvider(CloudProvider.OCI);
            instanceRequest.getSpotInstanceLaunchSpecification()
                    .setPlacement(new SpotInstanceLaunchSpecification.Placement(DUMMY_ZONE));
            instanceRequest.getSpotInstanceLaunchSpecification()
                    .setCapacityType(CapacityType.RESERVED);
            SpotInstanceInternalState instanceInternalState = SpotInstanceInternalState.RUNNING;
            instanceRequest.setInstanceState(
                    new SpotInstanceState(
                            SpotInstanceInternalState.getStateCode(instanceInternalState),
                            instanceInternalState.getStateName()));
        } else if (state.equals(SpotInstanceRequestState.CLOSED)) {
            instanceRequest.setState(SpotInstanceRequestState.CLOSED);
            instanceRequest.setSpotInstanceRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
            instanceRequest.setStatus(
                    new SpotInstanceRequestStatus("instance-terminated-by-service",
                                                  "Instance request status set to closed",
                                                  dummyTime.plusMillis(10000)));
            instanceRequest.setHealthInfo(HealthInfo.builder()
                                                      .errorLog(DUMMY_FAILED_CONTAINER_LOG)
                                                      .build());
            instanceRequest.setLaunchedAvailabilityZone(DUMMY_ZONE);
            instanceRequest.setInstanceId(DUMMY_TERMINATED_INSTANCE_ID);
            instanceRequest.setSpotCloudProvider(CloudProvider.OCI);
            instanceRequest.getSpotInstanceLaunchSpecification()
                    .setPlacement(new SpotInstanceLaunchSpecification.Placement(DUMMY_ZONE));
            // Set capacity type to SPOT (default) for CLOSED state since entity doesn't have it set
            instanceRequest.getSpotInstanceLaunchSpecification().setCapacityType(CapacityType.SPOT);

            SpotInstanceInternalState instanceInternalState =
                    SpotInstanceInternalState.TERMINATED;
            instanceRequest.setInstanceState(
                    new SpotInstanceState(
                            SpotInstanceInternalState.getStateCode(instanceInternalState),
                            instanceInternalState.getStateName()));
        } else if (state.equals(SpotInstanceRequestState.CANCELED)) {
            instanceRequest.setState(SpotInstanceRequestState.CANCELED);
            instanceRequest.setSpotInstanceRequestId(DUMMY_CANCELED_REQUEST_ID);
            instanceRequest.setStatus(
                    new SpotInstanceRequestStatus("canceled-before-fulfillment",
                                                  "Instance request status set to canceled-before-fulfillment",
                                                  dummyTime.plusMillis(10000)));
            // Explicitly set capacity type to null for CANCELED state
            instanceRequest.getSpotInstanceLaunchSpecification().setCapacityType(null);
        }
        return List.of(instanceRequest);
    }

    private void validateAckedInstancePopulation(
            List<SpotInstanceRequest> instanceRequests,
            Map<String, Integer> expectedAckedInstances) {
        Map<String, Integer> populatedAckedInstances = new HashMap<>();
        for (SpotInstanceRequest instanceRequest : instanceRequests) {
            if (instanceRequest.getInstanceId() == null &&
                    instanceRequest.getState().equals(SpotInstanceRequestState.OPEN)
                    && instanceRequest.getStatus().getCode()
                    .equals(SpotRequestStatusCode.PENDING_FULFILLMENT.toString())) {
                updateInstanceCountInMap(populatedAckedInstances,
                                         instanceRequest.getSpotInstanceRequestId());
            }
        }
    }

    private void updateInstanceCountInMap(
            Map<String, Integer> populatedAckedInstances,
            String requestId) {
        Integer ackedInstances = populatedAckedInstances.get(requestId);
        if (ackedInstances == null) {
            populatedAckedInstances.put(requestId, 1);
        } else {
            populatedAckedInstances.put(requestId, ++ackedInstances);
        }
    }

    // Tests for ACTIVE + FULFILLED state support in acknowledged instance population
    @Test
    void describeInstanceRequests_withActiveRequestAndFeatureFlagEnabled_populatesAckedInstances() {
        // Setup: Request is ACTIVE + FULFILLED (after state transition)
        // Expected: Should still populate acknowledged instances
        
        // 2 acked instances in SQS
        doReturn(true).when(icmsConfigurationProperties).isPopulateAcknowledgedInstances();
        doReturn(true).when(icmsConfigurationProperties).isRequestStateTransitionToActiveEnabled();
        doReturn(Set.of(DUMMY_ZONE)).when(cloudHealthRepository).finalAllHealthyZones();

        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID)
                             .build())
                .creationTime(Instant.now())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .acknowledgedInstances(2)
                .build();
        doReturn(List.of(sqsMessageEntity)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // 1 created instance
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);

        doReturn(Map.of(instanceEntity1.getRequestId(),
                        List.of(instanceEntity1))).when(instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        // Request is ACTIVE + FULFILLED (after state transition)
        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                          SpotRequestStatusCode.FULFILLED,
                                          DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                          ResourceProvider.OCI);
        instanceRequestEntity.setCheckBatchwiseInfo(true);

        doReturn(List.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                              DUMMY_CUSTOMER_ID);
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                                  Set.of());

        // Assert: Should have 1 acked instance + 1 actual instance = 2 total
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();
        assertEquals(2, receivedResponse.size());

        validateAckedInstancePopulation(receivedResponse,
                                        Map.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, 1));

        // Verify feature flag was checked
        verify(icmsConfigurationProperties).isRequestStateTransitionToActiveEnabled();
    }

    @Test
    void describeInstanceRequests_withActiveRequestAndFeatureFlagDisabled_doesNotPopulateAckedInstances() {
        // Setup: Request is ACTIVE but feature flag is disabled
        // Expected: Should NOT populate acknowledged instances
        
        doReturn(true).when(icmsConfigurationProperties).isPopulateAcknowledgedInstances();
        doReturn(false).when(icmsConfigurationProperties).isRequestStateTransitionToActiveEnabled();

        // 1 created instance
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);

        doReturn(Map.of(instanceEntity1.getRequestId(),
                        List.of(instanceEntity1))).when(instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        // Request is ACTIVE + FULFILLED but feature flag is OFF
        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                          SpotRequestStatusCode.FULFILLED,
                                          DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                          ResourceProvider.OCI);
        instanceRequestEntity.setCheckBatchwiseInfo(true);

        doReturn(List.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                              DUMMY_CUSTOMER_ID);
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                                  Set.of());

        // Assert: Should only have 1 actual instance (no acked instances)
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();
        assertEquals(1, receivedResponse.size());
        
        // Should not have tried to fetch SQS messages since request not considered
        verifyNoMoreInteractions(sqsMessageRepository);
    }

    @Test
    void describeInstanceRequests_withActiveStateFilter_populatesAckedInstancesForActiveRequests() {
        // Setup: State filter is ["active"], request is ACTIVE + FULFILLED
        // Expected: Should populate acknowledged instances for ACTIVE requests

        doReturn(true).when(icmsConfigurationProperties).isPopulateAcknowledgedInstances();
        doReturn(true).when(icmsConfigurationProperties).isRequestStateTransitionToActiveEnabled();
        doReturn(Set.of(DUMMY_ZONE)).when(cloudHealthRepository).finalAllHealthyZones();

        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID)
                             .build())
                .creationTime(Instant.now())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .acknowledgedInstances(3)
                .build();
        doReturn(List.of(sqsMessageEntity)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // 1 created instance
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);

        doReturn(Map.of(instanceEntity1.getRequestId(),
                        List.of(instanceEntity1))).when(instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        // Request is ACTIVE + FULFILLED
        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                          SpotRequestStatusCode.FULFILLED,
                                          DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                          ResourceProvider.OCI);
        instanceRequestEntity.setCheckBatchwiseInfo(true);

        doReturn(List.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                              DUMMY_CUSTOMER_ID);
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // Call with "active" state filter
        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                                  Set.of("active"));

        // Assert: Should have 2 acked instances + 1 actual instance = 3 total
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();
        assertEquals(3, receivedResponse.size());
    }

    @Test
    void describeInstanceRequests_withOpenStateFilter_showsActiveRequestsForBackwardCompatibility() {
        // Setup: State filter is ["open"], request is ACTIVE + FULFILLED with acked instances
        // Expected: ACTIVE request IS shown for backward compatibility (users with "open" filter see ACTIVE requests)

        doReturn(true).when(icmsConfigurationProperties).isPopulateAcknowledgedInstances();
        doReturn(true).when(icmsConfigurationProperties).isRequestStateTransitionToActiveEnabled();
        doReturn(Set.of(DUMMY_ZONE)).when(cloudHealthRepository).finalAllHealthyZones();

        // 2 acked instances
        SqsMessageEntity sqsMessageEntity = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                             .messageBatchId(DUMMY_MESSAGE_BATCH_ID)
                             .build())
                .creationTime(Instant.now())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .acknowledgedInstances(2)
                .build();
        doReturn(List.of(sqsMessageEntity)).when(sqsMessageRepository)
                .findByRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // 1 created instance
        InstanceV2Entity instanceEntity1 =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);

        doReturn(Map.of(instanceEntity1.getRequestId(),
                        List.of(instanceEntity1))).when(instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        // Request is ACTIVE + FULFILLED
        InstanceRequestV2Entity instanceRequestEntity =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                          SpotRequestStatusCode.FULFILLED,
                                          DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID, dummyTime,
                                          ResourceProvider.OCI);
        instanceRequestEntity.setCheckBatchwiseInfo(true);

        doReturn(List.of(instanceRequestEntity)).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                              DUMMY_CUSTOMER_ID);
        mockParseNonByocRequestInfo(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);

        // Call with "open" state filter (but request is ACTIVE)
        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID),
                                                                  Set.of("open"));

        // Assert: Should show 1 acked instance (backward compatibility)
        // Users with "open" filter see ACTIVE requests too
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();
        assertEquals(1, receivedResponse.size());
    }

    @Test
    void describeInstanceRequests_withOpenAndActiveInStateFilter_showsBothStates() {
        // Setup: State filter is ["open", "active"], have both OPEN and ACTIVE requests
        // Expected: Should show both requests with their respective acked instances
        
        String openRequestId = "open-request-id";
        String activeRequestId = "active-request-id";
        
        doReturn(true).when(icmsConfigurationProperties).isPopulateAcknowledgedInstances();
        doReturn(true).when(icmsConfigurationProperties).isRequestStateTransitionToActiveEnabled();
        doReturn(Set.of(DUMMY_ZONE)).when(cloudHealthRepository).finalAllHealthyZones();

        // SQS messages for both requests
        SqsMessageEntity sqsOpenRequest = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .requestId(openRequestId)
                             .messageBatchId("batch-1")
                             .build())
                .creationTime(Instant.now())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .acknowledgedInstances(2)
                .build();
        
        SqsMessageEntity sqsActiveRequest = SqsMessageEntity.builder()
                .key(SqsMessageKey.builder()
                             .requestId(activeRequestId)
                             .messageBatchId("batch-2")
                             .build())
                .creationTime(Instant.now())
                .zone(DUMMY_ZONE)
                .status(PENDING_FULFILLMENT)
                .acknowledgedInstances(2)
                .build();
        
        doReturn(List.of(sqsOpenRequest)).when(sqsMessageRepository)
                .findByRequestId(openRequestId);
        doReturn(List.of(sqsActiveRequest)).when(sqsMessageRepository)
                .findByRequestId(activeRequestId);

        // 1 instance for active request
        InstanceV2Entity activeInstance =
                getDummyInstanceEntity(SpotInstanceInternalState.RUNNING,
                                           SpotInstanceRequestState.ACTIVE, dummyTime,
                                           ResourceProvider.OCI);
        activeInstance.setRequestId(activeRequestId);
        activeInstance.setInstanceId("active-instance-1");

        doReturn(Map.of(activeRequestId, List.of(activeInstance))).when(instanceV2Repository)
                .findAllInstancesByCustomerAndRequestIds(eq(DUMMY_CUSTOMER_ID), Mockito.anySet(), Mockito.anyBoolean());

        // OPEN request with PENDING_FULFILLMENT
        InstanceRequestV2Entity openRequest =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.OPEN,
                                          PENDING_FULFILLMENT,
                                          openRequestId, dummyTime,
                                          ResourceProvider.OCI);
        openRequest.setCheckBatchwiseInfo(true);
        
        // ACTIVE request with FULFILLED
        InstanceRequestV2Entity activeRequest =
                getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                          SpotRequestStatusCode.FULFILLED,
                                          activeRequestId, dummyTime,
                                          ResourceProvider.OCI);
        activeRequest.setCheckBatchwiseInfo(true);

        doReturn(List.of(openRequest, activeRequest)).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(openRequestId, activeRequestId),
                                              DUMMY_CUSTOMER_ID);
        doReturn(getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                openRequestId, CloudProvider.OCI.toString())).when(instanceServiceHelper)
                .parseRequestInfo(openRequest.getRequest());
        doReturn(getDummyClientRequestDataModel(DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID,
                activeRequestId, CloudProvider.OCI.toString())).when(instanceServiceHelper)
                .parseRequestInfo(activeRequest.getRequest());

        // Call with both "open" and "active" state filters
        GetSpotInstanceRequests instanceRequestsResponse =
                describeAndCancelInstanceService.describeInstanceRequests(DUMMY_CUSTOMER_ID,
                                                                  Set.of(openRequestId, activeRequestId),
                                                                  Set.of("open", "active"));

        // Assert: Should show entries from both requests
        // OPEN: 2 acked instances
        // ACTIVE: 1 acked + 1 actual = 2 instances
        // Total: 4 entries
        assertThat(instanceRequestsResponse.getSpotInstances()).isNull();
        List<SpotInstanceRequest> receivedResponse =
                instanceRequestsResponse.getSpotInstanceRequest();
        assertEquals(4, receivedResponse.size());
    }

    // Tests for populateAcknowledgedInstances in describeInstancesByDeploymentId

    // ------------------------------------------------------------------
    // NotFound + terminated-placeholder behavior on the deployment GET path,
    // and the expiredAckedInstances=false kill switch.
    // ------------------------------------------------------------------

    // ------------------------------------------------------------------
    // Additional corner-case coverage for populateAcknowledgedInstances:
    //   - master config gate (populate-acknowledged-instances=false)
    //   - per-request gate (checkBatchwiseInfo=false)
    //   - user-cancelled request (CANCELED but not SCHEDULE_EXPIRED)
    //   - mixed healthy + expired ACK batches
    //   - SQS message with status != PENDING_FULFILLMENT
    //   - SQS message with null acknowledgedInstances
    //   - BYOC ACK path
    // ------------------------------------------------------------------

    // ------------------------------------------------------------------
    // includeTerminated orthogonality:
    //   - filters REAL terminated DB rows only
    //   - does NOT affect synthetic terminated placeholders
    // (See class-level javadoc on describeInstancesByDeploymentId.)
    // ------------------------------------------------------------------

    // ------------------------------------------------------------------
    // Per-batch TERMINATED placeholders (cannot-fulfill / schedule-expired)
    // ------------------------------------------------------------------

    private void mockParseRequestWithInstanceCount(String requestId, int instanceCount) {
        ClientRequestDataModel base = getDummyClientRequestDataModel(
                DUMMY_NON_BYOC_INSTANCE_TYPE, DUMMY_NON_BYOC_NCA_ID, requestId, CloudProvider.OCI.toString());
        ClientRequestDataModel withCount = ClientRequestDataModel.builder()
                .instanceCount(instanceCount)
                .sub(base.getSub())
                .requestId(base.getRequestId())
                .spotInstanceRequestAction(base.getSpotInstanceRequestAction())
                .launchSpecification(base.getLaunchSpecification())
                .build();
        doReturn(withCount).when(instanceServiceHelper).parseRequestInfo(Mockito.any());
    }
}
