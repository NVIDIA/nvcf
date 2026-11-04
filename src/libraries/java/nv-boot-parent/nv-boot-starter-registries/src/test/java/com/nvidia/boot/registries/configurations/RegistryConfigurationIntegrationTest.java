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

import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.TestUtils.registryConfig;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.custom.CustomContainerRegistry;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.custom.CustomHelmRegistry;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.config.CustomScopeConfigurer;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.cloud.context.scope.refresh.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.DependsOn;

/**
 * Integration tests verifying that the consolidated registry lists (containerRegistries,
 * helmRegistries) correctly merge known and custom registries. These @Primary beans are
 * the ones injected into ContainerRegistryService and HelmRegistryService in production.
 */
class RegistryConfigurationIntegrationTest {

    private static final String CUSTOM_CONTAINER_KEY = "custom-container-registry";
    private static final String CUSTOM_CONTAINER_NAME = "Custom Container Registry";
    private static final String CUSTOM_CONTAINER_HOSTNAME = "custom.container.example.com";

    private static final String CUSTOM_HELM_KEY = "custom-helm-registry";
    private static final String CUSTOM_HELM_NAME = "Custom Helm Registry";
    private static final String CUSTOM_HELM_HOSTNAME = "custom.helm.example.com";

    private static ApplicationContextRunner baseRunner() {
        return new ApplicationContextRunner()
                .withUserConfiguration(RefreshScopeConfig.class)
                .withUserConfiguration(IntegrationTestConfig.class)
                .withUserConfiguration(ContainerRegistryAutoConfiguration.class)
                .withUserConfiguration(HelmRegistryAutoConfiguration.class);
    }

    @Test
    void containerRegistriesIncludesCustomRegistryForServiceInjection() {
        baseRunner().withUserConfiguration(IntegrationTestPropertiesConfig.class)
                .run(context -> {
                    @SuppressWarnings("unchecked")
                    var containerRegistries =
                            (List<ContainerRegistry>) context.getBean("containerRegistries", List.class);
                    var customRegistry = containerRegistries.stream()
                            .filter(CustomContainerRegistry.class::isInstance)
                            .map(CustomContainerRegistry.class::cast)
                            .filter(r -> CUSTOM_CONTAINER_HOSTNAME.equals(r.getHostname()))
                            .findFirst();
                    assertThat(customRegistry).isPresent();
                    assertThat(customRegistry.get().getName()).isEqualTo(CUSTOM_CONTAINER_NAME);
                    assertThat(customRegistry.get().getHostname())
                            .isEqualTo(CUSTOM_CONTAINER_HOSTNAME);
                });
    }

    @Test
    void helmRegistriesIncludesCustomRegistryForServiceInjection() {
        baseRunner().withUserConfiguration(IntegrationTestPropertiesConfig.class)
                .run(context -> {
                    @SuppressWarnings("unchecked")
                    var helmRegistries =
                            (List<HelmRegistry>) context.getBean("helmRegistries", List.class);
                    var customRegistry = helmRegistries.stream()
                            .filter(CustomHelmRegistry.class::isInstance)
                            .map(CustomHelmRegistry.class::cast)
                            .filter(r -> CUSTOM_HELM_HOSTNAME.equals(r.getHostname()))
                            .findFirst();
                    assertThat(customRegistry).isPresent();
                    assertThat(customRegistry.get().getName()).isEqualTo(CUSTOM_HELM_NAME);
                    assertThat(customRegistry.get().getHostname())
                            .isEqualTo(CUSTOM_HELM_HOSTNAME);
                });
    }

    @Test
    void bothConsolidatedListsIncludeKnownAndCustomRegistries() {
        baseRunner().withUserConfiguration(IntegrationTestPropertiesConfig.class)
                .run(context -> {
                    var containerRegistries =
                            context.getBean("containerRegistries", List.class);
                    var helmRegistries = context.getBean("helmRegistries", List.class);

                    var containerCustomCount = containerRegistries.stream()
                            .filter(CustomContainerRegistry.class::isInstance)
                            .count();
                    var helmCustomCount = helmRegistries.stream()
                            .filter(CustomHelmRegistry.class::isInstance)
                            .count();

                    assertThat(containerCustomCount).isEqualTo(1);
                    assertThat(helmCustomCount).isEqualTo(1);
                    assertThat(containerRegistries).hasSizeGreaterThan(1);
                    assertThat(helmRegistries).hasSizeGreaterThan(1);
                });
    }

    @Configuration
    static class RefreshScopeConfig {
        @Bean
        RefreshScope refreshScope() {
            return new RefreshScope();
        }

        @Bean
        @DependsOn("refreshScope")
        CustomScopeConfigurer customScopeConfigurer(RefreshScope refreshScope) {
            var configurer = new CustomScopeConfigurer();
            configurer.addScope("refresh", refreshScope);
            return configurer;
        }
    }

    /**
     * Provides known registry beans so Spring resolves List parameters by collecting
     * individual beans, avoiding circular dependency with the consolidated list beans.
     */
    @Configuration
    static class IntegrationTestConfig {
        @Bean
        ContainerRegistry testKnownContainerRegistry() {
            return new ContainerRegistry() {
                @Override
                public String getName() {
                    return "NGC";
                }

                @Override
                public String getHostname() {
                    return "nvcr.io";
                }

                @Override
                public void validateArtifact(String containerImageUrl, String secret) {}

                @Override
                public void validateCredential(String hostname, String secret) {}

                @Override
                public void invalidateCache() {}
            };
        }

        @Bean
        HelmRegistry testKnownHelmRegistry() {
            return new HelmRegistry() {
                @Override
                public String getName() {
                    return "NGC";
                }

                @Override
                public String getHostname() {
                    return "nvcr.io";
                }

                @Override
                public void validateArtifact(String helmChartUrl, String secret) {}

                @Override
                public void validateCredential(String hostname, String secret) {}

                @Override
                public void invalidateCache() {}
            };
        }
    }

    @Configuration
    static class IntegrationTestPropertiesConfig {
        @Bean
        RegistryConfigurationProperties registryConfigurationProperties() {
            var recognized = new RegistryConfigurationProperties.RecognizedRegistryConfiguration();
            recognized.setContainer(Map.of(
                    NGC_PRIVATE_REGISTRY_KEY,
                    registryConfig("NGC", "nvcr.io"),
                    CUSTOM_CONTAINER_KEY,
                    registryConfig(CUSTOM_CONTAINER_NAME, CUSTOM_CONTAINER_HOSTNAME)));
            recognized.setHelm(Map.of(
                    NGC_PRIVATE_REGISTRY_KEY,
                    registryConfig("NGC", "nvcr.io"),
                    CUSTOM_HELM_KEY,
                    registryConfig(CUSTOM_HELM_NAME, CUSTOM_HELM_HOSTNAME)));
            recognized.setModel(Map.of());
            recognized.setResource(Map.of());

            var props = new RegistryConfigurationProperties();
            props.setRecognized(recognized);
            return props;
        }
    }
}
