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

package com.nvidia.boot.registries.service.registry.helm.artifactory;

import static com.nvidia.boot.registries.util.RegistriesConstants.ARTIFACTORY_REGISTRY_NAME;

import com.nvidia.boot.registries.service.registry.client.artifactory.ArtifactoryClient;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@RequiredArgsConstructor
public class ArtifactoryHelmRegistry implements HelmRegistry {

    private final ArtifactoryClient artifactoryClient;

    @Override
    public String getName() {
        return ARTIFACTORY_REGISTRY_NAME;
    }

    @Override
    public String getHostname() {
        return artifactoryClient.getHostname();
    }

    @Override
    public void validateArtifact(String helmChartUrl, String secret) {
        artifactoryClient.validateArtifact(helmChartUrl, secret);
    }

    @Override
    public void validateCredential(String hostname, String secret) {
        artifactoryClient.getOciRegistryAuthClient().validateCredential(hostname, secret);
    }

    @Override
    public void invalidateCache() {
        artifactoryClient.getOciRegistryAuthClient().invalidateCache();
    }
}

