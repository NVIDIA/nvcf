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
package com.nvidia.nvct.service.scheduler;

import static com.google.common.collect.ImmutableListMultimap.toImmutableListMultimap;
import static com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest;
import static com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.State.CANCELED;
import static com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.State.CLOSED;
import static com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.State.FAILED;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.google.common.collect.ImmutableListMultimap;
import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.rest.task.dto.HealthDto;
import com.nvidia.nvct.service.icms.IcmsStubService.GetInstancesResponse.InstanceRequest.State;
import jakarta.validation.constraints.NotNull;
import java.util.Collection;
import java.util.Collections;
import java.util.EnumSet;
import java.util.List;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
class CommonRoutineService {

    private static final String MESG_ICMS_REQUEST_INSTANCE_DETAILS =
            "ICMS Request Id %s: Request State %s; Instance Id %s; Instance State %s; ";
    private static final String MESG_HEALTH_INFO_UNAVAILABLE =
            "Health info is not available";
    private static final String UNKNOWN = "UNKNOWN";

    public static final Set<State> TERMINAL_INSTANCE_STATES =
            Collections.unmodifiableSet(EnumSet.of(CLOSED, CANCELED, FAILED));

    public static ImmutableListMultimap<UUID, InstanceRequest> mapInstancesByRequestId(
            Collection<InstanceRequest> instanceRequests) {
        return instanceRequests.stream()
                .collect(toImmutableListMultimap(
                        InstanceRequest::getInstanceRequestId,
                        Function.identity()));
    }

    // Return true if there are no instances or all of the instances are in a terminal state.
    public static boolean areAllInstancesInTerminalState(
            Collection<InstanceRequest> instanceRequests) {
        return CollectionUtils.isEmpty(instanceRequests)
                || instanceRequests.stream().map(InstanceRequest::getState)
                .allMatch(TERMINAL_INSTANCE_STATES::contains);
    }

    public static Set<HealthDto> getHealthDtos(
            List<UUID> instanceRequestIds,
            List<InstanceRequest> instanceRequests) {
        return instanceRequestIds.stream()
                .map(instanceRequestId -> {
                    var instances = instanceRequests.stream()
                            .filter(instanceRequest -> instanceRequest.getInstanceRequestId()
                                    .equals(instanceRequestId))
                            .toList();
                    var allWithErrors = instances.stream()
                            .allMatch(instance -> instance.getHealthInfo() != null &&
                                    isNotBlank(instance.getHealthInfo().getErrorLog()));
                    if (allWithErrors) {
                        return instances.stream().findAny()
                                .map(instance -> {
                                    var healthInfo = instance.getHealthInfo();
                                    var launchSpec = instance.getLaunchSpecification();
                                    var error = healthInfo != null ?
                                            healthInfo.getErrorLog() : "No health info";
                                    return getHealthDto(launchSpec.getGpu(),
                                                        launchSpec.getInstanceType(),
                                                        instance.getCloudProvider(),
                                                        error);
                                })
                                .orElse(null);
                    }
                    return null;
                })
                .filter(Objects::nonNull)
                .collect(Collectors.toSet());
    }

    public static HealthDto getHealthDto(
            String gpu,
            String instanceType,
            String backend,
            String error) {
        return HealthDto.builder()
                .error(error)
                .instanceType(instanceType)
                .gpu(gpu)
                .backend(backend)
                .build();
    }

    public static HealthDto getHealthDto(
            GpuSpecUdt gpuSpec,
            String errorMessage) {
        return CommonRoutineService.getHealthDto(gpuSpec.getGpu(),
                                                 gpuSpec.getInstanceType(),
                                                 gpuSpec.getBackend(),
                                                 errorMessage);
    }

    public static String getErrorMessage(Set<HealthDto> healthDtos) {
        var builder = new StringBuilder();
        if (healthDtos.isEmpty()) {
            return builder.append("No health information available").toString();
        }

        healthDtos.forEach(healthDto -> {
            if (!builder.isEmpty()) {
                builder.append("; ");
            }
            builder.append(healthDto.error());
        });
        return builder.toString();
    }

    // Collect health info of all the instances.
    public static String getErrorMessage(Collection<InstanceRequest> instanceRequests) {
        if (CollectionUtils.isEmpty(instanceRequests)) {
            return "No error logs available";
        }

        var healthInfos = instanceRequests.stream()
                .map(CommonRoutineService::getHealthInfo)
                .toList();
        return StringUtils.join(healthInfos, ";");
    }

    private static String getHealthInfo(InstanceRequest instanceRequest) {
        var errorMessageBuilder = getErrorMessageStringBuilder(instanceRequest);
        var healthInfo = instanceRequest.getHealthInfo();
        if ((healthInfo != null) && isNotBlank(healthInfo.getErrorLog())) {
            errorMessageBuilder.append("Error Message - ").append(healthInfo.getErrorLog());
        } else {
            errorMessageBuilder.append(MESG_HEALTH_INFO_UNAVAILABLE);
        }
        return errorMessageBuilder.toString();
    }

    @NotNull
    private static StringBuilder getErrorMessageStringBuilder(InstanceRequest instanceRequest) {
        var icmsRequesId = instanceRequest.getInstanceRequestId();
        var instanceId = isNotBlank(instanceRequest.getInstanceId()) ?
                instanceRequest.getInstanceId() : UNKNOWN;
        var requestState = instanceRequest.getState() != null ?
                                        instanceRequest.getState() : UNKNOWN;
        var instanceState = instanceRequest.getInstanceState() != null ?
                instanceRequest.getInstanceState().getName() : UNKNOWN;
        return new StringBuilder(MESG_ICMS_REQUEST_INSTANCE_DETAILS.formatted(icmsRequesId,
                                                                              requestState,
                                                                              instanceId,
                                                                              instanceState));
    }

}
