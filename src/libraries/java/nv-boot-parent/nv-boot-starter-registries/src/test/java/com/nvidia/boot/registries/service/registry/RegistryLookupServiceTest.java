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

package com.nvidia.boot.registries.service.registry;

import static com.nvidia.boot.registries.util.TestConstants.TEST_CONTAINER_REGISTRY_HOST_NAME_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_ARTIFACT_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_RECOGNIZED_CONTAINER_REGISTRY_KEY_1;
import static com.nvidia.boot.registries.util.TestUtils.registryConfig;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.RecognizedRegistryConfiguration;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import java.util.Collections;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class RegistryLookupServiceTest {

    private RegistryLookupService service;

    @BeforeEach
    void setUp() {
        var recognizedRegistryConfig = new RecognizedRegistryConfiguration();
        recognizedRegistryConfig.setContainer(Map.of(
                TEST_RECOGNIZED_CONTAINER_REGISTRY_KEY_1,
                registryConfig(TEST_CONTAINER_REGISTRY_HOST_NAME_1, true, true)));
        recognizedRegistryConfig.setHelm(Map.of());
        recognizedRegistryConfig.setModel(Map.of());
        recognizedRegistryConfig.setResource(Map.of());

        var registryMapperService = new RegistryMapperService(
                TEST_NGC_CONTAINER_REGISTRY_PROD,
                TEST_NGC_ARTIFACT_REGISTRY_PROD,
                TEST_NGC_HELM_REGISTRY_PROD);

        service = new RegistryLookupService(
                Collections.emptyList(),
                Collections.emptyList(),
                Collections.emptyList(),
                Collections.emptyList(),
                recognizedRegistryConfig,
                registryMapperService);
    }

    @Test
    void getRegistryConfig_KnownHostname_ReturnsConfig() {
        var config = service.getRegistryConfig(ArtifactTypeEnum.CONTAINER,
                                               TEST_CONTAINER_REGISTRY_HOST_NAME_1);

        assertThat(config).isNotNull();
        assertThat(config.getHostname()).isEqualTo(TEST_CONTAINER_REGISTRY_HOST_NAME_1);
        assertThat(config.getCredentialValidation()).isNotNull();
        assertThat(config.getCredentialValidation().isEnabled()).isTrue();
        assertThat(config.getArtifactValidation()).isNotNull();
        assertThat(config.getArtifactValidation().isEnabled()).isTrue();
    }

    @Test
    void getRegistryConfig_UnknownHostname_ThrowsBadRequestException() {
        assertThrows(BadRequestException.class, () -> service.getRegistryConfig(
                ArtifactTypeEnum.CONTAINER, "unknown.registry.com"));
    }

    @Test
    void getRegistryConfig_EmptyMapForType_ThrowsBadRequestException() {
        var recognizedRegistryConfig = new RecognizedRegistryConfiguration();
        recognizedRegistryConfig.setContainer(Map.of());
        recognizedRegistryConfig.setHelm(Map.of());
        recognizedRegistryConfig.setModel(Map.of());
        recognizedRegistryConfig.setResource(Map.of());

        var lookupService = new RegistryLookupService(
                Collections.emptyList(), Collections.emptyList(),
                Collections.emptyList(), Collections.emptyList(),
                recognizedRegistryConfig, new RegistryMapperService("c", "a", "h"));

        assertThrows(BadRequestException.class, () -> lookupService.getRegistryConfig(
                ArtifactTypeEnum.CONTAINER, "any.host"));
    }

    @Test
    void getRegistryConfig_RecognizedMissing_ThrowsBadRequestException() {
        var lookupService = new RegistryLookupService(
                Collections.emptyList(), Collections.emptyList(),
                Collections.emptyList(), Collections.emptyList(),
                null, new RegistryMapperService("c", "a", "h"));

        assertThrows(BadRequestException.class,
                () -> lookupService.getRegistryConfig(ArtifactTypeEnum.CONTAINER, "any.host"));
    }
}
