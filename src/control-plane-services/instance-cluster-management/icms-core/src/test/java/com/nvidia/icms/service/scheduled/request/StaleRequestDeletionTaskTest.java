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

import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.util.TimeUtils;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.outbound.cassandra.request.entity.InstanceRequestV2Entity;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;

@ExtendWith(MockitoExtension.class)
class StaleRequestDeletionTaskTest extends IntegrationTest {

    @Autowired
    StaleRequestDeletionTask staleRequestDeletionTask;

    @Autowired
    InstanceRequestV2Repository instanceRequestV2Repository;

    @Autowired
    InstanceV2Repository instanceV2Repository;

    @Test
    void execute_withValidInputs_returnsSuccess() {

        // Prepare
        insertEntriesInDb();

        // Act
        staleRequestDeletionTask.execute();

        // Assert
        Optional<InstanceRequestV2Entity> response1 =
                instanceRequestV2Repository.findRequestByIdAndCustomer("r3", DUMMY_CUSTOMER_ID);
        assertTrue(response1.isEmpty());
        List<InstanceV2Entity> response = instanceV2Repository.findInstancesByRequestId("r3");
        assertTrue(response.isEmpty());

        Optional<InstanceRequestV2Entity> response2 =
                instanceRequestV2Repository.findRequestByIdAndCustomer("r2", DUMMY_CUSTOMER_ID);
        assertFalse(response2.isEmpty());
        InstanceRequestV2Entity instanceRequestEntity = response2.get();
        assertEquals(SpotInstanceRequestState.CLOSED, instanceRequestEntity.getState());

        // If request is open and doesn't have any instances then it should still be open
        Optional<InstanceRequestV2Entity> response3 =
                instanceRequestV2Repository.findRequestByIdAndCustomer("r4", DUMMY_CUSTOMER_ID);
        assertFalse(response3.isEmpty());
        InstanceRequestV2Entity instanceRequestEntity1 = response3.get();
        assertEquals(SpotInstanceRequestState.OPEN, instanceRequestEntity1.getState());

        // If request is open and all requested instances are not created then it should be open
        Optional<InstanceRequestV2Entity> response5 =
                instanceRequestV2Repository.findRequestByIdAndCustomer("r5", DUMMY_CUSTOMER_ID);
        assertFalse(response5.isEmpty());
        InstanceRequestV2Entity instanceRequestEntity5 = response3.get();
        assertEquals(SpotInstanceRequestState.OPEN, instanceRequestEntity5.getState());

        // deleted canceled requests and its instances
        Optional<InstanceRequestV2Entity> response6 =
                instanceRequestV2Repository.findRequestByIdAndCustomer("r6", DUMMY_CUSTOMER_ID);
        assertTrue(response6.isEmpty());
        Optional<InstanceV2Entity> instanceListResp6 =
                instanceV2Repository.findInstanceById("i61");
        assertTrue(instanceListResp6.isEmpty());

        // not deleted canceled requests as time check not passed
        Optional<InstanceRequestV2Entity> response7 =
                instanceRequestV2Repository.findRequestByIdAndCustomer("r7", DUMMY_CUSTOMER_ID);
        assertFalse(response7.isEmpty());
    }

