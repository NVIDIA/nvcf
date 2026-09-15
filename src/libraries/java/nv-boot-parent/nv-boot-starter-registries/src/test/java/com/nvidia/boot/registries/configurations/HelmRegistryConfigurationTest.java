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

class HelmRegistryConfigurationTest {

    private static final String CUSTOM_REGISTRY_KEY = "custom-helm-registry";
    private static final String CUSTOM_REGISTRY_NAME = "Custom Helm Registry";
    private static final String CUSTOM_REGISTRY_HOSTNAME = "helm.registry.example.com";

    private static ApplicationContextRunner baseRunner() {
        return new ApplicationContextRunner()
                .withUserConfiguration(RefreshScopeConfig.class)
                .withUserConfiguration(TestKnownRegistryConfig.class)
                .withUserConfiguration(HelmRegistryAutoConfiguration.class);
    }

    @Test
    void customHelmRegistriesCreatesCustomRegistryForCustomKeys() {
        baseRunner().withUserConfiguration(TestPropertiesWithCustomRegistryConfig.class)
                .run(context -> {
                    var customList = context.getBean("customHelmRegistries", List.class);
                    assertThat(customList).hasSize(1);
                    var registry = (CustomHelmRegistry) customList.get(0);
                    assertThat(registry.getName()).isEqualTo(CUSTOM_REGISTRY_NAME);
                    assertThat(registry.getHostname()).isEqualTo(CUSTOM_REGISTRY_HOSTNAME);
                });
    }

    @Test
    void helmRegistriesMergesKnownAndCustomRegistries() {
        baseRunner().withUserConfiguration(TestPropertiesWithCustomRegistryConfig.class)
                .run(context -> {
                    var combined = context.getBean("helmRegistries", List.class);
                    assertThat(combined).isNotEmpty();
                    var customCount = combined.stream()
                            .filter(CustomHelmRegistry.class::isInstance)
                            .count();
                    assertThat(customCount).isEqualTo(1);
                });
    }

    @Test
    void helmRegistriesReturnsOnlyKnownWhenNoCustomRegistries() {
        baseRunner().withUserConfiguration(TestPropertiesConfig.class)
                .run(context -> {
                    var customList = context.getBean("customHelmRegistries", List.class);
                    assertThat(customList).isEmpty();

                    var combined = context.getBean("helmRegistries", List.class);
                    var customCount = combined.stream()
                            .filter(CustomHelmRegistry.class::isInstance)
                            .count();
                    assertThat(customCount).isZero();
                });
    }

    /**
     * Provides a dummy HelmRegistry bean so Spring resolves
     * List&lt;HelmRegistry&gt; knownHelmRegistries by collecting individual beans
     * rather than falling back to the helmRegistries bean (which would cause a circular
     * dependency when no individual beans exist).
     */
    @Configuration
    static class TestKnownRegistryConfig {
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

    @Configuration
    static class TestPropertiesConfig {
        @Bean
        RegistryConfigurationProperties registryConfigurationProperties() {
            var recognized = new RegistryConfigurationProperties.RecognizedRegistryConfiguration();
            recognized.setContainer(Map.of());
            recognized.setHelm(Map.of(
                    NGC_PRIVATE_REGISTRY_KEY,
                    registryConfig("NGC", "nvcr.io")));
            recognized.setModel(Map.of());
            recognized.setResource(Map.of());

            var props = new RegistryConfigurationProperties();
            props.setRecognized(recognized);
            return props;
        }
    }

    @Configuration
    static class TestPropertiesWithCustomRegistryConfig {
        @Bean
        RegistryConfigurationProperties registryConfigurationProperties() {
            var recognized = new RegistryConfigurationProperties.RecognizedRegistryConfiguration();
            recognized.setContainer(Map.of());
            recognized.setHelm(Map.of(
                    NGC_PRIVATE_REGISTRY_KEY,
                    registryConfig("NGC", "nvcr.io"),
                    CUSTOM_REGISTRY_KEY,
                    registryConfig(CUSTOM_REGISTRY_NAME, CUSTOM_REGISTRY_HOSTNAME)));
            recognized.setModel(Map.of());
            recognized.setResource(Map.of());

            var props = new RegistryConfigurationProperties();
            props.setRecognized(recognized);
            return props;
        }
    }
}
