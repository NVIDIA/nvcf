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

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.GetActiveInstanceInfoResponse;
import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.OverrideBillingRequest;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.createInstances.CreateInstanceService;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import java.util.Map;
import java.util.Set;

import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@Slf4j
@AllArgsConstructor
/**
 * This class is invoked by InstanceController for instance-related operations.
 * It decides the cloud provider service and invokes the respective service.
 */
public class InstanceService {

    private final DescribeAndCancelInstanceService describeAndCancelInstanceService;
    private final CancelInstanceService cancelInstanceService;
    private final TerminateInstanceService terminateInstanceService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final FunctionBillingService functionBillingService;
    private final InstanceInfoService instanceInfoService;
    private final CreateInstanceService createInstanceService;

    public CreateSpotInstancesResponse requestInstances(
            String customer,
            SpotInstanceRequestSchema instanceRequest,
            Map<String, Object> auditProps) {

        return createInstanceService.processInstanceRequest(customer, instanceRequest, auditProps);
    }


    public GetSpotInstanceRequests describeInstanceRequests(
            String customer, Set<String> requestIds,
            Set<String> stateFilter) {

        return describeAndCancelInstanceService.describeInstanceRequests(customer, requestIds, stateFilter);
    }

    public GetSpotInstanceRequests describeAdminInstanceRequests(Set<String> requestIds,
                                                             Set<String> stateFilter) {

        return describeAndCancelInstanceService.describeAdminInstanceRequests(requestIds, stateFilter);
    }

    public GetSpotInstanceRequests describeInstances(
            String customer,
            List<String> instanceIds) {
        return describeAndCancelInstanceService.describeInstances(customer, instanceIds);
    }

    public GetSpotInstanceRequests describeInstancesByDeploymentId(
            @NotNull String ncaId,
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecId,
            boolean includeTerminated,
            boolean expiredAckedInstances) {
        return describeAndCancelInstanceService.describeInstancesByDeploymentId(
                ncaId,
                deploymentId,
                gpuSpecId,
                includeTerminated,
                expiredAckedInstances);
    }


    public TerminateInstancesResponse terminateInstances(
            String customer,
            Set<String> instanceIds,
            Map<String, Object> auditProps) {
        return terminateInstanceService.terminateInstances(customer, instanceIds, auditProps);
    }

    public TerminateInstancesResponse terminateInstances(
            @NotNull String ncaId,
            @Nullable UUID deploymentId,
            @Nullable UUID gpuSpecificationId,
            @NotNull String instanceId,
            @NotNull Map<String, Object> auditProps) {
        return terminateInstanceService.terminateInstances(ncaId, deploymentId, gpuSpecificationId, instanceId, auditProps);
    }

    public TerminateInstancesResponse terminateInstanceRequests(
            String customer,
            Set<String> requestIds,
            Map<String, Object> auditProps) {
        return terminateInstanceService.terminateInstanceRequests(customer, requestIds, auditProps);
    }

    public TerminateInstancesResponse terminateInstanceRequests(
            @NotNull String ncaId,
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecificationId,
            @NotNull String requestId,
            @NotNull Map<String, Object> auditProps) {
        return terminateInstanceService.terminateInstanceRequests(ncaId, deploymentId, gpuSpecificationId, requestId, null, auditProps);
    }

    public TerminateInstancesResponse instanceDeploymentTermination(
            @NotNull String ncaId,
            @NotNull UUID deploymentId,
            @Nullable UUID gpuSpecificationId,
            @NotNull Map<String, Object> auditProps) {
        return terminateInstanceService.instanceDeploymentTermination(ncaId, deploymentId, gpuSpecificationId, auditProps);
    }


    public void cancelInstanceRequests(
            String customer, Set<String> requestIds,
            Map<String, Object> auditProps) {
        cancelInstanceService.cancelInstanceRequests(customer, requestIds, auditProps);
    }

    public void overrideBilling(OverrideBillingRequest request) {
        functionBillingService.addFunctionBillingOverride(request);
    }

    public GetActiveInstanceInfoResponse getActiveInstancesForZone(String zoneName) {
        if (icmsConfigurationProperties.isInstanceListingApiEnabled()) {
            return instanceInfoService.getActiveInstancesForZone(zoneName);
        }
        String errMsg = "GET /v1/si/clusters/{cluster-id/zone-name}/instances is not available";
        log.error(errMsg);
        throw new PreConditionFailedException(errMsg);
    }


}
