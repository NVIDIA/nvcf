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

import static com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode.FULFILLED;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.ResourceProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import com.nvidia.icms.service.ExpiredInstanceTerminateService;
import com.nvidia.icms.service.TerminateInstanceService;
import com.nvidia.icms.util.TestUtil;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class TerminateInstanceEventServiceTest {

    @Mock
    private InstanceRequestV2Repository instanceRequestV2Repository;

    private TerminateInstanceEventService terminateInstanceEventService;

    @Mock
    IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    InstanceV2Repository instanceV2Repository;

    @Mock
    ExpiredInstanceTerminateService expiredInstanceTerminateService;

    @Mock
    TerminateInstanceService terminateInstanceService;

    @BeforeEach
    void init() {
        this.terminateInstanceEventService =
                new TerminateInstanceEventService(icmsConfigurationProperties,
                                                  instanceRequestV2Repository,
                                                  instanceV2Repository, terminateInstanceService,
                                                  expiredInstanceTerminateService);
    }

    @Test
    void execute_featureDisabled_skip() {
        // Mock
        doReturn(false).when(icmsConfigurationProperties).isTerminateExpiredInstancesEnabled();

        //Act
        terminateInstanceEventService.execute();

        // Assert
        verifyNoInteractions(terminateInstanceService);
        verifyNoInteractions(instanceRequestV2Repository);
        verifyNoInteractions(instanceV2Repository);
    }

    @Test
    void execute_validInputWithRunningInstances_Success() {
        // Mock
        doReturn(true).when(icmsConfigurationProperties).isTerminateExpiredInstancesEnabled();
        doReturn(5).when(icmsConfigurationProperties).getTerminateExpiredRequestFromPastMonths();
        doReturn(90).when(icmsConfigurationProperties).getInstanceLifetimeValidityInDays();

        InstanceRequestV2Entity instanceRequestEntity =
                TestUtil.getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                                   FULFILLED,
                                                   DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                   Instant.now().minus(92, ChronoUnit.DAYS),
                                                   ResourceProvider.BYOC);
        InstanceRequestV2Entity instanceRequestEntity2 =
                TestUtil.getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                                   FULFILLED,
                                                   DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID + "_2",
                                                   Instant.now().minus(92, ChronoUnit.DAYS),
                                                   ResourceProvider.BYOC);
        doReturn(List.of(instanceRequestEntity, instanceRequestEntity2)).when(instanceRequestV2Repository)
                .findRequestsInLastMonths(eq(5));

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        instanceEntity.setInstanceUpdateTime(Instant.now().minus(92, ChronoUnit.DAYS));

        InstanceV2Entity instanceEntity2 = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity2.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID + "_2");
        instanceEntity2.setInstanceUpdateTime(Instant.now().minus(92, ChronoUnit.DAYS));

        List<InstanceV2Entity> instanceEntities =
                new ArrayList<>(List.of(instanceEntity, instanceEntity2));
        doReturn(instanceEntities)
                .when(instanceV2Repository).findAllByCustomer(DUMMY_CUSTOMER_ID);

        //Act
        terminateInstanceEventService.execute();

        // Assert
        verify(terminateInstanceService, times(2))
                .updateRequestStateToClosedFromAsyncTerminateTask(any());
        verify(instanceRequestV2Repository).findRequestsInLastMonths(any());
        verify(expiredInstanceTerminateService, times(2))
                .terminateExpiredInstances(any());
    }

    @Test
    void execute_validInputWithAlreadyTerminatedInstances_Success() {
        // Mock
        doReturn(true).when(icmsConfigurationProperties).isTerminateExpiredInstancesEnabled();
        doReturn(5).when(icmsConfigurationProperties).getTerminateExpiredRequestFromPastMonths();
        doReturn(90).when(icmsConfigurationProperties).getInstanceLifetimeValidityInDays();

        InstanceRequestV2Entity instanceRequestEntity =
                TestUtil.getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                                   FULFILLED,
                                                   DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID,
                                                   Instant.now().minus(90, ChronoUnit.DAYS),
                                                   ResourceProvider.BYOC);
        InstanceRequestV2Entity instanceRequestEntity2 =
                TestUtil.getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                                   FULFILLED,
                                                   DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID + "_2",
                                                   Instant.now().minus(90, ChronoUnit.DAYS),
                                                   ResourceProvider.BYOC);
        doReturn(List.of(instanceRequestEntity, instanceRequestEntity2)).when(instanceRequestV2Repository)
                .findRequestsInLastMonths(eq(5));

        InstanceV2Entity instanceEntity = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID);
        instanceEntity.setInstanceStateCode(SpotInstanceInternalState.getStateCode
                (SpotInstanceInternalState.TERMINATED));
        instanceEntity.setInstanceUpdateTime(Instant.now().minus(90, ChronoUnit.DAYS));

        InstanceV2Entity instanceEntity2 = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity2.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID + "_2");
        instanceEntity2.setInstanceStateCode(SpotInstanceInternalState.getStateCode
                (SpotInstanceInternalState.TERMINATED));
        instanceEntity2.setInstanceUpdateTime(Instant.now().minus(90, ChronoUnit.DAYS));

        List<InstanceV2Entity> instanceEntities =
                new ArrayList<>(List.of(instanceEntity, instanceEntity2));
        doReturn(instanceEntities)
                .when(instanceV2Repository).findAllByCustomer(DUMMY_CUSTOMER_ID);

        //Act
        terminateInstanceEventService.execute();

        // Assert
        verifyNoInteractions(expiredInstanceTerminateService);
        verify(instanceRequestV2Repository).findRequestsInLastMonths(any());
        verify(terminateInstanceService, times(2))
                .updateRequestStateToClosedFromAsyncTerminateTask(any());
    }

    @Test
    void execute_withValidRunningInstance_shouldNotCloseRequestId_Success() {
        // Mock
        doReturn(true).when(icmsConfigurationProperties).isTerminateExpiredInstancesEnabled();
        doReturn(5).when(icmsConfigurationProperties).getTerminateExpiredRequestFromPastMonths();
        doReturn(90).when(icmsConfigurationProperties).getInstanceLifetimeValidityInDays();

        InstanceRequestV2Entity instanceRequestEntity1 =
                TestUtil.getDummyInstanceRequestEntity(SpotInstanceRequestState.ACTIVE,
                                                   FULFILLED,
                                                   DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID + "_1",
                                                   Instant.now().minus(92, ChronoUnit.DAYS),
                                                   ResourceProvider.BYOC);
        doReturn(List.of(instanceRequestEntity1)).when(instanceRequestV2Repository)
                .findRequestsInLastMonths(eq(5));

        InstanceV2Entity instanceEntity1 = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity1.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID + "_1");
        instanceEntity1.setInstanceUpdateTime(Instant.now().minus(87, ChronoUnit.DAYS));

        InstanceV2Entity instanceEntity2 = TestUtil.getInstanceEntityForRunningInstance();
        instanceEntity2.setRequestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID + "_1");
        instanceEntity2.setInstanceUpdateTime(Instant.now().minus(92, ChronoUnit.DAYS));

        List<InstanceV2Entity> instanceEntities =
                new ArrayList<>(List.of(instanceEntity1, instanceEntity2));
        doReturn(instanceEntities)
                .when(instanceV2Repository).findAllByCustomer(DUMMY_CUSTOMER_ID);

        //Act
        terminateInstanceEventService.execute();

        // Assert
        verify(instanceRequestV2Repository).findRequestsInLastMonths(any());
        verifyNoInteractions(terminateInstanceService);
        verify(expiredInstanceTerminateService, times(1))
                .terminateExpiredInstances(any());
    }
}