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

import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_1;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.createInstances.CreateInstanceService;
import java.util.Map;
import java.util.UUID;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
public class InstanceServiceTest {

    @Mock
    private ByocService byocService;

    @Mock
    private DescribeAndCancelInstanceService describeAndCancelInstanceService;

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private TerminateInstanceService terminateInstanceService;
    @Mock
    private FunctionBillingService functionBillingService;

    @Mock
    private InstanceInfoService instanceInfoService;

    @Mock
    private CancelInstanceService cancelInstanceService;

    @Mock
    private CreateInstanceService createInstanceService;

    private InstanceService instanceService;

    @BeforeEach
    void init() {
        instanceService = new InstanceService(
                describeAndCancelInstanceService,
                cancelInstanceService,
                terminateInstanceService,
                icmsConfigurationProperties,
                functionBillingService,
                instanceInfoService,
                createInstanceService);
    }

    @Test
    void requestInstances_withBYOC_success() {

        // Prepare
        SpotInstanceRequestSchema instanceRequestSchema = SpotInstanceRequestSchema.builder()
                .action(REQUEST_SPOT_INSTANCES).instanceCount(1)
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .containerImage(DUMMY_CONTAINER_IMAGE).environment(DUMMY_ENVIRONMENT_VALUE)
                .backend("backend")
                .gpu("gpu").ncaId("ncaId").build();

        UUID uuid = UUID.randomUUID();
        when(createInstanceService.processInstanceRequest(DUMMY_CUSTOMER_1,
                                                          instanceRequestSchema,
                                                          Map.of())).thenReturn(
                new CreateSpotInstancesResponse(uuid));

        // Act
        CreateSpotInstancesResponse createInstancesResponse =
                instanceService.requestInstances(DUMMY_CUSTOMER_1, instanceRequestSchema,
                                                 Map.of());

        // Assert
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertEquals(uuid, createInstancesResponse.getRequestId());
        verify(createInstanceService).processInstanceRequest(DUMMY_CUSTOMER_1,
                                                             instanceRequestSchema, Map.of());
    }

    @Test
    void terminateInstancesByGpuSpec_delegatesToTerminateService() {
        UUID deploymentId = UUID.randomUUID();
        UUID gpuSpecId = UUID.randomUUID();
        var response = new TerminateInstancesResponse();
        when(terminateInstanceService.terminateInstances(
                "nca", deploymentId, gpuSpecId, 2, Map.of())).thenReturn(response);

        var result = instanceService.terminateInstances(
                "nca", deploymentId, gpuSpecId, 2, Map.of());

        Assertions.assertSame(response, result);
        verify(terminateInstanceService).terminateInstances(
                "nca", deploymentId, gpuSpecId, 2, Map.of());
    }

    @Test
    void requestInstances_withBYOCAndHelmChart_success() {

        // Prepare
        SpotInstanceRequestSchema instanceRequestSchema = SpotInstanceRequestSchema.builder()
                .action(REQUEST_SPOT_INSTANCES).instanceCount(1)
                .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                .containerImage(null).environment(DUMMY_ENVIRONMENT_VALUE)
                .backend("backend")
                .gpu("gpu").ncaId("ncaId").helmChart("helmChart").build();

        UUID uuid = UUID.randomUUID();
        when(createInstanceService.processInstanceRequest(DUMMY_CUSTOMER_1,
                                                          instanceRequestSchema,
                                                          Map.of())).thenReturn(
                new CreateSpotInstancesResponse(uuid));

        // Act
        CreateSpotInstancesResponse createInstancesResponse =
                instanceService.requestInstances(DUMMY_CUSTOMER_1, instanceRequestSchema,
                                                 Map.of());

        // Assert
        Assertions.assertNotNull(createInstancesResponse);
        Assertions.assertEquals(uuid, createInstancesResponse.getRequestId());
        verify(createInstanceService).processInstanceRequest(DUMMY_CUSTOMER_1,
                                                             instanceRequestSchema, Map.of());
    }



    @Test
    void describeInstancesByDeploymentId_success() {
        String ncaId = "nca-id";
        UUID deploymentId = UUID.randomUUID();
        UUID gpuSpecId = UUID.randomUUID();
        GetSpotInstanceRequests response = new GetSpotInstanceRequests();

        when(describeAndCancelInstanceService.describeInstancesByDeploymentId(
                ncaId,
                deploymentId,
                gpuSpecId,
                true,
                false)).thenReturn(response);

        GetSpotInstanceRequests result = instanceService.describeInstancesByDeploymentId(
                ncaId,
                deploymentId,
                gpuSpecId,
                true,
                false);

        Assertions.assertSame(response, result);
        verify(describeAndCancelInstanceService)
                .describeInstancesByDeploymentId(
                        ncaId,
                        deploymentId,
                        gpuSpecId,
                        true,
                        false);
    }
}
