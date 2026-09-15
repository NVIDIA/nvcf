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

import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.DOCKER_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PRIVATE_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_CONTAINER_REGISTRY_HOST_NAME_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_ECR_PRIVATE_REGISTRY_HOST_NAME;
import static com.nvidia.boot.registries.util.TestConstants.TEST_HELM_REGISTRY_HOST_NAME_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_MODEL_REGISTRY_HOST_NAME_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_ARTIFACT_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY_CANARY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_RECOGNIZED_CONTAINER_REGISTRY_KEY_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_RECOGNIZED_HELM_REGISTRY_KEY_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_RECOGNIZED_MODEL_REGISTRY_KEY_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_RECOGNIZED_RESOURCE_REGISTRY_KEY_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_RESOURCE_REGISTRY_HOST_NAME_1;
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

class RegistryValidationServiceTest {

    private RecognizedRegistryConfiguration recognizedRegistryConfig;
    private RegistryMapperService registryMapperService;

    @BeforeEach
    void setUp() {
        recognizedRegistryConfig = new RecognizedRegistryConfiguration();
        recognizedRegistryConfig.setContainer(Map.of(
                TEST_RECOGNIZED_CONTAINER_REGISTRY_KEY_1,
                registryConfig(TEST_CONTAINER_REGISTRY_HOST_NAME_1, true, true)));
        recognizedRegistryConfig.setHelm(Map.of(
                TEST_RECOGNIZED_HELM_REGISTRY_KEY_1,
                registryConfig(TEST_HELM_REGISTRY_HOST_NAME_1, true, true)));
        recognizedRegistryConfig.setModel(Map.of(
                TEST_RECOGNIZED_MODEL_REGISTRY_KEY_1,
                registryConfig(TEST_MODEL_REGISTRY_HOST_NAME_1, true, true)));
        recognizedRegistryConfig.setResource(Map.of(
                TEST_RECOGNIZED_RESOURCE_REGISTRY_KEY_1,
                registryConfig(TEST_RESOURCE_REGISTRY_HOST_NAME_1, true, true)));

        registryMapperService = new RegistryMapperService(
                TEST_NGC_CONTAINER_REGISTRY_PROD,
                TEST_NGC_ARTIFACT_REGISTRY_PROD,
                TEST_NGC_HELM_REGISTRY_PROD);
    }

    @Test
    void isCredentialValidationEnabled_NoMatchingHostname_ThrowsBadRequestException() {
        var service = createService();

        assertThrows(BadRequestException.class, () -> service.isCredentialValidationEnabled(
                ArtifactTypeEnum.CONTAINER, "unknown.registry.com"));
    }

    @Test
    void isArtifactValidationEnabled_NoMatchingHostname_ThrowsBadRequestException() {
        var service = createService();

        assertThrows(BadRequestException.class, () -> service.isArtifactValidationEnabled(
                ArtifactTypeEnum.HELM, "unknown.registry.com"));
    }

    @Test
    void isCredentialValidationEnabled_Disabled_ReturnsFalse() {
        recognizedRegistryConfig.setContainer(
                Map.of(DOCKER_REGISTRY_KEY, registryConfig("docker.io", false, true)));

        var service = createService();

        assertThat(service.isCredentialValidationEnabled(
                ArtifactTypeEnum.CONTAINER, "docker.io")).isFalse();
    }

    @Test
    void isCredentialValidationEnabled_Enabled_ReturnsTrue() {
        var service = createService();

        assertThat(service.isCredentialValidationEnabled(
                ArtifactTypeEnum.CONTAINER, TEST_CONTAINER_REGISTRY_HOST_NAME_1)).isTrue();
    }

    @Test
    void isCredentialValidationEnabled_ValidationConfigMissing_ReturnsTrue() {
        recognizedRegistryConfig.setContainer(
                Map.of(TEST_RECOGNIZED_CONTAINER_REGISTRY_KEY_1,
                       registryConfig(TEST_CONTAINER_REGISTRY_HOST_NAME_1)));

        var service = createService();

        assertThat(service.isCredentialValidationEnabled(
                ArtifactTypeEnum.CONTAINER, TEST_CONTAINER_REGISTRY_HOST_NAME_1)).isTrue();
    }

