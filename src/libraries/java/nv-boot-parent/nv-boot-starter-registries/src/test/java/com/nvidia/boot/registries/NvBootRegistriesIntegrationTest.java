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

package com.nvidia.boot.registries;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties;
import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistryService;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistryService;
import com.nvidia.boot.registries.service.registry.model.ModelRegistryService;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistryService;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ActiveProfiles;

/**
 * Integration test to verify registry auto-configurations are loaded when the app starts.
 */
@SpringBootTest(classes = TestRegistryApplication.class)
@ActiveProfiles("test")
class NvBootRegistriesIntegrationTest {

    @Autowired
    private RegistryConfigurationProperties registryConfigurationProperties;

    @Autowired
    private RegistryMapperService registryMapperService;

    @Autowired
    private RegistryLookupService registryLookupService;

    @Autowired
    private ModelRegistryService modelRegistryService;

    @Autowired
    private ResourceRegistryService resourceRegistryService;

    @Autowired
    private HelmRegistryService helmRegistryService;

    @Autowired
    private ContainerRegistryService containerRegistryService;

    @Test
    void registryConfigurationsAreLoaded() {
        assertThat(registryConfigurationProperties).isNotNull();
        assertThat(registryConfigurationProperties.getRecognized()).isNotNull();
    }

    @Test
    void registryServiceBeansAreInjected() {
        assertThat(registryMapperService).isNotNull();
        assertThat(registryLookupService).isNotNull();
        assertThat(modelRegistryService).isNotNull();
        assertThat(resourceRegistryService).isNotNull();
        assertThat(helmRegistryService).isNotNull();
        assertThat(containerRegistryService).isNotNull();
    }
}
