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
package com.nvidia.icms.inbound.rest.converters;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.icms.inbound.rest.model.CloudProvider;
import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.HealthInfo;
import com.nvidia.icms.inbound.rest.model.SpotInstance;
import com.nvidia.icms.inbound.rest.model.SpotInstanceLaunchSpecification;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceState;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.instance.GetInstanceRequestsResponse;
import com.nvidia.icms.inbound.rest.model.instance.Instance;
import com.nvidia.icms.inbound.rest.model.instance.InstanceRequest;
import com.nvidia.icms.inbound.rest.model.instance.InstanceRequestState;
import com.nvidia.icms.inbound.rest.model.instance.TerminateInstanceRequestsResponse;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import java.time.Instant;
import java.util.List;
import java.util.Set;
import org.junit.jupiter.api.Test;

class InstanceModelConverterTest {

    private static final String REQUEST_ID = "req-123";
    private static final String INSTANCE_ID = "inst-abc";

    @Test
    void toGetInstanceRequestsResponse_mapsAllFieldsAndRenamesLegacyPrefixes() {
        Instant createTime = Instant.parse("2025-01-01T00:00:00Z");
        Instant updateTime = Instant.parse("2025-01-01T01:00:00Z");
        SpotInstanceRequest instanceRequest = SpotInstanceRequest.builder()
                .createTime(createTime)
                .instanceId(INSTANCE_ID)
                .spotInstanceRequestId(REQUEST_ID)
                .spotCloudProvider(CloudProvider.AWS)
                .state(SpotInstanceRequestState.ACTIVE)
                .status(new SpotInstanceRequestStatus("code", "msg", createTime))
                .instanceState(new SpotInstanceState(16, "running"))
                .healthInfo(new HealthInfo())
                .instanceInterruptionBehavior("terminate")
                .instanceIps(Set.of("1.2.3.4"))
                .deploymentId("dep-1")
                .gpuSpecificationId("gpu-1")
                .launchedAvailabilityZone("zone-1")
                .spotInstanceLaunchSpecification(new SpotInstanceLaunchSpecification(
                        "instType", "img",
                        new SpotInstanceLaunchSpecification.Placement("zone-1"),
                        "gpu", "backend", "ncaId", CapacityType.SPOT))
                .build();

        SpotInstance instance = SpotInstance.builder()
                .createTime(createTime)
                .imageId("img-1")
                .containerImage("c-img")
                .instanceId(INSTANCE_ID)
                .spotCloudProvider(CloudProvider.AWS)
                .instanceType("instType")
                .placement(new SpotInstanceLaunchSpecification.Placement("zone-1"))
                .state(new SpotInstanceState(16, "running"))
                .healthInfo(new HealthInfo())
                .launchRequestId(REQUEST_ID)
                .instanceIps(Set.of("1.2.3.4"))
                .capacityType(CapacityType.SPOT)
                .deploymentId("dep-1")
                .gpuSpecificationId("gpu-1")
                .requestId(REQUEST_ID)
                .gpu("gpu")
                .updateTime(updateTime)
                .build();

        GetSpotInstanceRequests src = new GetSpotInstanceRequests(
                List.of(instanceRequest), List.of(instance));

        GetInstanceRequestsResponse dest = InstanceModelConverter.toGetInstanceRequestsResponse(src);

        assertThat(dest).isNotNull();
        assertThat(dest.getInstanceRequests()).hasSize(1);
        InstanceRequest mappedRequest = dest.getInstanceRequests().get(0);
        assertThat(mappedRequest.getInstanceRequestId()).isEqualTo(REQUEST_ID);
        assertThat(mappedRequest.getCloudProvider()).isEqualTo(CloudProvider.AWS);
        assertThat(mappedRequest.getInstanceId()).isEqualTo(INSTANCE_ID);
        assertThat(mappedRequest.getCreateTime()).isEqualTo(createTime);
        assertThat(mappedRequest.getState()).isEqualTo(InstanceRequestState.ACTIVE);
        assertThat(mappedRequest.getStatus()).isNotNull();
        assertThat(mappedRequest.getStatus().getCode()).isEqualTo("code");
        assertThat(mappedRequest.getInstanceState()).isNotNull();
        assertThat(mappedRequest.getInstanceState().getCode()).isEqualTo(16);
        assertThat(mappedRequest.getInstanceState().getName()).isEqualTo("running");
        assertThat(mappedRequest.getInstanceLaunchSpecification()).isNotNull();
        assertThat(mappedRequest.getInstanceLaunchSpecification().getInstanceType())
                .isEqualTo("instType");
        assertThat(mappedRequest.getInstanceLaunchSpecification().getPlacement().getAvailabilityZone())
                .isEqualTo("zone-1");
        assertThat(mappedRequest.getInstanceIps()).containsExactly("1.2.3.4");

        assertThat(dest.getInstances()).hasSize(1);
        Instance mappedInstance = dest.getInstances().get(0);
        assertThat(mappedInstance.getCloudProvider()).isEqualTo(CloudProvider.AWS);
        assertThat(mappedInstance.getInstanceId()).isEqualTo(INSTANCE_ID);
        assertThat(mappedInstance.getCapacityType()).isEqualTo(CapacityType.SPOT);
        assertThat(mappedInstance.getRequestId()).isEqualTo(REQUEST_ID);
        assertThat(mappedInstance.getGpu()).isEqualTo("gpu");
        assertThat(mappedInstance.getUpdateTime()).isEqualTo(updateTime);
        assertThat(mappedInstance.getState()).isNotNull();
        assertThat(mappedInstance.getState().getName()).isEqualTo("running");
    }

