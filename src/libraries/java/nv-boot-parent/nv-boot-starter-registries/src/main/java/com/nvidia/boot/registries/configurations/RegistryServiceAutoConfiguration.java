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

package com.nvidia.boot.registries.configurations;

import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_ARTIFACT_REGISTRY_PROD_HOSTNAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_CONTAINER_REGISTRY_PROD_HOSTNAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_HELM_REGISTRY_PROD_HOSTNAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_KEY;

import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.RegistryValidationService;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistryService;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistryService;
import com.nvidia.boot.registries.service.registry.model.ModelRegistry;
import com.nvidia.boot.registries.service.registry.model.ModelRegistryService;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistry;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistryService;
import java.util.List;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnBean;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;

// Spring Boot only scans for components (@Service, @Component, @Repository, etc.) within
// the application's package and its sub-packages by default.
// Since the dependency library classes may be in a different package hierarchy
// (com.nvidia.boot.registries.* vs com.nvidia.nvct.*), Spring Boot's component scanner
// doesn't find them. With that the users don't need to setup component scanning manually.
@AutoConfiguration
@ConditionalOnBean(RegistryConfigurationProperties.class)
public class RegistryServiceAutoConfiguration {

    @Bean
    @RefreshScope
    public ModelRegistryService modelRegistryService(
            List<ModelRegistry> modelRegistries,
            RegistryMapperService registryMapperService,
            RegistryValidationService registryValidationService) {
        return new ModelRegistryService(modelRegistries, registryMapperService,
                                        registryValidationService);
    }

    @Bean
    @RefreshScope
    public ResourceRegistryService resourceRegistryService(
            List<ResourceRegistry> resourceRegistries,
            RegistryMapperService registryMapperService,
            RegistryValidationService registryValidationService) {
        return new ResourceRegistryService(resourceRegistries, registryMapperService,
                                           registryValidationService);
    }

    @Bean
    @RefreshScope
    public HelmRegistryService helmRegistryService(
            @Qualifier("helmRegistries") List<HelmRegistry> helmRegistries,
            RegistryMapperService registryMapperService,
            RegistryValidationService registryValidationService) {
        return new HelmRegistryService(helmRegistries, registryMapperService,
                                       registryValidationService);
    }

    @Bean
    @RefreshScope
    public ContainerRegistryService containerRegistryService(
            @Qualifier("containerRegistries") List<ContainerRegistry> containerRegistries,
            RegistryMapperService registryMapperService,
            RegistryValidationService registryValidationService) {
        return new ContainerRegistryService(containerRegistries, registryMapperService,
                                            registryValidationService);
    }

    @Bean
    @RefreshScope
    public RegistryLookupService registryLookupService(
            List<ResourceRegistry> resourceRegistries,
            List<ModelRegistry> modelRegistries,
            @Qualifier("containerRegistries") List<ContainerRegistry> containerRegistries,
            @Qualifier("helmRegistries") List<HelmRegistry> helmRegistries,
            RegistryConfigurationProperties registryConfigurationProperties,
            RegistryMapperService registryMapperService) {
        return new RegistryLookupService(
                resourceRegistries,
                modelRegistries,
                helmRegistries,
                containerRegistries,
                registryConfigurationProperties.getRecognized(),
                registryMapperService);
    }

    @Bean
    @RefreshScope
    public RegistryMapperService registryMapperService(
            RegistryConfigurationProperties registryConfigurationProperties) {
        var registryConfig = registryConfigurationProperties.getRecognized();

        var helmRegistryHostname = registryConfig.getHelm().containsKey(NGC_PRIVATE_REGISTRY_KEY) ?
                registryConfig.getHelm().get(NGC_PRIVATE_REGISTRY_KEY).getHostname() :
                NGC_HELM_REGISTRY_PROD_HOSTNAME;
        var containerRegistryHostname =
                registryConfig.getContainer().containsKey(NGC_PRIVATE_REGISTRY_KEY) ?
                        registryConfig.getContainer().get(NGC_PRIVATE_REGISTRY_KEY).getHostname() :
                        NGC_CONTAINER_REGISTRY_PROD_HOSTNAME;
        // Same for NGC model and resource registry
        var artifactRegistryHostname =
                registryConfig.getModel().containsKey(NGC_PRIVATE_REGISTRY_KEY) ?
                        registryConfig.getModel().get(NGC_PRIVATE_REGISTRY_KEY).getHostname() :
                        NGC_ARTIFACT_REGISTRY_PROD_HOSTNAME;

        return new RegistryMapperService(containerRegistryHostname, artifactRegistryHostname,
                                         helmRegistryHostname);
    }

    @Bean
    @RefreshScope
    public RegistryValidationService registryValidationService(
            RegistryLookupService registryLookupService) {
        return new RegistryValidationService(registryLookupService);
    }
}
