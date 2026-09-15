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

import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_NCA_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.GetActiveInstanceInfoResponse;
import com.nvidia.icms.inbound.rest.model.InstanceInfo;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceStatus;
import com.nvidia.icms.outbound.cassandra.instance.InstancePerZoneRepository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneEntity;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceByZoneKey;
import java.time.Instant;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class InstanceInfoServiceTest {

    @Mock
    private InstancePerZoneRepository instancePerZoneRepository;

    @InjectMocks
    private InstanceInfoService instanceInfoService;

    @Test
    void getActiveInstancesForZone_withValidInputs_returnsSuccess() {

        // Prepare
        when(instancePerZoneRepository.findAllActiveInstancesByZone(DUMMY_ZONE)).thenReturn(
                List.of(getDummyInstanceByZoneEntity()));

        // Act
        GetActiveInstanceInfoResponse response =
                instanceInfoService.getActiveInstancesForZone(DUMMY_ZONE);

        // Assert
        assertNotNull(response);
        assertNotEquals(0, response.getInstances().size());
        InstanceInfo instanceInfo = response.getInstances().get(0);
        assertEquals(DUMMY_INSTANCE_ID, instanceInfo.getInstanceId());
        assertEquals(SpotInstanceInternalState.RUNNING.getStateName(),
                     instanceInfo.getInstanceState());
        assertEquals(DUMMY_REQUEST_ID, instanceInfo.getRequestId());

        // verify
        verify(instancePerZoneRepository).findAllActiveInstancesByZone(DUMMY_ZONE);
    }

    @Test
    void getActiveInstancesForZone_withFailedToFetchInfo_throwsException() {

        // Prepare
        when(instancePerZoneRepository.findAllActiveInstancesByZone(DUMMY_ZONE)).thenThrow(
                new IcmsInternalServerException("dummy_exception"));

        // Act
        IcmsInternalServerException exception = assertThrows(IcmsInternalServerException.class,
                                                            () -> {
                                                                instanceInfoService.getActiveInstancesForZone(
                                                                        DUMMY_ZONE);
                                                            });

        // Assert
        assertNotNull(exception);
        assertEquals("dummy_exception", exception.getBody().getDetail());

        // verify
        verify(instancePerZoneRepository).findAllActiveInstancesByZone(DUMMY_ZONE);
    }

    private InstanceByZoneEntity getDummyInstanceByZoneEntity() {
        return InstanceByZoneEntity.builder()
                .key(InstanceByZoneKey.builder()
                             .instanceId(DUMMY_INSTANCE_ID)
                             .zone(DUMMY_ZONE)
                             .truncatedTs(Instant.now())
                             .build())
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .backend("AWS")
                .ncaId(DUMMY_NON_BYOC_NCA_ID)
                .requestId(DUMMY_REQUEST_ID)
                .gpu(DUMMY_GPU_NAME)
                .instanceStateCode(16)
                .instanceStateName(SpotInstanceInternalState.RUNNING)
                .customer(DUMMY_CUSTOMER_1)
                .imageId(DUMMY_CONTAINER_IMAGE)
                .requestState(SpotInstanceRequestState.ACTIVE)
                .requestStatusCode(SpotInstanceStatus.FULFILLED)
                .requestStatusUpdateTime(Instant.now())
                .updateTimestamp(Instant.now())
                .build();
    }
}