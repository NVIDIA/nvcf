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

import static com.nvidia.nvcf.util.NvcfUtils.filterBlankStrings;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.nvidia.nvcf.icms.client.IcmsStubService;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;

/**
 * Mapper service for function deployment and GPU specification entities/DTOs and
 * autoscaling configuration. Builds {@link GpuSpecificationEntity} and {@link GpuSpecificationDto}.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionDeploymentMapperService {

    private final JsonMapper jsonMapper;
    private final AutoscalingConfigurationMapper autoscalingConfigurationMapper;
    private final HelmValidationPolicyMapperService helmValidationPolicyMapperService;

    /**
     * Build list of GPU specification entities from create request. Use for batch writer
     * and for logic that needs GPU spec data. Does not read from deployment entity.
     */
    public List<GpuSpecificationEntity> toGpuSpecificationEntities(
            UUID deploymentId,
            String ncaId,
            Map<UUID, GpuSpecificationDto> gpuSpecDtoMap) {
        if (CollectionUtils.isEmpty(gpuSpecDtoMap)) {
            return List.of();
        }
        var ncaIdSafe = ncaId != null ? ncaId : "";
        return gpuSpecDtoMap.entrySet().stream()
                .map(dto -> toGpuSpecificationEntity(
                        dto.getValue(), dto.getKey(), deploymentId, ncaIdSafe))
                .toList();
    }


    /**
     * Build GPU specification entity from DTO (e.g. for create flow).
     */
    public GpuSpecificationEntity toGpuSpecificationEntity(
            GpuSpecificationDto dto,
            UUID gpuSpecificationId,
            UUID deploymentId,
            String ncaId) {
        var builder = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                        .ncaId(ncaId)
                        .deploymentId(deploymentId)
                        .gpuSpecificationId(gpuSpecificationId)
                        .build())
                .gpu(dto.gpu())
                .instanceType(dto.instanceType())
                .minInstances(dto.minInstances())
                .maxInstances(dto.maxInstances())
                .maxRequestConcurrency(
                        Objects.requireNonNullElse(dto.maxRequestConcurrency(), 1))
                .preferredOrder(Objects.requireNonNullElse(dto.preferredOrder(), 1));

        if (isNotBlank(dto.backend())) {
            builder.backend(dto.backend());
        }
        if (dto.configuration() != null) {
            var jsonStr = dto.configuration().toString();
            builder.configuration(
                    Base64.getEncoder().encodeToString(jsonStr.getBytes(StandardCharsets.UTF_8)));
        }
        if (!CollectionUtils.isEmpty(dto.availabilityZones())) {
            var zones = filterBlankStrings(dto.availabilityZones());
            if (!zones.isEmpty()) {
                builder.availabilityZones(zones);
            }
        }
        if (!CollectionUtils.isEmpty(dto.clusters())) {
            var clusters = filterBlankStrings(dto.clusters());
            if (!clusters.isEmpty()) {
                builder.clusters(clusters);
            }
        }
        if (!CollectionUtils.isEmpty(dto.regions())) {
            var regions = filterBlankStrings(dto.regions());
            if (!regions.isEmpty()) {
                builder.regions(regions);
            }
        }
        if (!CollectionUtils.isEmpty(dto.attributes())) {
            var attributes = filterBlankStrings(dto.attributes());
            if (!attributes.isEmpty()) {
                builder.attributes(attributes);
            }
        }
        if (dto.autoscalingConfiguration() != null) {
            builder.autoscalingConfig(
                    autoscalingConfigurationMapper.toAutoscalingConfigurationJson(
                            dto.autoscalingConfiguration()));
        }
        if (dto.helmValidationPolicy() != null) {
            builder.helmValidationPolicy(
                    helmValidationPolicyMapperService.toHelmValidationPolicyJson(
                            dto.helmValidationPolicy()));
        }
        return builder.build();
    }

    /**
     * Convert GPU specification entity to DTO.
     */
    @SneakyThrows
    public GpuSpecificationDto toGpuSpecificationDto(
            GpuSpecificationEntity entity,
            Map<String, Set<IcmsStubService.InstanceTypeDetails>> instanceTypes) {
        var builder = GpuSpecificationDto.builder()
                .gpuSpecificationId(entity.getKey().getGpuSpecificationId())
                .backend(entity.getBackend())
                .gpu(entity.getGpu())
                .maxInstances(entity.getMaxInstances())
                .minInstances(entity.getMinInstances())
                .maxRequestConcurrency(
                        Objects.requireNonNullElse(entity.getMaxRequestConcurrency(), 1))
                .instanceType(
                        isNotBlank(entity.getInstanceType()) ? entity.getInstanceType() : "default")
                .preferredOrder(Objects.requireNonNullElse(entity.getPreferredOrder(), 1));

        if (isNotBlank(entity.getConfiguration())) {
            var jsonStr = Base64.getDecoder().decode(entity.getConfiguration());
            var objectNode = jsonMapper.readValue(jsonStr, ObjectNode.class);
            builder.configuration(objectNode);
        }

        var zones = filterBlankStrings(entity.getAvailabilityZones());
        if (!CollectionUtils.isEmpty(zones)) {
            builder.availabilityZones(zones.stream().toList());
        }

        var clusters = filterBlankStrings(entity.getClusters());
        if (!CollectionUtils.isEmpty(clusters)) {
            builder.clusters(clusters);
        }

        var regions = filterBlankStrings(entity.getRegions());
        if (!CollectionUtils.isEmpty(regions)) {
            builder.regions(regions);
        }

        var attributes = filterBlankStrings(entity.getAttributes());
        if (!CollectionUtils.isEmpty(attributes)) {
            builder.attributes(attributes);
        }

        if (instanceTypes != null && instanceTypes.containsKey(entity.getGpu())) {
            instanceTypes.get(entity.getGpu()).stream()
                    .filter(d -> Objects.equals(entity.getInstanceType(), d.getName()))
                    .findAny()
                    .ifPresent(d -> {
                        builder.cpuArch(d.getCpuArch());
                        builder.os(d.getOs());
                        builder.driverVersion(d.getDriverVersion());
                        builder.storage(d.getStorage());
                        builder.systemMemory(d.getSystemMemory());
                        builder.gpuMemory(d.getGpuMemory());
                    });
        }

        if (StringUtils.isNotBlank(entity.getAutoscalingConfig())) {
            builder.autoscalingConfiguration(
                    autoscalingConfigurationMapper
                            .toAutoscalingConfigurationDto(entity.getAutoscalingConfig()));
        }

        if (StringUtils.isNotBlank(entity.getHelmValidationPolicy())) {
            builder.helmValidationPolicy(
                    helmValidationPolicyMapperService
                            .toHelmValidationPolicyDto(entity.getHelmValidationPolicy()));
        }

        return builder.build();
    }
}
