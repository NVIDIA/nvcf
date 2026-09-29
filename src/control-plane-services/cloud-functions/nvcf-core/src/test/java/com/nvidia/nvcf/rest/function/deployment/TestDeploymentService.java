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

import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_CHART;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCES;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;

import com.nvidia.nvcf.persistence.function.DeploymentBatchWriter;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentKey;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.service.function.AutoscalingConfigurationMapper;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import java.net.URI;
import java.nio.ByteBuffer;
import java.time.Instant;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TestDeploymentService {

    @Autowired
    private AutoscalingConfigurationMapper autoscalingConfigurationMapper;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private FunctionMapperService functionMapperService;

    @Autowired
    private DeploymentBatchWriter deploymentBatchWriter;

    @Autowired
    private FunctionDeploymentService deploymentService;

    public FunctionDeploymentDto getFunctionDeployment(
            String ncaId, UUID functionId, UUID functionVersionId) {
        return deploymentService.getFunctionDeployment(
                ncaId, functionId, functionVersionId, x -> true);
    }

    public FunctionEntity createTestFunctionEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name) {
        var entity = createTestFunctionEntity(id, versionId, ncaId, name, FunctionStatus.INACTIVE, null);
        return saveFunctionModels(entity, defaultModels(versionId));
    }

    public FunctionEntity createTestFunctionEntityWithModel(
            UUID id,
            UUID versionId,
            String ncaId,
            String name,
            List<FunctionModelDto> models) {
        var entity = createTestFunctionEntity(id, versionId, ncaId, name, FunctionStatus.INACTIVE, null);
        return saveFunctionModels(entity, models);
    }

    public FunctionEntity createTestFunctionEntityWithModelAndResource(
            UUID id,
            UUID versionId,
            String ncaId,
            String name) {
        var entity = createTestFunctionEntity(id, versionId, ncaId, name, FunctionStatus.INACTIVE,
                                              TEST_RESOURCES);
        return saveFunctionModels(entity, defaultModels(versionId));
    }

    public FunctionEntity createTestFunctionEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name,
            FunctionStatus status) {
        return createTestFunctionEntity(id, versionId, ncaId, name, status, TEST_RESOURCES);
    }

    public FunctionEntity createTestFunctionEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name,
            FunctionStatus status,
            Set<ResourceUdt> resources) {
        var entity = FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(status)
                .ncaId(ncaId)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .resources(resources)
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .createdAt(Instant.now())
                .build();
        functionsRepository.save(entity);
        return entity;
    }

    private FunctionEntity saveFunctionModels(
            FunctionEntity entity, List<FunctionModelDto> models) {
        if (models == null) {
            return entity;
        }
        entity.setModelSpecs(functionMapperService.toModelSpecs(models));
        return functionsRepository.save(entity);
    }

    /** Default two-model list used by most test helpers. */
    public static List<FunctionModelDto>
    defaultModels(UUID versionId) {
        return List.of(
                FunctionModelDto.builder()
                        .name("model-1")
                        .version("1.0")
                        .uri(URI.create(TEST_MODEL_URL_1))
                        .build(),
                FunctionModelDto.builder()
                        .name("model-2")
                        .version("2.0")
                        .uri(URI.create(TEST_MODEL_URL_1))
                        .build());
    }

    /** Single small-model list (mirrors the old TEST_MODEL_SMALL set). */
    public static List<FunctionModelDto> smallModel(UUID versionId) {
        return List.of(
                FunctionModelDto.builder()
                        .name("model-1")
                        .version("1.0")
                        .uri(URI.create(TEST_MODEL_URL_2))
                        .build());
    }

    public FunctionEntity createHelmChartBasedFunctionEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name,
            FunctionStatus status) {
        var entity = FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(status)
                .ncaId(ncaId)
                .helmChart(TEST_NGC_HELM_CHART.toString())
                .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME)
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .createdAt(Instant.now())
                .build();
        functionsRepository.save(entity);
        return entity;
    }

    public FunctionDeploymentEntity createDeploymentEntity(
            UUID id,
            UUID versionId,
            UUID deploymentId,
            String ncaId) {
        return createDeploymentEntity(
                id, versionId, deploymentId, ncaId, GFN, Collections.emptySet());
    }

    public FunctionDeploymentEntity createDeploymentEntity(
            UUID id,
            UUID versionId,
            UUID deploymentId,
            String ncaId,
            UUID gpuSpecId1,
            UUID gpuSpecId2) {
        return createDeploymentEntity(id, versionId, deploymentId, ncaId, GFN,
                                      Collections.emptySet(), gpuSpecId1, gpuSpecId2);
    }

    public FunctionDeploymentEntity createDeploymentEntity(
            UUID id,
            UUID versionId,
            UUID deploymentId,
            String ncaId,
            String backend,
            Set<String> clusters) {
        return createDeploymentEntity(id, versionId, deploymentId, ncaId, backend, clusters,
                                      TEST_GPU_SPEC_ID, TEST_GPU_SPEC_ID_2);
    }

    public FunctionDeploymentEntity createDeploymentEntity(
            UUID id,
            UUID versionId,
            UUID deploymentId,
            String ncaId,
            String backend,
            Set<String> clusters,
            UUID gpuSpecId1,
            UUID gpuSpecId2) {
        var builder1 = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(ncaId)
                             .deploymentId(deploymentId)
                             .gpuSpecificationId(gpuSpecId1)
                             .build())
                .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                .maxInstances(4).minInstances(4).maxRequestConcurrency(9);
        var builder2 = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(ncaId)
                             .deploymentId(deploymentId)
                             .gpuSpecificationId(gpuSpecId2)
                             .build())
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                .maxInstances(5).minInstances(5).maxRequestConcurrency(99);
        if (StringUtils.isNotBlank(backend)) {
            builder1.backend(backend);
            builder2.backend(backend);
        }
        if (!clusters.isEmpty()) {
            builder1.clusters(clusters);
            builder2.clusters(clusters);
        }
        var gpuSpecs = Set.of(builder1.build(), builder2.build());
        return createDeploymentEntity(id, versionId, deploymentId, ncaId, gpuSpecs);
    }

    public FunctionDeploymentEntity createDeploymentEntity(
            UUID id,
            UUID versionId,
            UUID deploymentId,
            String ncaId,
            Set<GpuSpecificationEntity> gpuSpecs) {
        return createDeploymentEntity(id, versionId, deploymentId, ncaId, gpuSpecs, Instant.now());
    }

    public FunctionDeploymentEntity createDeploymentEntity(
            UUID id,
            UUID versionId,
            UUID deploymentId,
            String ncaId,
            Set<GpuSpecificationEntity> gpuSpecs,
            Instant createdAt) {
        return createDeploymentEntity(
                id, versionId, deploymentId, ncaId, gpuSpecs, Map.of(), createdAt);
    }

    public FunctionDeploymentEntity createDeploymentEntity(
            UUID id,
            UUID versionId,
            UUID deploymentId,
            String ncaId,
            Set<GpuSpecificationEntity> gpuSpecs,
            Map<UUID, ByteBuffer> autoscalerConfig) {
        return createDeploymentEntity(
                id, versionId, deploymentId, ncaId, gpuSpecs, autoscalerConfig, Instant.now());
    }

    public FunctionDeploymentEntity createDeploymentEntity(
            UUID id,
            UUID versionId,
            UUID deploymentId,
            String ncaId,
            Set<GpuSpecificationEntity> gpuSpecs,
            Map<UUID, ByteBuffer> autoscalerConfig,
            Instant createdAt) {
        var deploymentEntity = FunctionDeploymentEntity.builder()
                .key(FunctionDeploymentKey.builder().functionVersionId(versionId).build())
                .deploymentId(deploymentId)
                .functionId(id)
                .ncaId(ncaId)
                .createdAt(createdAt)
                .lastUpdatedAt(Instant.now())
                .build();
        List<GpuSpecificationEntity> gpuSpecList;
        if (gpuSpecs == null || gpuSpecs.isEmpty()) {
            gpuSpecList = List.of();
        } else if (autoscalerConfig == null || autoscalerConfig.isEmpty()) {
            gpuSpecList = List.copyOf(gpuSpecs);
        } else {
            gpuSpecList = gpuSpecs.stream().map(e -> {
                ByteBuffer buf = autoscalerConfig.get(e.getKey().getGpuSpecificationId());
                return buf != null ? e.toBuilder().autoscalingConfig(
                                autoscalingConfigurationMapper.toAutoscalingConfigurationJson(buf))
                        .build() : e;
            }).toList();
        }
        deploymentBatchWriter.createDeployment(
                new FunctionDeploymentContext(deploymentEntity, gpuSpecList));
        return deploymentEntity;
    }
}
