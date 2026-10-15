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

import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_FUNCTION_VERSION_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static com.nvidia.icms.util.TestUtil.customObjectMapper;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.Mockito.verify;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.util.GsonCompatMapper;
import com.nvidia.icms.inbound.rest.model.ClientRequestDataModel;
import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import com.nvidia.icms.util.TimeUtils;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class LatestInstanceStateEventServiceTest {

    @Mock
    private TelemetryEventClient telemetryEventClient;

    private ObjectMapper objectMapper;

    private LatestInstanceStateEventService service;

    @Captor
    private ArgumentCaptor<List<GenericMetric>> metricsCaptor;

    @BeforeEach
    void setUp() {
        objectMapper = customObjectMapper();
        service = new LatestInstanceStateEventService(telemetryEventClient, objectMapper);
    }

    @Test
    void sendLatestInstanceStateEvent_happyPath_sendsExpectedMetric() {
        UUID deploymentId = UUID.randomUUID();
        UUID gpuSpecificationId = UUID.randomUUID();
        String taskId = UUID.randomUUID().toString();
        String taskName = "dummy_task";
        Instant instantNow = Instant.now();
        InstanceV2Entity entity = buildEntityWithLaunchSpecJson(true, deploymentId, gpuSpecificationId, taskId, taskName, instantNow);

        service.sendLatestInstanceStateEvent(entity);

        verify(telemetryEventClient).triggerEvent(metricsCaptor.capture());
        List<GenericMetric> metrics = metricsCaptor.getValue();
        assertNotNull(metrics);
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.getFirst();

        assertNotNull(metric.getInstanceCreateTime());
        assertEquals(Events.LATEST_INSTANCE_STATE.toString(), metric.getEventName());
        assertEquals(DUMMY_REQUEST_ID, metric.getRequestId());
        assertEquals(DUMMY_INSTANCE_ID, metric.getInstanceId());
        assertEquals(SpotInstanceInternalState.RUNNING.getStateName(), metric.getInstanceState());
        assertEquals(DUMMY_GPU_NAME, metric.getGpuName());
        // ClusterId is set using zone
        assertEquals(DUMMY_ZONE, metric.getClusterId());
        assertEquals(CloudProvider.AWS.toString(), metric.getCloudProvider());
        // NCA, capacity, region
        assertEquals("dummy-nca-id", metric.getNcaId());
        assertEquals("SPOT", metric.getCapacityType());
        assertEquals("us-east-1", metric.getRegionName());
        assertEquals(DUMMY_FUNCTION_ID, metric.getFunctionId());
        assertEquals(DUMMY_FUNCTION_VERSION_ID, metric.getFunctionVersionId());
        assertEquals(DUMMY_FUNCTION_NAME, metric.getFunctionName());
        // Deployment and GPU spec ids
        assertEquals(deploymentId, metric.getDeploymentId());
        assertEquals(gpuSpecificationId, metric.getGpuSpecificationId());
        // Task information
        assertEquals(taskId, metric.getTaskId());
        assertEquals(taskName, metric.getTaskName());
        // ReservationId should be populated
        assertNotNull(metric.getReservationId());
    }

    @Test
    void sendLatestInstanceStateEvent_withInvalidRequestJson_stillSendsEvent() {
        InstanceV2Entity entity = buildEntityWithLaunchSpecJson(false, null, null, null, null, Instant.now());

        service.sendLatestInstanceStateEvent(entity);

        verify(telemetryEventClient).triggerEvent(anyList());
    }

    private InstanceV2Entity buildEntityWithLaunchSpecJson(boolean validJson, UUID deploymentId, UUID gpuSpecificationId, String taskId, String taskName, Instant createTimeStamp) {
        InstanceV2Entity entity = InstanceV2Entity.getEmptyEntity();
        entity.setRequestId(DUMMY_REQUEST_ID);
        entity.setInstanceId(DUMMY_INSTANCE_ID);
        entity.setInstanceStateName(SpotInstanceInternalState.RUNNING);
        entity.setGpu(DUMMY_GPU_NAME);
        entity.setCapacityType("SPOT");
        entity.setRegion("us-east-1");
        entity.setZone(DUMMY_ZONE);
        entity.setCloudProvider("aws");
        entity.setNcaId("dummy-nca-id");
        entity.setReservationId(UUID.randomUUID());
        entity.setCreateTimeuuid(TimeUtils.getUuidFromTimeStamp(createTimeStamp));

        if (validJson) {
            ClientRequestDataModel.LaunchSpecification launchSpec =
                    ClientRequestDataModel.LaunchSpecification.builder()
                            .functionId(DUMMY_FUNCTION_ID)
                            .versionId(DUMMY_FUNCTION_VERSION_ID)
                            .functionName(DUMMY_FUNCTION_NAME)
                            .taskId(taskId)
                            .taskName(taskName)
                            .deploymentId(deploymentId)
                            .gpuSpecificationId(gpuSpecificationId)
                            .gpu(DUMMY_GPU_NAME)
                            .build();
            ClientRequestDataModel data = ClientRequestDataModel.builder()
                    .instanceCount(1)
                    .requestId(DUMMY_REQUEST_ID)
                    .launchSpecification(launchSpec)
                    .build();
            entity.setRequestRawData(GsonCompatMapper.toJson(data));
        } else {
            entity.setRequestRawData("{invalid-json");
        }
        return entity;
    }
}