    @Test
    void isCredentialValidationEnabled_EcrPrivateHostname_UsesGlobalRecognizedHostname() {
        recognizedRegistryConfig.setContainer(
                Map.of(ECR_PRIVATE_REGISTRY_KEY,
                       registryConfig(ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME, false, true)));

        var service = createService();

        assertThat(service.isCredentialValidationEnabled(
                ArtifactTypeEnum.CONTAINER, TEST_ECR_PRIVATE_REGISTRY_HOST_NAME)).isFalse();
    }

    @Test
    void isCredentialValidationEnabled_CanaryHostname_UsesNormalizedRecognizedHostname() {
        recognizedRegistryConfig.setContainer(
                Map.of(NGC_PRIVATE_REGISTRY_KEY,
                       registryConfig(TEST_NGC_CONTAINER_REGISTRY_PROD, false, true)));

        var service = createService();

        assertThat(service.isCredentialValidationEnabled(
                ArtifactTypeEnum.CONTAINER, TEST_NGC_CONTAINER_REGISTRY_CANARY)).isFalse();
    }

    @Test
    void isArtifactValidationEnabled_Disabled_ReturnsFalse() {
        recognizedRegistryConfig.setHelm(
                Map.of(TEST_RECOGNIZED_HELM_REGISTRY_KEY_1,
                       registryConfig(TEST_HELM_REGISTRY_HOST_NAME_1, true, false)));

        var service = createService();

        assertThat(service.isArtifactValidationEnabled(
                ArtifactTypeEnum.HELM, TEST_HELM_REGISTRY_HOST_NAME_1)).isFalse();
    }

    @Test
    void isArtifactValidationEnabled_Enabled_ReturnsTrue() {
        var service = createService();

        assertThat(service.isArtifactValidationEnabled(
                ArtifactTypeEnum.MODEL, TEST_MODEL_REGISTRY_HOST_NAME_1)).isTrue();
    }

    @Test
    void isArtifactValidationEnabled_ValidationConfigMissing_ReturnsTrue() {
        recognizedRegistryConfig.setResource(
                Map.of(TEST_RECOGNIZED_RESOURCE_REGISTRY_KEY_1,
                       registryConfig(TEST_RESOURCE_REGISTRY_HOST_NAME_1)));

        var service = createService();

        assertThat(service.isArtifactValidationEnabled(
                ArtifactTypeEnum.RESOURCE, TEST_RESOURCE_REGISTRY_HOST_NAME_1)).isTrue();
    }

    @Test
    void isArtifactValidationEnabled_EachArtifactTypeRoutesToCorrectMap() {
        recognizedRegistryConfig.setContainer(
                Map.of(TEST_RECOGNIZED_CONTAINER_REGISTRY_KEY_1,
                       registryConfig(TEST_CONTAINER_REGISTRY_HOST_NAME_1, true, false)));
        recognizedRegistryConfig.setHelm(
                Map.of(TEST_RECOGNIZED_HELM_REGISTRY_KEY_1,
                       registryConfig(TEST_HELM_REGISTRY_HOST_NAME_1, true, false)));
        recognizedRegistryConfig.setModel(
                Map.of(TEST_RECOGNIZED_MODEL_REGISTRY_KEY_1,
                       registryConfig(TEST_MODEL_REGISTRY_HOST_NAME_1, true, true)));
        recognizedRegistryConfig.setResource(
                Map.of(TEST_RECOGNIZED_RESOURCE_REGISTRY_KEY_1,
                       registryConfig(TEST_RESOURCE_REGISTRY_HOST_NAME_1, true, true)));

        var service = createService();

        assertThat(service.isArtifactValidationEnabled(
                ArtifactTypeEnum.CONTAINER, TEST_CONTAINER_REGISTRY_HOST_NAME_1)).isFalse();
        assertThat(service.isArtifactValidationEnabled(
                ArtifactTypeEnum.HELM, TEST_HELM_REGISTRY_HOST_NAME_1)).isFalse();
        assertThat(service.isArtifactValidationEnabled(
                ArtifactTypeEnum.MODEL, TEST_MODEL_REGISTRY_HOST_NAME_1)).isTrue();
        assertThat(service.isArtifactValidationEnabled(
                ArtifactTypeEnum.RESOURCE, TEST_RESOURCE_REGISTRY_HOST_NAME_1)).isTrue();
    }

    private RegistryValidationService createService() {
        var lookupService = new RegistryLookupService(
                Collections.emptyList(),
                Collections.emptyList(),
                Collections.emptyList(),
                Collections.emptyList(),
                recognizedRegistryConfig,
                registryMapperService);
        return new RegistryValidationService(lookupService);
    }
}
