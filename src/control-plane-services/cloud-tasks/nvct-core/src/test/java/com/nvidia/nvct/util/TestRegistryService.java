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
package com.nvidia.nvct.util;

import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_REGISTRY;
import static com.nvidia.boot.registries.service.registry.client.acr.AzureRegistryClient.AZURE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PUBLIC_REGISTRY_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ACR_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ARTIFACTORY_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.DOCKER_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PRIVATE_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PUBLIC_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.HARBOR_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.VOLCENGINE_REGISTRY_NAME;
import static com.nvidia.nvct.util.TestConstants.BASE_ARTIFACT_URL;
import static com.nvidia.nvct.util.TestConstants.TEST_ARTIFACT_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_DOCKER_REGISTRY;
import static com.nvidia.nvct.util.TestConstants.TEST_HELM_REGISTRY;

import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistryService;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistryService;
import com.nvidia.boot.registries.service.registry.model.ModelRegistryService;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistryService;
import java.net.URI;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

@Service
public class TestRegistryService {

    private final ModelRegistryService modelRegistryService;
    private final ResourceRegistryService resourceRegistryService;
    private final HelmRegistryService helmRegistryService;
    private final ContainerRegistryService containerRegistryService;
    private final RegistryLookupService registryLookupService;
    private final RegistryMapperService registryMapperService;

    public TestRegistryService(
            ModelRegistryService modelRegistryService,
            ResourceRegistryService resourceRegistryService,
            HelmRegistryService helmRegistryService,
            ContainerRegistryService containerRegistryService,
            RegistryLookupService registryLookupService,
            RegistryMapperService registryMapperService,
            @Value("${nvct.registries.recognized.container.ngc.hostname}")
            String ngcContainerRegistryHostname,
            @Value("${nvct.registries.recognized.model.ngc.hostname}")
            String casBaseUrl,
            @Value("${nvct.registries.recognized.container.docker.hostname}")
            String dockerBaseUrl,
            @Value("${nvct.registries.recognized.container.ecr.hostname}")
            String ecrPrivateHostname,
            @Value("${nvct.registries.recognized.container.ecr-public.hostname}")
            String ecrPublicHostname,
            @Value("${nvct.registries.recognized.container.volcengine.hostname}")
            String volcengineHostname,
            @Value("${nvct.registries.recognized.container.acr.hostname}")
            String acrHostname,
            @Value("${nvct.registries.recognized.container.harbor.hostname}")
            String harborHostname,
            @Value("${nvct.registries.recognized.container.artifactory.hostname}")
            String artifactoryHostname) {
        this.modelRegistryService = modelRegistryService;
        this.resourceRegistryService = resourceRegistryService;
        this.helmRegistryService = helmRegistryService;
        this.containerRegistryService = containerRegistryService;
        this.registryLookupService = registryLookupService;
        this.registryMapperService = registryMapperService;

        updateNgcRegistryHostname(ngcContainerRegistryHostname, casBaseUrl);
        updateDockerRegistryHostname(dockerBaseUrl);
        updateEcrPrivateRegistryHostname(ecrPrivateHostname);
        updateEcrPublicRegistryHostname(ecrPublicHostname);
        updateVolcengineRegistryHostname(volcengineHostname);
        updateAcrRegistryHostname(acrHostname);
        updateHarborRegistryHostname(harborHostname);
        updateArtifactoryRegistryHostname(artifactoryHostname);
    }

