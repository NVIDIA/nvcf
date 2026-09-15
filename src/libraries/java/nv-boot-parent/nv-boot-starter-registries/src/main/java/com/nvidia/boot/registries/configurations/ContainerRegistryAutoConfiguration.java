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

import static com.nvidia.boot.registries.util.RegistriesConstants.ACR_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.ARTIFACTORY_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.DOCKER_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PRIVATE_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PUBLIC_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.HARBOR_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_KEY;
import static com.nvidia.boot.registries.util.RegistriesConstants.VOLCENGINE_REGISTRY_KEY;

import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.OAuth2Configuration;
import com.nvidia.boot.registries.service.registry.client.acr.AzureRegistryClient;
import com.nvidia.boot.registries.service.registry.client.artifactory.ArtifactoryClient;
import com.nvidia.boot.registries.service.registry.client.docker.DockerRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ecr.pub.EcrPublicContainerRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ecr.pvt.EcrPrivateContainerRegistryClient;
import com.nvidia.boot.registries.service.registry.client.harbor.HarborRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ngc.NgcContainerRegistryClient;
import com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineContainerRegistryClient;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.acr.AcrContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.artifactory.ArtifactoryContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.custom.CustomContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.docker.DockerContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.ecr.pub.EcrPublicContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.ecr.pvt.EcrPrivateContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.harbor.HarborContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.ngc.NgcContainerRegistry;
import com.nvidia.boot.registries.service.registry.container.volcengine.VolcengineContainerRegistry;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.AutoConfigureAfter;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Primary;
import org.springframework.util.CollectionUtils;
import org.springframework.web.reactive.function.client.WebClient;

@AutoConfiguration
@AutoConfigureAfter(RegistryConfigurationPropertiesAutoConfiguration.class)
public class ContainerRegistryAutoConfiguration {

    private static final Set<String> KNOWN_CONTAINER_REGISTRIES =
            Set.of(NGC_PRIVATE_REGISTRY_KEY,
                   DOCKER_REGISTRY_KEY,
                   ECR_PRIVATE_REGISTRY_KEY,
                   ECR_PUBLIC_REGISTRY_KEY,
                   ACR_REGISTRY_KEY,
                   ARTIFACTORY_REGISTRY_KEY,
                   VOLCENGINE_REGISTRY_KEY,
                   HARBOR_REGISTRY_KEY);

    @Bean
    @RefreshScope
    public List<ContainerRegistry> customContainerRegistries(
            RegistryConfigurationProperties properties) {
        var containerRegistriesMap = properties.getRecognized().getContainer();
        var customContainerRegistries = new ArrayList<ContainerRegistry>();

        var entries = containerRegistriesMap.entrySet();
        for (var entry: entries) {
            var key = entry.getKey();
            if (KNOWN_CONTAINER_REGISTRIES.contains(key)) {
                continue;
            }

            entry.getValue().getArtifactValidation().setEnabled(false);
            entry.getValue().getCredentialValidation().setEnabled(false);
            var registry = new CustomContainerRegistry(entry.getValue().getName(),
                                                       entry.getValue().getHostname());
            customContainerRegistries.add(registry);
        }

        return customContainerRegistries;
    }