    @Test
    void toGetInstanceRequestsResponse_withNullSource_returnsNull() {
        assertThat(InstanceModelConverter.toGetInstanceRequestsResponse(null)).isNull();
    }

    @Test
    void toGetInstanceRequestsResponse_withNullCollections_returnsResponseWithNullCollections() {
        GetSpotInstanceRequests src = new GetSpotInstanceRequests(null, null);

        GetInstanceRequestsResponse dest = InstanceModelConverter.toGetInstanceRequestsResponse(src);

        assertThat(dest).isNotNull();
        assertThat(dest.getInstanceRequests()).isNull();
        assertThat(dest.getInstances()).isNull();
    }

    @Test
    void toTerminateInstanceRequestsResponse_mapsAllFields() {
        TerminateInstancesResponse.TerminatingInstance src = TerminateInstancesResponse.TerminatingInstance
                .builder()
                .instanceId(INSTANCE_ID)
                .requestId(REQUEST_ID)
                .currentState(new SpotInstanceState(32, "shutting-down"))
                .previousState(new SpotInstanceState(16, "running"))
                .build();

        TerminateInstancesResponse legacy = new TerminateInstancesResponse(List.of(src));

        TerminateInstanceRequestsResponse dest =
                InstanceModelConverter.toTerminateInstanceRequestsResponse(legacy);

        assertThat(dest).isNotNull();
        assertThat(dest.getTerminatingInstances()).hasSize(1);
        TerminateInstanceRequestsResponse.TerminatingInstance mapped =
                dest.getTerminatingInstances().get(0);
        assertThat(mapped.getInstanceId()).isEqualTo(INSTANCE_ID);
        assertThat(mapped.getRequestId()).isEqualTo(REQUEST_ID);
        assertThat(mapped.getCurrentState()).isNotNull();
        assertThat(mapped.getCurrentState().getCode()).isEqualTo(32);
        assertThat(mapped.getCurrentState().getName()).isEqualTo("shutting-down");
        assertThat(mapped.getPreviousState()).isNotNull();
        assertThat(mapped.getPreviousState().getCode()).isEqualTo(16);
        assertThat(mapped.getPreviousState().getName()).isEqualTo("running");
    }

    @Test
    void toTerminateInstanceRequestsResponse_withNullSource_returnsNull() {
        assertThat(InstanceModelConverter.toTerminateInstanceRequestsResponse(null)).isNull();
    }

    @Test
    void toTerminateInstanceRequestsResponse_withNullList_returnsResponseWithNullList() {
        TerminateInstancesResponse legacy = new TerminateInstancesResponse(null);

        TerminateInstanceRequestsResponse dest =
                InstanceModelConverter.toTerminateInstanceRequestsResponse(legacy);

        assertThat(dest).isNotNull();
        assertThat(dest.getTerminatingInstances()).isNull();
    }
}