    private void insertEntriesInDb() {
        // Insert requests
        instanceRequestV2Repository.insert(getInstanceRequestEntity("r0", SpotInstanceRequestState.OPEN,
                                                          TimeUtils.getFirstDateOfPreviousMonth(0)));
        instanceRequestV2Repository.insert(getInstanceRequestEntity("r1", SpotInstanceRequestState.OPEN,
                                                          TimeUtils.getFirstDateOfPreviousMonth(1)));
        instanceRequestV2Repository.insert(getInstanceRequestEntity("r2", SpotInstanceRequestState.OPEN,
                                                          TimeUtils.getFirstDateOfPreviousMonth(2)));
        instanceRequestV2Repository.insert(getInstanceRequestEntity("r3", SpotInstanceRequestState.CLOSED,
                                                          TimeUtils.getFirstDateOfPreviousMonth(3)));
        instanceRequestV2Repository.insert(getInstanceRequestEntity("r4", SpotInstanceRequestState.OPEN,
                                                          TimeUtils.getCurrentDate()));
        instanceRequestV2Repository.insert(getInstanceRequestEntity("r5", SpotInstanceRequestState.OPEN,
                TimeUtils.getCurrentDate()));

        // canceled request without instances and time check passed
        instanceRequestV2Repository.insert(getInstanceRequestEntity("r6", SpotInstanceRequestState.CANCELED,
                TimeUtils.getFirstDateOfPreviousMonth(1)));
        // canceled request with time check not passed
        instanceRequestV2Repository.insert(getInstanceRequestEntity("r7", SpotInstanceRequestState.CANCELED,
                TimeUtils.getCurrentDate()));

        // Insert instances
        instanceV2Repository.insert(
                getInstanceEntity("r0", "i0", TimeUtils.getFirstDateOfPreviousMonth(0), "z1",
                                      SpotInstanceInternalState.STARTING,
                                      SpotInstanceRequestState.ACTIVE));
        instanceV2Repository.insert(
                getInstanceEntity("r0", "i01", TimeUtils.getFirstDateOfPreviousMonth(0), "z1",
                                      SpotInstanceInternalState.STARTING,
                                      SpotInstanceRequestState.ACTIVE));
        instanceV2Repository.insert(
                getInstanceEntity("r0", "i02", TimeUtils.getFirstDateOfPreviousMonth(0), "z1",
                                      SpotInstanceInternalState.STARTING,
                                      SpotInstanceRequestState.ACTIVE));

        instanceV2Repository.insert(
                getInstanceEntity("r1", "i1", TimeUtils.getFirstDateOfPreviousMonth(1), "z1",
                                      SpotInstanceInternalState.RUNNING,
                                      SpotInstanceRequestState.ACTIVE));
        instanceV2Repository.insert(
                getInstanceEntity("r1", "i11", TimeUtils.getFirstDateOfPreviousMonth(1), "z1",
                                      SpotInstanceInternalState.RUNNING,
                                      SpotInstanceRequestState.ACTIVE));
        instanceV2Repository.insert(
                getInstanceEntity("r1", "i12", TimeUtils.getFirstDateOfPreviousMonth(1), "z1",
                                      SpotInstanceInternalState.RUNNING,
                                      SpotInstanceRequestState.ACTIVE));

        instanceV2Repository.insert(
                getInstanceEntity("r2", "i2", TimeUtils.getFirstDateOfPreviousMonth(2), "z2",
                                      SpotInstanceInternalState.TERMINATED,
                                      SpotInstanceRequestState.CLOSED));
        instanceV2Repository.insert(
                getInstanceEntity("r2", "i21", TimeUtils.getFirstDateOfPreviousMonth(2), "z2",
                                      SpotInstanceInternalState.TERMINATED,
                                      SpotInstanceRequestState.CLOSED));
        instanceV2Repository.insert(
                getInstanceEntity("r2", "i22", TimeUtils.getFirstDateOfPreviousMonth(2), "z2",
                                      SpotInstanceInternalState.TERMINATED,
                                      SpotInstanceRequestState.CLOSED));

        instanceV2Repository.insert(
                getInstanceEntity("r3", "i3", TimeUtils.getFirstDateOfPreviousMonth(3), "z2",
                                      SpotInstanceInternalState.TERMINATED,
                                      SpotInstanceRequestState.CLOSED));
        instanceV2Repository.insert(
                getInstanceEntity("r3", "i31", TimeUtils.getFirstDateOfPreviousMonth(3), "z2",
                                      SpotInstanceInternalState.TERMINATED,
                                      SpotInstanceRequestState.CLOSED));

        instanceV2Repository.insert(
                getInstanceEntity("r5", "i5", TimeUtils.getFirstDateOfPreviousMonth(1), "z5",
                        SpotInstanceInternalState.RUNNING,
                        SpotInstanceRequestState.ACTIVE));

        instanceV2Repository.insert(
                getInstanceEntity("r6", "i61", TimeUtils.getFirstDateOfPreviousMonth(2), "z6",
                        SpotInstanceInternalState.TERMINATED,
                        SpotInstanceRequestState.CLOSED));
    }

    private InstanceRequestV2Entity getInstanceRequestEntity(
            String requestId, SpotInstanceRequestState state, Instant instant) {
        return InstanceRequestV2Entity.builder()
                .requestId(requestId)
                .customer(DUMMY_CUSTOMER_ID)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(instant))
                .state(state)
                .statusUpdateTime(instant)
                .instanceCount(3)
                .build();
    }

    private InstanceV2Entity getInstanceEntity(
            String requestId, String instanceId,
            Instant instant, String zoneName,
            SpotInstanceInternalState instanceInternalState,
            SpotInstanceRequestState requestState) {

        return InstanceV2Entity.builder()
                .customer(DUMMY_CUSTOMER_ID)
                .createTimeuuid(TimeUtils.getUuidFromTimeStamp(instant))
                .instanceUpdateTime(instant)
                .requestId(requestId)
                .instanceId(instanceId)
                .zone(zoneName)
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .gpu(DUMMY_GPU)
                .backend(CloudProvider.AWS.toString())
                .ncaId(DUMMY_NON_BYOC_NCA_ID)
                .instanceStateName(instanceInternalState)
                .requestState(requestState)
                .instanceStateCode(
                        SpotInstanceInternalState.getStateCode(instanceInternalState))
                .build();
    }
}