    /**
     * Consolidated list of all container registries (known + custom) for injection into
     * ContainerRegistryService and RegistryLookupService. Spring injects
     * {@code List<ContainerRegistry>}  by collecting individual {@code ContainerRegistry} beans;
     * it does not include beans of type {@code List<ContainerRegistry>}. This bean merges both
     * sources so consumers receive the full set.
     *
     * <p>
     * Individual {@code ContainerRegistry} beans for known container registries such as ngc,
     * docker, etc. should be in the context so that Spring can collect them and inject a list of
     * knownContainerRegistries when calling this method. However, if there are no individual
     * {@code ContainerRegistry} registry beans, then Spring cannot build a list. So, Spring
     * looks for factory methods with return type {@code  List<ContainerRegistry>} and the only
     * candidates are containerRegistries(the bean currently being created) and
     * customContainerRegistries factory methods in this class. If Spring chooses
     * containerRegistries factory method to satisfy knownContainerRegistries, it results in the
     * following circular dependency -
     *     Creating containerRegistries → needs knownContainerRegistries →
     *                                      resolves to containerRegistries → circular dependency
     * and causes BeanCurrentlyInCreationException: the bean being created is requested again
     *                                              while it is still in creation.
     * This is why we define factory methods to add dummy ContainerRegistry beans in the
     * integration tests.
     * </p>
     */
    @Bean
    @RefreshScope
    @Primary
    public List<ContainerRegistry> containerRegistries(
            List<ContainerRegistry> knownContainerRegistries,
            @Qualifier("customContainerRegistries")
            List<ContainerRegistry> customContainerRegistries) {
        if (CollectionUtils.isEmpty(customContainerRegistries)) {
            return knownContainerRegistries;
        }

        var combined = new ArrayList<ContainerRegistry>(knownContainerRegistries);
        combined.addAll(customContainerRegistries);
        return combined;
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "container", key = NGC_PRIVATE_REGISTRY_KEY)
    public ContainerRegistry ngcContainerRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var ngcConfig = registryConfigurationProperties.getRecognized()
                .getContainer()
                .get(NGC_PRIVATE_REGISTRY_KEY);
        var ngcContainerRegistryClient = new NgcContainerRegistryClient(
                webClientBuilder,
                ngcConfig.getHostname(),
                ngcConfig.getCallTimeout(),
                ngcConfig.getReadTimeout(),
                ngcConfig.getWriteTimeout(),
                ngcConfig.getConnectionTimeout());
        return new NgcContainerRegistry(ngcContainerRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "container", key = DOCKER_REGISTRY_KEY)
    public ContainerRegistry dockerContainerRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var dockerConfig = registryConfigurationProperties
                .getRecognized()
                .getContainer()
                .get(DOCKER_REGISTRY_KEY);
        var dockerRegistryClient =
                new DockerRegistryClient(webClientBuilder,
                                         dockerConfig.getHostname(),
                                         dockerConfig.getCallTimeout(),
                                         dockerConfig.getOauth2().getBaseUrl(),
                                         dockerConfig.getOauth2().getGroupScope());
        return new DockerContainerRegistry(dockerRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "container", key = ECR_PRIVATE_REGISTRY_KEY)
    public ContainerRegistry ecrPrivateContainerRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var ecrConfig = registryConfigurationProperties
                .getRecognized()
                .getContainer()
                .get(ECR_PRIVATE_REGISTRY_KEY);
        var ecrPrivateContainerRegistryClient = new EcrPrivateContainerRegistryClient(
                webClientBuilder,
                ecrConfig.getHostname(),
                ecrConfig.getCallTimeout());
        return new EcrPrivateContainerRegistry(ecrPrivateContainerRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "container", key = ECR_PUBLIC_REGISTRY_KEY)
    public ContainerRegistry ecrPublicContainerRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var ecrConfig = registryConfigurationProperties
                .getRecognized()
                .getContainer()
                .get(ECR_PUBLIC_REGISTRY_KEY);
        var ecrPublicContainerRegistryClient = new EcrPublicContainerRegistryClient(
                webClientBuilder,
                ecrConfig.getHostname(),
                ecrConfig.getCallTimeout());
        return new EcrPublicContainerRegistry(ecrPublicContainerRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "container", key = VOLCENGINE_REGISTRY_KEY)
    public ContainerRegistry volcengineContainerRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var volcengineConfig = registryConfigurationProperties
                .getRecognized()
                .getContainer()
                .get(VOLCENGINE_REGISTRY_KEY);
        var volcengineContainerRegistryClient = new VolcengineContainerRegistryClient(
                webClientBuilder,
                volcengineConfig.getHostname(),
                volcengineConfig.getCallTimeout());
        return new VolcengineContainerRegistry(volcengineContainerRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "container", key = ACR_REGISTRY_KEY)
    public ContainerRegistry azureContainerRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var acrConfig = registryConfigurationProperties
                .getRecognized()
                .getContainer()
                .get(ACR_REGISTRY_KEY);
        var azureRegistryClient = new AzureRegistryClient(
                webClientBuilder,
                acrConfig.getHostname(),
                acrConfig.getCallTimeout(),
                Optional.ofNullable(acrConfig.getOauth2())
                        .map(OAuth2Configuration::getBaseUrl)
                        .orElse(null));
        return new AcrContainerRegistry(azureRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "container", key = HARBOR_REGISTRY_KEY)
    public ContainerRegistry harborContainerRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var harborConfig = registryConfigurationProperties
                .getRecognized()
                .getContainer()
                .get(HARBOR_REGISTRY_KEY);
        var harborRegistryClient = new HarborRegistryClient(
                webClientBuilder,
                harborConfig.getHostname(),
                harborConfig.getCallTimeout(),
                Optional.ofNullable(harborConfig.getOauth2())
                        .map(OAuth2Configuration::getBaseUrl)
                        .orElse(null));
        return new HarborContainerRegistry(harborRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "container", key = ARTIFACTORY_REGISTRY_KEY)
    public ContainerRegistry artifactoryContainerRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var artifactoryConfig = registryConfigurationProperties
                .getRecognized()
                .getContainer()
                .get(ARTIFACTORY_REGISTRY_KEY);
        var artifactoryClient = new ArtifactoryClient(
                webClientBuilder,
                artifactoryConfig.getHostname(),
                artifactoryConfig.getCallTimeout(),
                Optional.ofNullable(artifactoryConfig.getOauth2())
                        .map(OAuth2Configuration::getBaseUrl)
                        .orElse(null));
        return new ArtifactoryContainerRegistry(artifactoryClient);
    }
}
