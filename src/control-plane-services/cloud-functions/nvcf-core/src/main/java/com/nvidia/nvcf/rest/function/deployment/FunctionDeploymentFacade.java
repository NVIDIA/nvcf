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
package com.nvidia.nvcf.rest.function.deployment;

import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.privateFunctionMatch;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.ListDeploymentsResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateFunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.service.function.FunctionAuditService;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.function.GpuSpecificationService;
import com.nvidia.nvcf.util.NvcfUtils;
import jakarta.servlet.http.HttpServletRequest;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionDeploymentFacade {

    private final FunctionAuditService functionAuditService;
    private final FunctionDeploymentService functionDeploymentService;
    private final GpuSpecificationService gpuSpecificationService;

    public DeploymentResponse createFunctionDeployment(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            FunctionDeploymentRequest deploymentRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var dto = functionDeploymentService.createFunctionDeployment(
                ncaId,
                functionId,
                functionVersionId,
                deploymentRequest,
                payloadBuilder,
                function -> privateFunctionMatch(ncaId, authentication, function));
        return new DeploymentResponse(dto);
    }

    public FunctionResponse deleteFunctionDeployment(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            boolean graceful,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var dto = functionDeploymentService.deleteFunctionDeployment(
                ncaId,
                functionId,
                functionVersionId,
                graceful,
                payloadBuilder,
                function -> privateFunctionMatch(ncaId, authentication, function));
        return new FunctionResponse(dto);
    }

    public DeploymentResponse getFunctionDeployment(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            Authentication authentication) {
        var dto = functionDeploymentService.getFunctionDeployment(
                ncaId,
                functionId,
                functionVersionId,
                function -> privateFunctionMatch(ncaId, authentication, function));
        return new DeploymentResponse(dto);
    }

    public DeploymentResponse getFunctionDeployment(
            String ncaId,
            UUID deploymentId,
            Authentication authentication) {
        var dto = functionDeploymentService.getFunctionDeployment(
                ncaId,
                deploymentId,
                function -> privateFunctionMatch(ncaId, authentication, function));
        return new DeploymentResponse(dto);
    }

    public ListDeploymentsResponse getAllFunctionDeployments(
            String ncaId,
            Authentication authentication) {
        var dtos = functionDeploymentService.getAllFunctionDeployments(
                ncaId,
                function -> privateFunctionMatch(ncaId, authentication, function));
        return new ListDeploymentsResponse(dtos);
    }

    public DeploymentResponse updateFunctionDeployment(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            UpdateFunctionDeploymentRequest updateDeploymentRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var dto = functionDeploymentService.updateFunctionDeployment(
                ncaId,
                functionId,
                functionVersionId,
                updateDeploymentRequest,
                payloadBuilder,
                function -> privateFunctionMatch(ncaId, authentication, function));
        return new DeploymentResponse(dto);
    }

    public UpdateGpuSpecificationResponse updateGpuSpecification(
            FunctionDeploymentEntity deploymentEntity,
            UUID gpuSpecificationId,
            UpdateGpuSpecificationRequest updateGpuSpecRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var ncaId = deploymentEntity.getNcaId();
        var dto = gpuSpecificationService.updateGpuSpecification(
                deploymentEntity,
                gpuSpecificationId,
                updateGpuSpecRequest,
                payloadBuilder,
                function -> privateFunctionMatch(ncaId, authentication, function));
        return new UpdateGpuSpecificationResponse(dto);
    }

    private AuditEventPayload.Builder auditEventPayloadBuilder(
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var customProperties = NvcfUtils.getCustomProperties(httpServletRequest);
        return functionAuditService.auditEventPayloadBuilder(authentication, customProperties);
    }
}
