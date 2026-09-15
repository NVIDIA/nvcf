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

import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.PENDING_FULFILLMENT;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_VERSION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
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
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class CancelInstanceServiceTest {
    
    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private AppAuditService auditService;
    
    @Mock
    private InstanceServiceHelper instanceServiceHelper;

    private Instant dummyTime;

    private CancelInstanceService cancelInstanceRequests;

    @BeforeEach
    void init() {
        ObjectMapper objectMapper = TestUtil.customObjectMapper();
        cancelInstanceRequests =
                new CancelInstanceService(instanceRequestV2Repository, telemetryEventClient, auditService,
                        instanceServiceHelper);
        dummyTime = Instant.now().truncatedTo(ChronoUnit.MILLIS);
    }

    @Test
    void cancelInstanceRequests_withValidParams_returnsSuccess() {
        List<InstanceRequestV2Entity> instanceRequestEntityList =
                getDummyInstanceRequestEntityList(SpotInstanceRequestState.OPEN);
        Map<String, Object> auditProps = new HashMap<>();
        doReturn(instanceRequestEntityList).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);
        doNothing().when(instanceRequestV2Repository).updateRequests(Mockito.anyList());

        doReturn(ClientRequestDataModel.LaunchSpecification.builder()
                         .versionId(DUMMY_FUNCTION_VERSION_ID)
                         .functionId(DUMMY_FUNCTION_ID)
                         .ncaId(DUMMY_BYOC_NCA_ID)
                         .instanceType(DUMMY_BYOC_INSTANCE_TYPE)
                         .deploymentId(UUID.randomUUID())
                         .gpuSpecificationId(UUID.randomUUID())
                         .build()).when(instanceServiceHelper)
                .getLaunchSpecificationForTelemetry(Mockito.any());

        cancelInstanceRequests.cancelInstanceRequests(DUMMY_CUSTOMER_1,
                                                              Set.of(DUMMY_REQUEST_ID),
                                                              auditProps);

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID),
                                                                     DUMMY_CUSTOMER_1);
        verify(instanceRequestV2Repository).updateRequests(Mockito.anyList());
    }

    @Test
    void cancelInstanceRequests_withValidParamsAndUpdateDBFailed_throwsInternalServerException() {
        List<InstanceRequestV2Entity> instanceRequestEntityList =
                getDummyInstanceRequestEntityList(SpotInstanceRequestState.OPEN);

        doReturn(instanceRequestEntityList).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);

        InstanceRequestV2Entity instanceRequestEntity = instanceRequestEntityList.get(0);
        Map<String, Object> auditProps = new HashMap<>();

        doThrow(new IcmsInternalServerException(
                String.format("Failed to update state of requestIds %s",
                        Set.of(DUMMY_REQUEST_ID)))).when(instanceRequestV2Repository)
                .updateRequests(List.of(instanceRequestEntity));

        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                () -> cancelInstanceRequests.cancelInstanceRequests(
                        DUMMY_CUSTOMER_1,
                        Set.of(DUMMY_REQUEST_ID),
                        auditProps));

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);
        verify(instanceRequestV2Repository).updateRequests(Mockito.anyList());

        assertEquals(exception.getBody().getDetail(),
                String.format("Failed to update state of requestIds %s",
                        Set.of(DUMMY_REQUEST_ID)));
    }

    @Test
    void cancelInstanceRequests_withInvalidParams_throwsBadRequestException() {
        List<InstanceRequestV2Entity> instanceRequestEntityList = new ArrayList<>();
        doReturn(instanceRequestEntityList).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);

        IcmsNotFoundException icmsNotFoundException = assertThrows(IcmsNotFoundException.class,
                () -> cancelInstanceRequests.cancelInstanceRequests(
                        DUMMY_CUSTOMER_1,
                        Set.of(DUMMY_REQUEST_ID),
                        new HashMap<>()));

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);
        assertEquals(icmsNotFoundException.getBody().getDetail(),
                String.format("Invalid requestIds : [%s]", DUMMY_REQUEST_ID));
    }

    @Test
    void validateCancelInstanceRequestIds_withInvalidRequestIds_throwsBadRequestException() {
        List<InstanceRequestV2Entity> instanceRequestEntityList =
                getDummyInstanceRequestEntityList(SpotInstanceRequestState.OPEN);

        doReturn(instanceRequestEntityList).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Mockito.argThat(
                                list -> list.containsAll(
                                        List.of(DUMMY_REQUEST_ID, DUMMY_REQUEST_ID + "2"))),
                        eq(DUMMY_CUSTOMER_1));

        IcmsNotFoundException icmsNotFoundException = assertThrows(IcmsNotFoundException.class,
                () -> cancelInstanceRequests.validateCancelInstanceRequestIds(
                        DUMMY_CUSTOMER_1,
                        Set.of(DUMMY_REQUEST_ID,
                                DUMMY_REQUEST_ID
                                        + "2")));

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Mockito.argThat(
                        list -> list.containsAll(
                                List.of(DUMMY_REQUEST_ID,
                                        DUMMY_REQUEST_ID
                                                + "2"))),
                eq(DUMMY_CUSTOMER_1));
        assertEquals(icmsNotFoundException.getBody().getDetail(),
                String.format("Invalid requestIds : [%s]", DUMMY_REQUEST_ID + "2"));
    }

    @Test
    void validateCancelInstanceRequestIds_withRequestStateNotOpen_throwsSisConflictException() {
        List<InstanceRequestV2Entity> instanceRequestEntityList =
                getDummyInstanceRequestEntityList(SpotInstanceRequestState.ACTIVE);

        doReturn(instanceRequestEntityList).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);

        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> cancelInstanceRequests.validateCancelInstanceRequestIds(
                        DUMMY_CUSTOMER_1,
                        Set.of(DUMMY_REQUEST_ID)));

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);
        assertEquals(exception.getBody().getDetail(), String.format(
                "Cancellation failed for following requestIds because the request state is not " +
                        "'open' and request status is not 'pending-evaluation' - %s",
                Set.of(DUMMY_REQUEST_ID)));
    }

    @Test
    void validateCancelInstanceRequestIds_withInvalidRequestStatusAsPendingFulfillment_throwsSisConflictException() {
        InstanceRequestV2Entity instanceRequestEntity1 =
                InstanceRequestV2Entity.builder().customer(DUMMY_CUSTOMER_1).requestId(DUMMY_REQUEST_ID)
                        .action(SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS)
                        .state(SpotInstanceRequestState.OPEN)
                        .statusCode(PENDING_FULFILLMENT.toString())
                        .statusMessage("dummy_message").statusUpdateTime(Instant.now()).build();
        List<InstanceRequestV2Entity> instanceRequestEntityList = new ArrayList<>();
        instanceRequestEntityList.add(instanceRequestEntity1);

        doReturn(instanceRequestEntityList).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);

        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> cancelInstanceRequests.validateCancelInstanceRequestIds(
                        DUMMY_CUSTOMER_1,
                        Set.of(DUMMY_REQUEST_ID)));

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);
        assertEquals(exception.getBody().getDetail(), String.format(
                "Cancellation failed for following requestIds because the request state is not " +
                        "'open' and request status is not 'pending-evaluation' - %s",
                Set.of(DUMMY_REQUEST_ID)));
    }

    @Test
    void validateCancelInstanceRequestIds_withInvalidRequestStatusAsFulfilled_throwsSisConflictException() {
        InstanceRequestV2Entity instanceRequestEntity =
                InstanceRequestV2Entity.builder().customer(DUMMY_CUSTOMER_1).requestId(DUMMY_REQUEST_ID)
                        .action(SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS)
                        .state(SpotInstanceRequestState.OPEN)
                        .statusCode(SpotRequestStatusCode.FULFILLED.toString())
                        .statusMessage("dummy_message").statusUpdateTime(Instant.now()).build();
        List<InstanceRequestV2Entity> instanceRequestEntityList = new ArrayList<>();
        instanceRequestEntityList.add(instanceRequestEntity);

        doReturn(instanceRequestEntityList).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);

        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> cancelInstanceRequests.validateCancelInstanceRequestIds(
                        DUMMY_CUSTOMER_1,
                        Set.of(DUMMY_REQUEST_ID)));

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);
        assertEquals(exception.getBody().getDetail(), String.format(
                "Cancellation failed for following requestIds because the request state is not " +
                        "'open' and request status is not 'pending-evaluation' - %s",
                Set.of(DUMMY_REQUEST_ID)));
    }

    @Test
    void validateCancelInstanceRequestIds_withActiveStateAndFulfilledStatus_throwsSisConflictException() {
        // This test covers the case when feature flag is enabled and request transitions to ACTIVE + FULFILLED
        InstanceRequestV2Entity instanceRequestEntity =
                InstanceRequestV2Entity.builder().customer(DUMMY_CUSTOMER_1).requestId(DUMMY_REQUEST_ID)
                        .action(SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS)
                        .state(SpotInstanceRequestState.ACTIVE)
                        .statusCode(SpotRequestStatusCode.FULFILLED.toString())
                        .statusMessage("dummy_message").statusUpdateTime(Instant.now()).build();
        List<InstanceRequestV2Entity> instanceRequestEntityList = new ArrayList<>();
        instanceRequestEntityList.add(instanceRequestEntity);

        doReturn(instanceRequestEntityList).when(instanceRequestV2Repository)
                .findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);

        IcmsConflictException exception = assertThrows(IcmsConflictException.class,
                () -> cancelInstanceRequests.validateCancelInstanceRequestIds(
                        DUMMY_CUSTOMER_1,
                        Set.of(DUMMY_REQUEST_ID)));

        verify(instanceRequestV2Repository).findRequestsByIdsAndCustomer(Set.of(DUMMY_REQUEST_ID), DUMMY_CUSTOMER_1);
        assertEquals(exception.getBody().getDetail(), String.format(
                "Cancellation failed for following requestIds because the request state is not " +
                        "'open' and request status is not 'pending-evaluation' - %s",
                Set.of(DUMMY_REQUEST_ID)));
    }

    private List<InstanceRequestV2Entity> getDummyInstanceRequestEntityList(SpotInstanceRequestState state) {
        InstanceRequestV2Entity instanceRequestEntity1 =
                InstanceRequestV2Entity.builder().customer(DUMMY_CUSTOMER_1)
                        .createTimeuuid(TimeUtils.getUuidFromTimeStamp(TimeUtils.getCurrentDate()))
                        .requestId(DUMMY_REQUEST_ID)
                        .action(SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS)
                        .state(state)
                        .statusCode(SpotRequestStatusCode.PENDING_EVALUATION.toString())
                        .statusMessage("dummy_message").statusUpdateTime(Instant.now()).build();
        List<InstanceRequestV2Entity> instanceRequestEntityList = new ArrayList<>();
        instanceRequestEntityList.add(instanceRequestEntity1);
        return instanceRequestEntityList;
    }
}
