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
package com.nvidia.nvcf.service.registry;

import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.CONTAINER;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.HELM;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.MODEL;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.RESOURCE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.when;

import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import java.net.URI;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import tools.jackson.databind.json.JsonMapper;

@ExtendWith(MockitoExtension.class)
class RegistryCredentialFunctionServiceTest {

    @Mock
    private JsonMapper jsonMapper;

    @Mock
    private FunctionLookupService functionLookupService;

    @Mock
    private FunctionMapperService functionMapperService;

    @Mock
    private RegistryFunctionMapperService registryFunctionMapperService;

    @Mock
    private RegistryCredentialLookupService registryCredentialLookupService;

    @Mock
    private RegistryMapperService registryMapperService;

    private RegistryCredentialFunctionService createService() {
        return new RegistryCredentialFunctionService(
                jsonMapper,
                functionMapperService,
                functionLookupService,
                registryFunctionMapperService,
                registryCredentialLookupService,
                registryMapperService,
                "test-pull-secret",
                "test.sidecar.hostname");
    }

    private static FunctionEntity containerFunction(String containerImage) {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .functionName("test-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v2/models/test/infer")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .containerImage(containerImage)
                .build();
    }

    private static FunctionEntity helmFunction(String helmChart) {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .functionName("test-helm-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v2/models/test/infer")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .helmChart(helmChart)
                .build();
    }

    private static FunctionEntity modelFunction(String modelUrl) {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .functionName("test-model-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v2/models/test/infer")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .containerImage("docker.io/library/nginx:latest")
                .modelSpecs(Map.of("test-model", "{\"version\":\"1.0\"}"))
                .build();
    }

    private static FunctionEntity functionWithoutModelSpecs() {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .functionName("test-legacy-model-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v2/models/test/infer")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .containerImage("docker.io/library/nginx:latest")
                .build();
    }

    private static FunctionModelDto modelEntity(UUID functionVersionId, String modelUrl) {
        return FunctionModelDto.builder()
                .name("test-model")
                .version("1.0")
                .uri(URI.create(modelUrl))
                .build();
    }

    private static FunctionEntity resourceFunction(String resourceUrl) {
        return FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-nca-id")
                .functionName("test-resource-function")
                .functionStatus(FunctionStatus.ACTIVE)
                .inferenceUrl("/v2/models/test/infer")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .containerImage("docker.io/library/nginx:latest")
                .resources(Set.of(ResourceUdt.builder()
                        .name("test-resource")
                        .version("1.0")
                        .url(resourceUrl)
                        .build()))
                .build();
    }

    // --- Container credential resolution ---

    @Test
    void containerCredentialsEmptyReturnsEmptyList() {
        var service = createService();
        var function = containerFunction("registry.example.com/image:latest");

        when(registryCredentialLookupService.getRegistryCredentialDtos(
                "test-nca-id", "registry.example.com", CONTAINER))
                .thenReturn(Collections.emptyList());

        var result = service.getContainerRegistryCredentialDetails(function);
        assertThat(result).isEmpty();
    }

    // --- Helm credential resolution ---

    @Test
    void helmCredentialsEmptyReturnsEmptyList() {
        var service = createService();
        var function = helmFunction("oci://registry.example.com/charts/mychart:1.0");

        when(registryCredentialLookupService.getRegistryCredentialDtos(
                "test-nca-id", "registry.example.com", HELM))
                .thenReturn(Collections.emptyList());

        var result = service.getHelmRegistryCredentialDetails(function);
        assertThat(result).isEmpty();
    }

    // --- Model credential resolution ---

    @Test
    void modelCredentialsEmptyThrowsIllegalState() {
        var service = createService();
        var function = modelFunction("https://registry.example.com/models/test:v1");
        var modelEntities = List.of(
                modelEntity(function.getFunctionVersionId(), "https://registry.example.com/models/test:v1"));

        when(functionMapperService.toFunctionModels(function.getModelSpecs()))
                .thenReturn(modelEntities);
        when(registryCredentialLookupService.getRegistryCredentialDtos(
                "test-nca-id", "registry.example.com", MODEL))
                .thenReturn(Collections.emptyList());

        assertThatThrownBy(() -> service.getModelRegistryCredentialsMap(function))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("Missing MODEL registry");
    }

    @Test
    void modelCredentialsReturnEmptyWhenModelSpecsAreMissing() {
        var service = createService();
        var function = functionWithoutModelSpecs();

        assertThat(service.getModelRegistryCredentialsMap(function)).isEmpty();
    }

    // --- Resource credential resolution ---

    @Test
    void resourceCredentialsEmptyThrowsIllegalState() {
        var service = createService();
        var function = resourceFunction("https://registry.example.com/resources/test:v1");

        when(registryCredentialLookupService.getRegistryCredentialDtos(
                "test-nca-id", "registry.example.com", RESOURCE))
                .thenReturn(Collections.emptyList());

        assertThatThrownBy(() -> service.getResourceRegistryCredentialsMap(function))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("Missing RESOURCE registry");
    }
}
