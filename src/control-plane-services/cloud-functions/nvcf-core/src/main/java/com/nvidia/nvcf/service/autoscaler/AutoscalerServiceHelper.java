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
package com.nvidia.nvcf.service.autoscaler;

import com.nvidia.nvcf.icms.client.IcmsStubService.Instance;
import com.nvidia.nvcf.proto.AutoscalerResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.ScalingStatusEnum;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class AutoscalerServiceHelper {

    static AutoscalerResponse buildScalingDownResponse(
            int terminating,
            Map<UUID, Set<Instance>> instances,
            FunctionStatusEnum functionStatus) {
        return buildResponse(instances, functionStatus, 0, terminating, ScalingStatusEnum.SCALING_DOWN);
    }

    static AutoscalerResponse buildScalingUpResponse(
            int allocating,
            Map<UUID, Set<Instance>> instances,
            FunctionStatusEnum functionStatus) {
        return buildResponse(instances, functionStatus, allocating, 0, ScalingStatusEnum.SCALING_UP);
    }

    static AutoscalerResponse buildNotScalableResponse(
            Map<UUID, Set<Instance>> instances, FunctionStatusEnum functionStatus) {
        return buildResponse(instances, functionStatus, 0, 0, ScalingStatusEnum.NOT_AUTO_SCALABLE);
    }

    static AutoscalerResponse buildNoScalingNeededResponse(
            Map<UUID, Set<Instance>> instances, FunctionStatusEnum functionStatus) {
        return buildResponse(instances, functionStatus, 0, 0, ScalingStatusEnum.NO_SCALING_NEEDED);
    }

    private static AutoscalerResponse buildResponse(
            Map<UUID, Set<Instance>> instances,
            FunctionStatusEnum functionStatus,
            int allocating,
            int terminating,
            ScalingStatusEnum scalingStatus) {
        var active = instances.values().stream()
                .flatMap(Set::stream)
                .filter(instance -> instance.getState().isRunning())
                .count();
        var pending = instances.values().stream()
                .flatMap(Set::stream)
                .filter(instance -> instance.getState().isStarting())
                .count();
        return AutoscalerResponse.newBuilder()
                .setActiveInstances((int) active)
                .setPendingInstances((int) pending)
                .setAllocatingInstances(allocating)
                .setTerminatingInstances(terminating)
                .setFunctionStatus(functionStatus.toString())
                .setScalingStatus(scalingStatus.toString())
                .build();
    }
}
