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

import static com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationPolicyEnum.CUSTOM_CONFIGURATION;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.google.common.collect.Sets;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse.ClusterGroup;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationPolicyEnum;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.InstanceUsageTypeEnum;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateFunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import com.nvidia.nvcf.service.reval.RevalClient;
import java.util.Collection;
import java.util.Collections;
import java.util.List;
import java.util.Objects;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

/**
 * Centralised deployment and GPU specification validation.
 * Used by {@link FunctionDeploymentService} and {@link GpuSpecificationService}.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class DeploymentValidationService {

    private static final String MESG_BOTH_BACKEND_CLUSTERS =
            "Function id '%s', version '%s': Backend:['%s'] and Clusters:['%s'] could not be "
                    + "specified at the same time.";
    private static final String MESG_GPU_CONFIGURATION_NOT_EMPTY =
            "Function id '%s', version '%s': The configuration field in Gpu specification "
                    + "should be empty for container based functions.";
    private static final String MESG_HELM_VALIDATION_POLICY_NOT_SUPPORTED =
            "Function id '%s', version '%s': The helmValidationPolicy field in Gpu specification "
                    + "should be empty for container based functions.";
    private static final String MESG_INVALID_CLUSTER_GROUP =
            "Function id '%s', version '%s': Invalid Backend '%s' specified";
    private static final String MESG_INVALID_GPU =
            "Function id '%s', version '%s': Invalid GPU '%s' specified";
    private static final String MESG_INVALID_INSTANCE_TYPE =
            "Function id '%s', version '%s': Invalid InstanceType '%s' specified";
    private static final String MESG_MISSING_CLUSTER_GROUPS =
            "Function id '%s', version '%s': No Backends defined for account '%s'";
    private static final String MESG_GPUS_MISSING =
            "Function id '%s', version '%s': GPUs missing for Backend '%s'";
    private static final String MESG_DEPLOYMENT_INVALID_ACCT =
            "Function id '%s', version '%s': Function deployment not found in account '%s'";
    private static final String MESG_GPU_SPEC_ID_NOT_FOUND =
            "Function id '%s', version '%s', deployment '%s': GPU spec id '%s' not found";
    private static final String MESG_EMPTY_UPDATE_REQUEST =
            "Function id '%s', version '%s': Empty update gpu specification request. "
                    + "At least one field should be provided.";
    private static final String MESG_UNKNOWN_GPU_SPEC_ID =
            "Invalid request: Unknown gpu-specification-id '%s'";
    private static final String MESG_MIN_GREATER_THAN_MAX =
            "Invalid request: minInstances '%d' must be lesser than or equal to maxInstances '%d'";
    private static final String MESG_SPECS_MISMATCH =
            "Function id '%s', version ='%s': GPU / InstanceType specs must match the ones in"
                    + " the original deployment";
    private static final String MESG_MULTIPLE_SPECS_WITHOUT_IDS =
            "Function id '%s', version ='%s': Deployment has multiple GPU specifications. "
                    + "To update deployment use PATCH endpoint.";
    private static final String MESG_BACKEND_MISMATCH =
            "Function id '%s', version '%s': Backend '%s' field does not match original "
                    + "deployment spec: '%s'.";
    private static final String MESG_CLUSTERS_MISMATCH =
            "Function id '%s', version '%s': Provided clusters: '%s' does not match clusters "
                    + "from original spec: '%s'";

    private final IcmsClient icmsClient;
    private final RevalClient revalClient;
    private final AutoscalingConfigurationMapper autoscalingConfigurationMapper;

    public void validateDeploymentRequest(
            String ncaId,
            FunctionEntity function,
            FunctionDeploymentRequest request) {
        var usage = getFunctionContainerType(function);
        var clusterGroups = icmsClient.getClusterGroups(ncaId, usage);
        var functionId = function.getFunctionId();
        var functionVersionId = function.getFunctionVersionId();

        if (CollectionUtils.isEmpty(clusterGroups)) {
            var mesg = MESG_MISSING_CLUSTER_GROUPS.formatted(
                    functionId, functionVersionId, ncaId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        request.deploymentSpecifications().forEach(spec -> {
            List<ClusterGroup.Gpu> gpus;
            if (isNotBlank(spec.backend())) {
                if (spec.clusters() != null && !spec.clusters().isEmpty()) {
                    var mesg = MESG_BOTH_BACKEND_CLUSTERS.formatted(
                            functionId, functionVersionId, spec.backend(), spec.clusters());
                    log.error(mesg);
                    throw new BadRequestException(mesg);
                }
                var clusterGroup = clusterGroups.stream()
                        .filter(cg -> cg.getName().equalsIgnoreCase(spec.backend()))
                        .findAny()
                        .orElseThrow(() -> {
                            var m = MESG_INVALID_CLUSTER_GROUP.formatted(
                                    functionId, functionVersionId, spec.backend());
                            log.error(m);
                            return new BadRequestException(m);
                        });
                if (CollectionUtils.isEmpty(clusterGroup.getGpus())) {
                    var mesg = MESG_GPUS_MISSING.formatted(
                            functionId, functionVersionId, spec.backend());
                    log.error(mesg);
                    throw new BadRequestException(mesg);
                }
                gpus = clusterGroup.getGpus().stream()
                        .filter(gpu -> gpu.getName().equalsIgnoreCase(spec.gpu()))
                        .toList();
            } else {
                gpus = clusterGroups.stream()
                        .map(ClusterGroup::getGpus)
                        .flatMap(Collection::stream)
                        .filter(gpu -> gpu.getName().equalsIgnoreCase(spec.gpu()))
                        .toList();
            }

            if (CollectionUtils.isEmpty(gpus)) {
                var mesg = MESG_INVALID_GPU.formatted(functionId, functionVersionId, spec.gpu());
                log.error(mesg);
                throw new BadRequestException(mesg);
            }

            var instanceType = spec.instanceType();
            if (isNotBlank(instanceType)
                    && gpus.stream()
                    .map(ClusterGroup.Gpu::getInstanceTypes)
                    .flatMap(Collection::stream)
                    .noneMatch(it -> it.getName().equalsIgnoreCase(instanceType))) {
                var mesg = MESG_INVALID_INSTANCE_TYPE.formatted(
                        functionId, functionVersionId, spec.instanceType());
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        });

        var helmChart = function.getHelmChart();
        if (isNotBlank(helmChart)) {
            validateDeploymentRequestHelmChart(
                    ncaId, function, request.deploymentSpecifications());
        } else {
            validateDeploymentRequestContainer(
                    function, request.deploymentSpecifications());
        }
    }

    public void validateDeploymentRequestHelmChart(
            String ncaId,
            FunctionEntity functionEntity,
            List<GpuSpecificationDto> deploymentSpecifications) {
        for (var spec : deploymentSpecifications) {
            String validationErrorMsg;
            try {
                validationErrorMsg = revalClient.validate(ncaId, functionEntity, spec);
            } catch (Exception e) {
                log.error(e.getMessage());
                throw new BadRequestException(e.getMessage(), e);
            }
            if (isNotBlank(validationErrorMsg)) {
                log.error(validationErrorMsg);
                throw new BadRequestException(validationErrorMsg);
            }
        }
    }

    private void validateDeploymentRequestContainer(
            FunctionEntity function,
            List<GpuSpecificationDto> deploymentSpecifications) {
        var functionId = function.getFunctionId();
        var functionVersionId = function.getFunctionVersionId();

        for (var gpuSpec : deploymentSpecifications) {
            if (gpuSpec.configuration() != null
                    && isNotBlank(gpuSpec.configuration().toString())) {
                var mesg = MESG_GPU_CONFIGURATION_NOT_EMPTY
                        .formatted(functionId, functionVersionId);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
            if (gpuSpec.helmValidationPolicy() != null) {
                var mesg = MESG_HELM_VALIDATION_POLICY_NOT_SUPPORTED
                        .formatted(functionId, functionVersionId);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        }
    }

    public void validateDeployment(FunctionDeploymentEntity deployment, String ncaId) {
        if (!deployment.getNcaId().equals(ncaId)) {
            var mesg = MESG_DEPLOYMENT_INVALID_ACCT.formatted(
                    deployment.getFunctionId(),
                    deployment.getKey().getFunctionVersionId(),
                    ncaId);
            log.debug(mesg);
            throw new NotFoundException(mesg);
        }
    }

    /**
     * Validates and mutates gpuSpecList to match updateDeploymentRequest.
     *
     * @return true if any spec changed
     */
    public boolean validateAndUpdateDeploymentSpecs(
            List<GpuSpecificationEntity> gpuSpecList,
            UpdateFunctionDeploymentRequest updateDeploymentRequest,
            UUID functionId,
            UUID versionId) {
        var dtos = updateDeploymentRequest.deploymentSpecifications();

        if (gpuSpecList.size() != dtos.size()) {
            var mesg = MESG_SPECS_MISMATCH.formatted(functionId, versionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if (dtos.stream().anyMatch(spec -> Objects.isNull(spec.gpuSpecificationId()))) {
            if (dtos.size() > 1) {
                var mesg = MESG_MULTIPLE_SPECS_WITHOUT_IDS.formatted(functionId, versionId);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        }

        boolean hasChanges = false;
        for (var dto : dtos) {
            GpuSpecificationEntity entity;
            if (dto.gpuSpecificationId() != null) {
                entity = gpuSpecList.stream()
                        .filter(e -> e.getKey().getGpuSpecificationId()
                                .equals(dto.gpuSpecificationId()))
                        .findFirst()
                        .orElse(null);
            } else {
                entity = gpuSpecList.isEmpty() ? null : gpuSpecList.get(0);
            }

            if (entity == null) {
                var mesg = MESG_SPECS_MISMATCH.formatted(functionId, versionId);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }

            validateBackend(dto, entity, functionId, versionId);
            validateClusters(dto, entity, functionId, versionId);

            if (!Objects.equals(entity.getMaxInstances(), dto.maxInstances())
                    || !Objects.equals(entity.getMinInstances(), dto.minInstances())
                    || !Objects.equals(entity.getMaxRequestConcurrency(),
                            dto.maxRequestConcurrency())) {
                hasChanges = true;
            }

            entity.setMaxInstances(dto.maxInstances());
            entity.setMinInstances(dto.minInstances());
            if (dto.maxRequestConcurrency() != null) {
                entity.setMaxRequestConcurrency(dto.maxRequestConcurrency());
            }
        }
        return hasChanges;
    }

    public void validateUpdateGpuSpecificationRequest(
            UUID gpuSpecificationId,
            List<GpuSpecificationEntity> gpuSpecList,
            UpdateGpuSpecificationRequest request,
            UUID functionId,
            UUID versionId) {
        if ((request.maxInstances() == null)
                && (request.minInstances() == null)
                && (request.autoscalingConfiguration() == null)
                && (request.autoscalingConfigurationPolicy() == null)) {
            var mesg = MESG_EMPTY_UPDATE_REQUEST.formatted(functionId, versionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var entity = gpuSpecList.stream()
                .filter(e -> e.getKey().getGpuSpecificationId().equals(gpuSpecificationId))
                .findFirst()
                .orElseThrow(() -> new NotFoundException(
                        MESG_UNKNOWN_GPU_SPEC_ID.formatted(gpuSpecificationId)));

        var minInstances =
                Objects.requireNonNullElse(request.minInstances(), entity.getMinInstances());
        var maxInstances =
                Objects.requireNonNullElse(request.maxInstances(), entity.getMaxInstances());
        if (minInstances > maxInstances) {
            var mesg = MESG_MIN_GREATER_THAN_MAX.formatted(minInstances, maxInstances);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    /**
     * Validates and mutates the matching GPU spec in the list. Returns true if any change.
     */
    public boolean validateAndUpdateGpuSpecs(
            List<GpuSpecificationEntity> gpuSpecList,
            UpdateGpuSpecificationRequest request,
            UUID gpuSpecId) {
        var entity = gpuSpecList.stream()
                .filter(e -> e.getKey().getGpuSpecificationId().equals(gpuSpecId))
                .findFirst()
                .orElseThrow(() -> new BadRequestException(
                        MESG_GPU_SPEC_ID_NOT_FOUND.formatted(null, null, null, gpuSpecId)));

        var gpuChanged = updateGpuSpecIfNeeded(entity, request);
        var configChanged = validateAndUpdateAutoscalerConfig(entity, request);
        return gpuChanged || configChanged;
    }

    private static boolean updateGpuSpecIfNeeded(
            GpuSpecificationEntity entity,
            UpdateGpuSpecificationRequest request) {
        boolean hasChanged = false;
        if (request.minInstances() != null
                && !Objects.equals(entity.getMinInstances(), request.minInstances())) {
            hasChanged = true;
            entity.setMinInstances(request.minInstances());
        }
        if (request.maxInstances() != null
                && !Objects.equals(entity.getMaxInstances(), request.maxInstances())) {
            hasChanged = true;
            entity.setMaxInstances(request.maxInstances());
        }
        return hasChanged;
    }

    private boolean validateAndUpdateAutoscalerConfig(
            GpuSpecificationEntity entity,
            UpdateGpuSpecificationRequest updateRequest) {
        var policy = Objects.requireNonNullElse(
                updateRequest.autoscalingConfigurationPolicy(), CUSTOM_CONFIGURATION);

        if (policy == AutoscalingConfigurationPolicyEnum.PLATFORM_CONFIGURATION) {
            if (entity.getAutoscalingConfig() != null) {
                entity.setAutoscalingConfig(null);
                return true;
            }
            return false;
        }

        if (updateRequest.autoscalingConfiguration() != null) {
            var newJson = autoscalingConfigurationMapper.toAutoscalingConfigurationJson(
                    updateRequest.autoscalingConfiguration());
            if (!Objects.equals(entity.getAutoscalingConfig(), newJson)) {
                entity.setAutoscalingConfig(newJson);
                return true;
            }
        }
        return false;
    }

    private static void validateBackend(UpdateGpuSpecificationDto dto,
                                        GpuSpecificationEntity entity,
                                        UUID functionId, UUID versionId) {
        var dtoBackend = StringUtils.isBlank(dto.backend()) ? "" : dto.backend();
        var entityBackend = StringUtils.isBlank(entity.getBackend()) ? "" : entity.getBackend();
        if (!StringUtils.equals(dtoBackend, entityBackend)) {
            var mesg = MESG_BACKEND_MISMATCH.formatted(
                    functionId, versionId, dtoBackend, entity.getBackend());
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private static void validateClusters(UpdateGpuSpecificationDto dto,
                                         GpuSpecificationEntity entity,
                                         UUID functionId, UUID versionId) {
        var entityClusters =
                entity.getClusters() != null ? entity.getClusters() : Collections.emptySet();
        var dtoClusters = dto.clusters() != null ? dto.clusters() : Collections.emptySet();
        if (!Sets.difference(entityClusters, dtoClusters).isEmpty()
                || !Sets.difference(dtoClusters, entityClusters).isEmpty()) {
            var mesg = MESG_CLUSTERS_MISMATCH.formatted(
                    functionId, versionId, dtoClusters, entity.getClusters());
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private static InstanceUsageTypeEnum getFunctionContainerType(FunctionEntity function) {
        return isNotBlank(function.getContainerImage())
                ? InstanceUsageTypeEnum.CONTAINER
                : InstanceUsageTypeEnum.DEFAULT;
    }

}
