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
package com.nvidia.nvcf.rest.function.management;

import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_URI;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_DTOS;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_CHART;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCES;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;

import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.HealthUdt;
import com.nvidia.nvcf.persistence.function.entity.Protocol;
import com.nvidia.nvcf.persistence.function.entity.RateLimitUdt;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import java.util.EnumSet;
import java.util.Set;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TestManagementService {

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private FunctionMapperService functionMapperService;


    public FunctionEntity createTestFunctionEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name,
            RateLimitUdt rateLimitUdt) {
        var healthInfo = HealthUdt.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(DEFAULT_HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(Protocol.HTTP)
                .uri(TEST_HEALTH_URI.toString())
                .build();
        var entity = FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(ncaId)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .utilsContainerImage(GO)
                .rateLimit(rateLimitUdt)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(healthInfo)
                .build();

        functionsRepository.save(entity);
        return entity;
    }

    public FunctionEntity createTestFunctionEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name) {
        return createTestFunctionEntity(id, versionId, ncaId, name, null);
    }

    public FunctionEntity createFunctionEntityWithTraits(
            UUID id,
            UUID versionId,
            String ncaId,
            String name,
            EnumSet<Trait> traits) {
        var builder = FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(ncaId)
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .utilsContainerImage(GO);

        if (traits.containsAll(Set.of(Trait.CONTAINER_BASED, Trait.HELM_BASED))) {
            throw new AssertionError("Function cannot be both container and helm based");
        }

        if (traits.contains(Trait.CONTAINER_BASED)) {
            builder = builder.containerArgs(TEST_CONTAINER_ARGS)
                    .containerImage(TEST_NGC_CONTAINER_IMAGE.toString());
        } else if (traits.contains(Trait.HELM_BASED)) {
            builder = builder.helmChart(TEST_NGC_HELM_CHART.toString())
                    .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME);
        }

        if (traits.contains(Trait.USING_RESOURCES)) {
            builder = builder.resources(TEST_RESOURCES);
        }
        if (traits.contains(Trait.USING_MODELS)) {
            builder = builder.modelSpecs(functionMapperService.toModelSpecs(TEST_MODEL_DTOS));
        }
        var entity = builder
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .build();

        functionsRepository.save(entity);
        return entity;
    }

    public enum Trait {
        CONTAINER_BASED,
        HELM_BASED,
        USING_MODELS,
        USING_RESOURCES
    }
}
