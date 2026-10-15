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

import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.SpotInstance;
import com.nvidia.icms.inbound.rest.model.SpotInstanceLaunchSpecification;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestStatus;
import com.nvidia.icms.inbound.rest.model.SpotInstanceState;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.instance.GetInstanceRequestsResponse;
import com.nvidia.icms.inbound.rest.model.instance.Instance;
import com.nvidia.icms.inbound.rest.model.instance.InstanceLaunchSpecification;
import com.nvidia.icms.inbound.rest.model.instance.InstanceRequest;
import com.nvidia.icms.inbound.rest.model.instance.InstanceRequestState;
import com.nvidia.icms.inbound.rest.model.instance.InstanceRequestStatus;
import com.nvidia.icms.inbound.rest.model.instance.InstanceState;
import com.nvidia.icms.inbound.rest.model.instance.TerminateInstanceRequestsResponse;
import jakarta.annotation.Nullable;
import java.util.List;
import lombok.experimental.UtilityClass;

/**
 * Converts the legacy response models into the new "Instance"-prefixed
 * response models.
 */
@UtilityClass
public class InstanceModelConverter {

    @Nullable
    public static GetInstanceRequestsResponse toGetInstanceRequestsResponse(
            @Nullable GetSpotInstanceRequests src) {
        if (src == null) {
            return null;
        }
        return GetInstanceRequestsResponse.builder()
                .instanceRequests(toInstanceRequests(src.getSpotInstanceRequest()))
                .instances(toInstances(src.getSpotInstances()))
                .build();
    }

    @Nullable
    public static TerminateInstanceRequestsResponse toTerminateInstanceRequestsResponse(
            @Nullable TerminateInstancesResponse src) {
        if (src == null) {
            return null;
        }
        TerminateInstanceRequestsResponse dest = new TerminateInstanceRequestsResponse();
        if (src.getTerminatingInstances() != null) {
            dest.setTerminatingInstances(src.getTerminatingInstances().stream()
                    .map(InstanceModelConverter::toTerminatingInstance)
                    .toList());
        }
        return dest;
    }

    private static List<InstanceRequest> toInstanceRequests(List<SpotInstanceRequest> src) {
        if (src == null) {
            return null;
        }
        return src.stream().map(InstanceModelConverter::toInstanceRequest).toList();
    }

    private static List<Instance> toInstances(List<SpotInstance> src) {
        if (src == null) {
            return null;
        }
        return src.stream().map(InstanceModelConverter::toInstance).toList();
    }

    private static InstanceRequest toInstanceRequest(SpotInstanceRequest src) {
        if (src == null) {
            return null;
        }
        return InstanceRequest.builder()
                .createTime(src.getCreateTime())
                .instanceId(src.getInstanceId())
                .instanceLaunchSpecification(
                        toInstanceLaunchSpecification(src.getSpotInstanceLaunchSpecification()))
                .launchedAvailabilityZone(src.getLaunchedAvailabilityZone())
                .instanceRequestId(src.getSpotInstanceRequestId())
                .cloudProvider(src.getSpotCloudProvider())
                .state(toInstanceRequestState(src.getState()))
                .status(toInstanceRequestStatus(src.getStatus()))
                .instanceState(toInstanceState(src.getInstanceState()))
                .healthInfo(src.getHealthInfo())
                .instanceInterruptionBehavior(src.getInstanceInterruptionBehavior())
                .instanceIps(src.getInstanceIps())
                .deploymentId(src.getDeploymentId())
                .gpuSpecificationId(src.getGpuSpecificationId())
                .build();
    }

    private static Instance toInstance(SpotInstance src) {
        if (src == null) {
            return null;
        }
        InstanceLaunchSpecification.Placement placement = null;
        if (src.getPlacement() != null) {
            placement = new InstanceLaunchSpecification.Placement(
                    src.getPlacement().getAvailabilityZone());
        }
        return Instance.builder()
                .createTime(src.getCreateTime())
                .imageId(src.getImageId())
                .containerImage(src.getContainerImage())
                .instanceId(src.getInstanceId())
                .cloudProvider(src.getSpotCloudProvider())
                .instanceType(src.getInstanceType())
                .placement(placement)
                .state(toInstanceState(src.getState()))
                .healthInfo(src.getHealthInfo())
                .launchRequestId(src.getLaunchRequestId())
                .instanceIps(src.getInstanceIps())
                .capacityType(src.getCapacityType())
                .deploymentId(src.getDeploymentId())
                .gpuSpecificationId(src.getGpuSpecificationId())
                .requestId(src.getRequestId())
                .gpu(src.getGpu())
                .updateTime(src.getUpdateTime())
                .build();
    }

    private static InstanceLaunchSpecification toInstanceLaunchSpecification(
            SpotInstanceLaunchSpecification src) {
        if (src == null) {
            return null;
        }
        InstanceLaunchSpecification dest = new InstanceLaunchSpecification();
        dest.setInstanceType(src.getInstanceType());
        dest.setContainerImage(src.getContainerImage());
        if (src.getPlacement() != null) {
            dest.setPlacement(new InstanceLaunchSpecification.Placement(
                    src.getPlacement().getAvailabilityZone()));
        }
        dest.setGpu(src.getGpu());
        dest.setBackend(src.getBackend());
        dest.setNcaId(src.getNcaId());
        dest.setCapacityType(src.getCapacityType());
        return dest;
    }

    private static InstanceRequestState toInstanceRequestState(SpotInstanceRequestState src) {
        if (src == null) {
            return null;
        }
        return InstanceRequestState.toInstanceRequestState(src.toString()).orElse(null);
    }

    private static InstanceRequestStatus toInstanceRequestStatus(SpotInstanceRequestStatus src) {
        if (src == null) {
            return null;
        }
        return new InstanceRequestStatus(src.getCode(), src.getMessage(), src.getUpdateTime());
    }

    private static InstanceState toInstanceState(SpotInstanceState src) {
        if (src == null) {
            return null;
        }
        return InstanceState.builder()
                .code(src.getCode())
                .name(src.getName())
                .build();
    }

    private static TerminateInstanceRequestsResponse.TerminatingInstance toTerminatingInstance(
            TerminateInstancesResponse.TerminatingInstance src) {
        if (src == null) {
            return null;
        }
        return TerminateInstanceRequestsResponse.TerminatingInstance.builder()
                .instanceId(src.getInstanceId())
                .requestId(src.getRequestId())
                .currentState(toInstanceState(src.getCurrentState()))
                .previousState(toInstanceState(src.getPreviousState()))
                .build();
    }
}
