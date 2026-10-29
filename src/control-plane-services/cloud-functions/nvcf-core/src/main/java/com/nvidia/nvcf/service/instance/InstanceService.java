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
package com.nvidia.nvcf.service.instance;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService.GetInstancesResponse.InstanceRequest;
import com.nvidia.nvcf.icms.client.IcmsStubService.Instance;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.rest.function.management.dto.InstanceDto;
import com.nvidia.nvcf.rest.function.management.dto.InstanceStatusEnum;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class InstanceService {

    private static final String MESG_INSTANCE_BY_ACC_FUNC_STATUS =
            "{} active instances for account '{}' and function '{}'";

    private static final String MESG_SKIP_UNKNOWN_DEPLOYMENT =
            "Function Version id '{}', Deployment id '{}', Delete by deploymentId returned " +
                    "NOT_FOUND, skipping";
    private final IcmsClient icmsClient;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;

    public List<InstanceRequest> getInstanceRequests(String ncaId,
                                                     UUID functionId,
                                                     UUID functionVersionId) {
        log.debug(MESG_INSTANCE_BY_ACC_FUNC_STATUS, "Retrieving", ncaId, functionId);
        var deploymentContextOpt =
                functionDeploymentLookupService.getDeploymentContextByVersionId(
                        functionVersionId);
        if (deploymentContextOpt.isEmpty()) {
            // Deployment not found, no instances could be retrieved, returning empty list.
            return List.of();
        }
        var deploymentContext = deploymentContextOpt.get();
        var deploymentId = deploymentContext.deployment().getDeploymentId();
        var instances = icmsClient.getInstancesByDeploymentId(ncaId, deploymentId);
        Map<UUID, String> gpuByGpuSpecId = gpuByGpuSpecId(deploymentContext.gpuSpecs());
        // ICMS endpoint response does not contain gpu field. We will take gpu
        // from gpu specification.
        // TODO add gpu field to ICMS response
        var instanceRequests = toInstanceRequests(instances, gpuByGpuSpecId);
        log.debug(MESG_INSTANCE_BY_ACC_FUNC_STATUS, "Retrieved", ncaId, functionId);
        return instanceRequests;
    }

    private static List<InstanceRequest> toInstanceRequests(
            List<Instance> instances,
            Map<UUID, String> gpuByGpuSpecId) {
        if (instances == null) {
            return List.of();
        }
        return instances.stream()
                .map(instance -> toInstanceRequest(
                        instance, gpuByGpuSpecId.get(instance.getGpuSpecificationId())))
                .toList();
    }

    private static Map<UUID, String> gpuByGpuSpecId(List<GpuSpecificationEntity> gpuSpecs) {
        return gpuSpecs.stream()
                .collect(Collectors.toMap(
                        spec -> spec.getKey().getGpuSpecificationId(),
                        GpuSpecificationEntity::getGpu));
    }

    private static InstanceRequest toInstanceRequest(Instance instance, String gpu) {
        return InstanceRequest.builder()
                .createTime(instance.getCreateTime())
                .instanceId(instance.getInstanceId())
                .launchSpecification(InstanceRequest.LaunchSpecification.builder()
                                             .instanceType(instance.getInstanceType())
                                             .containerImage(instance.getContainerImage())
                                             .placement(instance.getPlacement())
                                             .gpu(Objects.nonNull(gpu) ? gpu :
                                                          instance.getInstanceType())
                                             .build())
                .launchedAvailabilityZone(toAvailabilityZone(instance))
                .instanceRequestId(toRequestId(instance.getLaunchRequestId()))
                .state(toRequestState(instance.getState()))
                .status(toStatus(instance))
                .cloudProvider(instance.getCloudProvider())
                .instanceState(instance.getState())
                .healthInfo(instance.getHealthInfo())
                .build();
    }

    private static String toAvailabilityZone(Instance instance) {
        var placement = instance.getPlacement();
        return placement != null ? placement.getAvailabilityZone() : null;
    }

    private static UUID toRequestId(String launchRequestId) {
        return StringUtils.isNotBlank(launchRequestId)
                ? UUID.fromString(launchRequestId) : null;
    }

    private static InstanceRequest.State toRequestState(
            InstanceRequest.InstanceState instanceState) {
        if (instanceState == null) {
            return null;
        }
        if (instanceState.isStartingOrRunning()) {
            return InstanceRequest.State.ACTIVE;
        }
        var stateName = instanceState.getName();
        if ("terminated".equalsIgnoreCase(stateName)) {
            return InstanceRequest.State.CLOSED;
        }
        if ("shutting-down".equalsIgnoreCase(stateName)) {
            return InstanceRequest.State.CANCELED;
        }
        return null;
    }

    private static InstanceRequest.Status toStatus(Instance instance) {
        var instanceState = instance.getState();
        if (instanceState == null) {
            return null;
        }
        return InstanceRequest.Status.builder()
                .code(Integer.toString(instanceState.getCode()))
                .message(instanceState.getName())
                .updateTime(instance.getCreateTime())
                .build();
    }

    public List<InstanceDto> getActiveInstancesForFunction(
            String ncaId,
            UUID functionId,
            UUID functionVersionId) {
        return getInstanceRequests(ncaId, functionId,
                                   functionVersionId).stream()
                .filter(instance -> instance.getInstanceId() != null)
                .filter(instance -> instance.getInstanceState() != null
                        && instance.getInstanceState().isStartingOrRunning())
                .map(instance -> InstanceDto.builder()
                        .instanceId(instance.getInstanceId())
                        .functionId(functionId)
                        .functionVersionId(functionVersionId)
                        .instanceType(instance.getLaunchSpecification().getInstanceType())
                        .instanceStatus(InstanceStatusEnum
                                                .fromText(instance.getInstanceState().getName()))
                        .icmsRequestId(instance.getInstanceRequestId())
                        .ncaId(ncaId)
                        .gpu(instance.getLaunchSpecification().getGpu())
                        .backend(instance.getCloudProvider())
                        .location(instance.getLaunchedAvailabilityZone())
                        .instanceCreatedAt(instance.getCreateTime())
                        .instanceUpdatedAt(instance.getStatus().getUpdateTime())
                        .build())
                .toList();
    }

    public void deleteInstances(
            String ncaId,
            UUID functionVersionId,
            UUID deploymentId) {
        try {
            icmsClient.deleteInstancesByDeploymentId(ncaId, deploymentId);
        } catch (NotFoundException e) {
            // ignored. if the deployment id is not valid it's just as good as deleted.
            log.warn(MESG_SKIP_UNKNOWN_DEPLOYMENT, functionVersionId, deploymentId);
        }
    }
}