    private void updateNgcRegistryHostname(String ngcContainerRegistryHostname, String casBaseUrl) {
        modelRegistryService.overwriteRegistryHostnameMap(
                URI.create(casBaseUrl).getHost(),
                URI.create(BASE_ARTIFACT_URL).getHost());
        resourceRegistryService.overwriteRegistryHostnameMap(
                URI.create(casBaseUrl).getHost(),
                URI.create(BASE_ARTIFACT_URL).getHost());
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(casBaseUrl).getHost(), TEST_HELM_REGISTRY);
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(ngcContainerRegistryHostname).getHost(), TEST_CONTAINER_REGISTRY);
        registryLookupService.updateContainerRegistryConfigMap(
                ngcContainerRegistryHostname, TEST_CONTAINER_REGISTRY);
        registryLookupService.updateContainerRegistryMap(
                NGC_PRIVATE_REGISTRY_NAME, TEST_CONTAINER_REGISTRY);
        registryLookupService.updateHelmRegistryConfigMap(
                casBaseUrl, TEST_HELM_REGISTRY);
        registryLookupService.updateHelmRegistryMap(
                NGC_PRIVATE_REGISTRY_NAME, TEST_HELM_REGISTRY);
        registryLookupService.updateModelRegistryConfigMap(
                casBaseUrl, TEST_ARTIFACT_REGISTRY);
        registryLookupService.updateModelRegistryMap(
                NGC_PRIVATE_REGISTRY_NAME, TEST_ARTIFACT_REGISTRY);
        registryLookupService.updateResourceRegistryConfigMap(
                casBaseUrl, TEST_ARTIFACT_REGISTRY);
        registryLookupService.updateResourceRegistryMap(
                NGC_PRIVATE_REGISTRY_NAME, TEST_ARTIFACT_REGISTRY);
        registryMapperService.updateNgcContainerRegistryHostname(TEST_CONTAINER_REGISTRY);
        registryMapperService.updateNgcArtifactRegistryHostname(TEST_ARTIFACT_REGISTRY);
        registryMapperService.updateNgcHelmRegistryHostname(TEST_HELM_REGISTRY);
    }

    private void updateDockerRegistryHostname(String dockerBaseUrl) {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(dockerBaseUrl).getHost(), TEST_DOCKER_REGISTRY);
        registryLookupService.updateContainerRegistryConfigMap(
                dockerBaseUrl, TEST_DOCKER_REGISTRY);
        registryLookupService.updateContainerRegistryMap(
                DOCKER_REGISTRY_NAME, TEST_DOCKER_REGISTRY);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(dockerBaseUrl).getHost(), TEST_DOCKER_REGISTRY);
        registryLookupService.updateHelmRegistryConfigMap(
                dockerBaseUrl, TEST_DOCKER_REGISTRY);
        registryLookupService.updateHelmRegistryMap(
                DOCKER_REGISTRY_NAME, TEST_DOCKER_REGISTRY);
    }

    private void updateEcrPrivateRegistryHostname(String ecrPrivateHostname) {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(ecrPrivateHostname).getHost(), ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryConfigMap(
                ecrPrivateHostname, ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryMap(
                ECR_PRIVATE_REGISTRY_NAME, ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(ecrPrivateHostname).getHost(), ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryConfigMap(
                ecrPrivateHostname, ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryMap(
                ECR_PRIVATE_REGISTRY_NAME, ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
    }

    private void updateEcrPublicRegistryHostname(String ecrPublicHostname) {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(ecrPublicHostname).getHost(), ECR_PUBLIC_REGISTRY_HOSTNAME);
        registryLookupService.updateContainerRegistryConfigMap(
                ecrPublicHostname, ECR_PUBLIC_REGISTRY_HOSTNAME);
        registryLookupService.updateContainerRegistryMap(
                ECR_PUBLIC_REGISTRY_NAME, ECR_PUBLIC_REGISTRY_HOSTNAME);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(ecrPublicHostname).getHost(), ECR_PUBLIC_REGISTRY_HOSTNAME);
        registryLookupService.updateHelmRegistryConfigMap(
                ecrPublicHostname, ECR_PUBLIC_REGISTRY_HOSTNAME);
        registryLookupService.updateHelmRegistryMap(
                ECR_PUBLIC_REGISTRY_NAME, ECR_PUBLIC_REGISTRY_HOSTNAME);
    }

    private void updateVolcengineRegistryHostname(String volcengineHostname) {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(volcengineHostname).getHost(), VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryConfigMap(
                volcengineHostname, VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryMap(
                VOLCENGINE_REGISTRY_NAME, VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(volcengineHostname).getHost(), VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryConfigMap(
                volcengineHostname, VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryMap(
                VOLCENGINE_REGISTRY_NAME, VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
    }

    private void updateAcrRegistryHostname(String acrHostname) {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(acrHostname).getHost(), AZURE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryConfigMap(
                acrHostname, AZURE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateContainerRegistryMap(
                ACR_REGISTRY_NAME, AZURE_REGISTRY_GLOBAL_HOSTNAME);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(acrHostname).getHost(), AZURE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryConfigMap(
                acrHostname, AZURE_REGISTRY_GLOBAL_HOSTNAME);
        registryLookupService.updateHelmRegistryMap(
                ACR_REGISTRY_NAME, AZURE_REGISTRY_GLOBAL_HOSTNAME);
    }

    private void updateHarborRegistryHostname(String harborHostname) {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(harborHostname).getHost(), TEST_HARBOR_REGISTRY);
        registryLookupService.updateContainerRegistryConfigMap(
                harborHostname, TEST_HARBOR_REGISTRY);
        registryLookupService.updateContainerRegistryMap(
                HARBOR_REGISTRY_NAME, TEST_HARBOR_REGISTRY);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(harborHostname).getHost(), TEST_HARBOR_REGISTRY);
        registryLookupService.updateHelmRegistryConfigMap(
                harborHostname, TEST_HARBOR_REGISTRY);
        registryLookupService.updateHelmRegistryMap(
                HARBOR_REGISTRY_NAME, TEST_HARBOR_REGISTRY);
    }

    private void updateArtifactoryRegistryHostname(String artifactoryHostname) {
        containerRegistryService.overwriteRegistryHostnameMap(
                URI.create(artifactoryHostname).getHost(), TEST_ARTIFACTORY_REGISTRY);
        registryLookupService.updateContainerRegistryConfigMap(
                artifactoryHostname, TEST_ARTIFACTORY_REGISTRY);
        registryLookupService.updateContainerRegistryMap(
                ARTIFACTORY_REGISTRY_NAME, TEST_ARTIFACTORY_REGISTRY);

        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(artifactoryHostname).getHost(), TEST_ARTIFACTORY_REGISTRY);
        registryLookupService.updateHelmRegistryConfigMap(
                artifactoryHostname, TEST_ARTIFACTORY_REGISTRY);
        registryLookupService.updateHelmRegistryMap(
                ARTIFACTORY_REGISTRY_NAME, TEST_ARTIFACTORY_REGISTRY);
    }
}
