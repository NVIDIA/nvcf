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

import com.nvidia.boot.registries.configurations.RegistryConfigPathProvider;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * Minimal test application for registry integration tests.
 * Provides RegistryConfigPathProvider required by the registry auto-configurations.
 */
@SpringBootApplication
public class TestRegistryApplication {

    public static void main(String[] args) {
        SpringApplication.run(TestRegistryApplication.class, args);
    }

    @Configuration(proxyBeanMethods = false)
    static class TestRegistryConfig {

        @Bean
        RegistryConfigPathProvider registryConfigPathProvider() {
            return () -> "test.registries";
        }

        /**
         * Provides dummy ContainerRegistry and HelmRegistry beans so Spring resolves
         * List&lt;ContainerRegistry&gt; knownContainerRegistries and
         * List&lt;HelmRegistry&gt; knownHelmRegistries by collecting individual beans
         * rather than falling back to the consolidated list beans (which would cause a
         * circular dependency when no individual beans exist or during RefreshScope init).
         */
        @Bean
        ContainerRegistry testKnownContainerRegistry() {
            return new ContainerRegistry() {
                @Override
                public String getName() {
                    return "test-container";
                }

                @Override
                public String getHostname() {
                    return "test.example.com";
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
                    return "test-helm";
                }

                @Override
                public String getHostname() {
                    return "test.example.com";
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
}
