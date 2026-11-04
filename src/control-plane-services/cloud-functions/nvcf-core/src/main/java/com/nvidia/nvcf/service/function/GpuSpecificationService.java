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
package com.nvidia.nvcf.service.function;

import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.BUSY_STATUSES;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService.InstanceTypeDetails;
import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.GpuSpecificationsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.InstanceUsageTypeEnum;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.function.Predicate;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.core.env.Environment;
import org.springframework.stereotype.Service;
import tools.jackson.databind.json.JsonMapper;

/**
 * Service for GPU specification validation, updates, and instance-type resolution.
 * Used by {@link FunctionDeploymentService} for create/update deployment flows.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class GpuSpecificationService {

    private static final String MESG_GPU_SPEC_OPERATION =
            "Function id '{}', version '{}', deployment '{}', gpu spec id '{}': {}";
    private static final String MESG_FORBIDDEN_DEPLOYMENT_OPERATION =
            "Function id '%s', version '%s': Forbidden to '%s' deployment";
    private static final String MESG_INVALID_STATUS =
            "Function id '%s', version '%s': Invalid status '%s'";
    private static final String MESG_ERROR_FETCH_INSTANCE_TYPES =
            "Account {}: Error fetching Instance Types from ICMS - {}";

    private final GpuSpecificationsRepository gpuSpecificationRepository;
    private final FunctionsDeploymentRepository deploymentRepository;
    private final DeploymentValidationService deploymentValidationService;
    private final FunctionLookupService functionLookupService;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;
    private final FunctionAuditService functionAuditService;
    private final FunctionDeploymentMapperService functionDeploymentMapperService;
    private final JsonMapper jsonMapper;
    private final IcmsClient icmsClient;
    private final Environment environment;
    
    /**
     * Resolves and sets instance type on the spec if not already set (e.g. for local/mock).
     */
    public void updateInstanceType(
            String ncaId,
            GpuSpecificationEntity spec,
            InstanceUsageTypeEnum instanceUsage) {
        if (java.util.Arrays.stream(environment.getActiveProfiles())
                .allMatch(profile -> profile.startsWith("local"))) {
            if (java.util.Arrays.stream(environment.getActiveProfiles())
                    .noneMatch("local-icms-mock"::equals)) {
                return;
            }
        }
        if (StringUtils.isBlank(spec.getInstanceType())) {
            var instanceType = icmsClient.getDefaultInstanceType(
                    ncaId, spec.getBackend(), spec.getGpu(), instanceUsage);
            spec.setInstanceType(instanceType);
        }
    }

    public GpuSpecificationDto updateGpuSpecification(
            FunctionDeploymentEntity deployment, UUID gpuSpecId,
            UpdateGpuSpecificationRequest request,
            AuditEventPayload.Builder payloadBuilder,
            Predicate<FunctionEntity> privateFunctionMatch) {
        var ncaId = deployment.getNcaId();
        var functionId = deployment.getFunctionId();
        var functionVersionId = deployment.getKey().getFunctionVersionId();

        var deploymentContext = functionDeploymentLookupService.getDeploymentContext(deployment);

        var function = functionLookupService.lookupUsingAccountIdAndFunctionIdAndVersionIdOrThrow(
                ncaId, functionId, functionVersionId);
        if (!privateFunctionMatch.test(function)) {
            var mesg = MESG_FORBIDDEN_DEPLOYMENT_OPERATION
                    .formatted(functionId, functionVersionId, "update");
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
        var status = function.getFunctionStatus();
        if (!BUSY_STATUSES.contains(status)) {
            var mesg = MESG_INVALID_STATUS.formatted(functionId, functionVersionId, status);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var functionJsonBefore = jsonMapper.valueToTree(function);
        var deploymentJsonBefore = jsonMapper.valueToTree(deployment);

        var updatedEntity = validateAndPersist(deploymentContext, gpuSpecId, request);

        functionAuditService.auditUpdateFunctionDeployment(payloadBuilder, functionJsonBefore,
                                                           deploymentJsonBefore, function,
                                                           deployment);
        var instanceTypes = getInstanceTypesQuietly(ncaId, getFunctionContainerType(function));
        return functionDeploymentMapperService.toGpuSpecificationDto(updatedEntity, instanceTypes);
    }

    public Map<String, Set<InstanceTypeDetails>> getInstanceTypesQuietly(
            String ncaId, InstanceUsageTypeEnum instanceUsage) {
        Map<String, Set<InstanceTypeDetails>> instanceTypes = Collections.emptyMap();
        try {
            instanceTypes = icmsClient.getInstanceTypes(ncaId, instanceUsage);
        } catch (Exception e) {
            // Ignore. This is auxiliary data, we don't want to error the whole request. With this
            // exception happened only a few fields in response will be empty.
            log.error(MESG_ERROR_FETCH_INSTANCE_TYPES, ncaId, e.getMessage());
        }
        return instanceTypes;
    }

    /**
     * Validates the request, applies updates to the matching GPU spec in the context,
     * persists deployment and the updated entity. Caller is responsible for audit and building DTO.
     *
     * @return the updated GPU specification entity, or the same entity if no changes were applied
     */
    private GpuSpecificationEntity validateAndPersist(
            FunctionDeploymentContext deploymentContext,
            UUID gpuSpecId,
            UpdateGpuSpecificationRequest request) {
        var deployment = deploymentContext.deployment();
        var functionId = deployment.getFunctionId();
        var functionVersionId = deployment.getKey().getFunctionVersionId();
        var deploymentId = deployment.getDeploymentId();
        var gpuSpecList = new ArrayList<>(deploymentContext.gpuSpecs());

        deploymentValidationService.validateUpdateGpuSpecificationRequest(
                gpuSpecId, gpuSpecList, request, functionId, functionVersionId);

        if (!deploymentValidationService.validateAndUpdateGpuSpecs(
                gpuSpecList, request, gpuSpecId)) {
            return gpuSpecList.stream()
                    .filter(e -> e.getKey().getGpuSpecificationId().equals(gpuSpecId))
                    .findFirst()
                    .orElseThrow();
        }

        log.info(MESG_GPU_SPEC_OPERATION, functionId, functionVersionId,
                 deploymentId, gpuSpecId, "Updating gpu specification");

        var updatedEntity = gpuSpecList.stream()
                .filter(e -> e.getKey().getGpuSpecificationId().equals(gpuSpecId))
                .findFirst()
                .orElseThrow();
        deployment.setLastUpdatedAt(Instant.now());
        deploymentRepository.save(deployment);
        gpuSpecificationRepository.save(updatedEntity);

        log.info(MESG_GPU_SPEC_OPERATION, functionId, functionVersionId,
                 deploymentId, gpuSpecId, "Updated gpu specification");
        return updatedEntity;
    }

    private static InstanceUsageTypeEnum getFunctionContainerType(FunctionEntity function) {
        return isNotBlank(function.getContainerImage())
                ? InstanceUsageTypeEnum.CONTAINER
                : InstanceUsageTypeEnum.DEFAULT;
    }
}
