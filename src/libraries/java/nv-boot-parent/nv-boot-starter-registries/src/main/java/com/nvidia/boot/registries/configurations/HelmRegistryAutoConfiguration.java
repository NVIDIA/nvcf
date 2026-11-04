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
import com.nvidia.boot.registries.service.registry.client.ecr.pub.EcrPublicArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ecr.pvt.EcrPrivateArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.client.harbor.HarborRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ngc.NgcArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.acr.AcrHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.artifactory.ArtifactoryHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.custom.CustomHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.docker.DockerHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.ecr.pub.EcrPublicHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.ecr.pvt.EcrPrivateHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.harbor.HarborHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.ngc.NgcHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.volcengine.VolcengineHelmRegistry;
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
public class HelmRegistryAutoConfiguration {

    private static final Set<String> KNOWN_HELM_REGISTRIES =
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
    public List<HelmRegistry> customHelmRegistries(
            RegistryConfigurationProperties properties) {
        var helmRegistriesMap = properties.getRecognized().getHelm();
        var customHelmRegistries = new ArrayList<HelmRegistry>();

        var entries = helmRegistriesMap.entrySet();
        for (var entry: entries) {
            var key = entry.getKey();
            if (KNOWN_HELM_REGISTRIES.contains(key)) {
                continue;
            }

            entry.getValue().getArtifactValidation().setEnabled(false);
            entry.getValue().getCredentialValidation().setEnabled(false);
            var registry = new CustomHelmRegistry(entry.getValue().getName(),
                                                  entry.getValue().getHostname());
            customHelmRegistries.add(registry);
        }

        return customHelmRegistries;
    }

    /**
     * Consolidated list of all helm registries (known + custom) for injection into
     * HelmRegistryService and RegistryLookupService. Spring injects
     * {@code List<HelmRegistry>}  by collecting individual {@code HelmRegistry} beans;
     * it does not include beans of type {@code List<HelmRegistry>}. This bean merges both
     * sources so consumers receive the full set.
     *
     * <p>
     * Individual {@code HelmRegistry} beans for known helm registries such as ngc,
     * docker, etc. should be in the context so that Spring can collect them and inject a list of
     * knownHelmRegistries when calling this method. However, if there are no individual
     * {@code HelmRegistry} registry beans, then Spring cannot build a list. So, Spring
     * looks for factory methods with return type {@code  List<HelmRegistry>} and the only
     * candidates are helmRegistries(the bean currently being created) and customHelmRegistries
     * factory methods in this class. If Spring chooses helmRegistries factory method to
     * satisfy knownHelmRegistries, it results in the following circular dependency -
     *     Creating helmRegistries → needs knownHelmRegistries →
     *                                      resolves to helmRegistries → circular dependency
     * and causes BeanCurrentlyInCreationException: the bean being created is requested again
     *                                              while it is still in creation.
     * This is why we define factory methods to add dummy HelmRegistry beans in the
     * integration tests.
     * </p>
     */
    @Bean
    @RefreshScope
    @Primary
    public List<HelmRegistry> helmRegistries(
            List<HelmRegistry> knownHelmRegistries,
            @Qualifier("customHelmRegistries") List<HelmRegistry> customHelmRegistries) {
        if (CollectionUtils.isEmpty(customHelmRegistries)) {
            return knownHelmRegistries;
        }

        var combined = new ArrayList<HelmRegistry>(knownHelmRegistries);
        combined.addAll(customHelmRegistries);
        return combined;
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "helm", key = NGC_PRIVATE_REGISTRY_KEY)
    public HelmRegistry ngcHelmRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var ngcConfig = registryConfigurationProperties.getRecognized()
                .getHelm().get(NGC_PRIVATE_REGISTRY_KEY);
        // We are using the artifact endpoint to validate helm registry, so need to translate
        // the helm registry hostname to artifact hostname.
        var ngcEndpoint = NgcHelmRegistry
                .translateHelmRegistryHostnameToArtifactHostname(ngcConfig.getHostname());
        var ngcHelmArtifactRegistryClient = new NgcArtifactRegistryClient(
                webClientBuilder, ngcEndpoint, ngcConfig.getCallTimeout(), ngcConfig.getReadTimeout(),
                ngcConfig.getWriteTimeout(), ngcConfig.getConnectionTimeout(),
                ngcConfig.getOauth2().getBaseUrl(), ngcConfig.getOauth2().getGroupScope());
        return new NgcHelmRegistry(ngcHelmArtifactRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "helm", key = DOCKER_REGISTRY_KEY)
    public HelmRegistry dockerHelmRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var dockerConfig = registryConfigurationProperties.getRecognized()
                .getHelm().get(DOCKER_REGISTRY_KEY);
        var dockerRegistryClient = new DockerRegistryClient(
                webClientBuilder,
                dockerConfig.getHostname(),
                dockerConfig.getCallTimeout(),
                dockerConfig.getOauth2().getBaseUrl(),
                dockerConfig.getOauth2().getGroupScope());
        return new DockerHelmRegistry(dockerRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "helm", key = ECR_PRIVATE_REGISTRY_KEY)
    public HelmRegistry ecrPrivateHelmRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var ecrConfig = registryConfigurationProperties
                .getRecognized()
                .getHelm()
                .get(ECR_PRIVATE_REGISTRY_KEY);
        var ecrPrivateArtifactRegistryClient =
                new EcrPrivateArtifactRegistryClient(
                        webClientBuilder,
                        ecrConfig.getHostname(),
                        ecrConfig.getCallTimeout());
        return new EcrPrivateHelmRegistry(ecrPrivateArtifactRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "helm", key = ECR_PUBLIC_REGISTRY_KEY)
    public HelmRegistry ecrPublicHelmRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var ecrConfig = registryConfigurationProperties
                .getRecognized()
                .getHelm()
                .get(ECR_PUBLIC_REGISTRY_KEY);
        var ecrPublicArtifactRegistryClient = new EcrPublicArtifactRegistryClient(
                webClientBuilder,
                ecrConfig.getHostname(),
                ecrConfig.getCallTimeout()
        );
        return new EcrPublicHelmRegistry(ecrPublicArtifactRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "helm", key = VOLCENGINE_REGISTRY_KEY)
    public HelmRegistry volcengineHelmRegistry(
            RegistryConfigurationProperties registryConfigurationProperties,
            WebClient.Builder webClientBuilder) {
        var volcengineConfig = registryConfigurationProperties
                .getRecognized()
                .getContainer()
                .get(VOLCENGINE_REGISTRY_KEY);
        var volcengineArtifactRegistryClient = new VolcengineArtifactRegistryClient(
                webClientBuilder,
                volcengineConfig.getHostname(),
                volcengineConfig.getCallTimeout());
        return new VolcengineHelmRegistry(volcengineArtifactRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "helm", key = ACR_REGISTRY_KEY)
    public HelmRegistry azureHelmRegistry(
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
        return new AcrHelmRegistry(azureRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "helm", key = HARBOR_REGISTRY_KEY)
    public HelmRegistry harborHelmRegistry(
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
        return new HarborHelmRegistry(harborRegistryClient);
    }

    @Bean
    @RefreshScope
    @ConditionalOnRegistryKey(registryType = "helm", key = ARTIFACTORY_REGISTRY_KEY)
    public HelmRegistry artifactoryHelmRegistry(
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
        return new ArtifactoryHelmRegistry(artifactoryClient);
    }
}
