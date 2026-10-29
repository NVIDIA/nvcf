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

package com.nvidia.boot.registries.service.registry.helm.ngc;

import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_NAME;

import com.nvidia.boot.registries.service.registry.client.ngc.NgcArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ngc.NgcRegistryUtils;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@RequiredArgsConstructor
@Slf4j
public class NgcHelmRegistry implements HelmRegistry {
    private final NgcArtifactRegistryClient ngcArtifactRegistryClient;

    @Override
    public String getName() {
        return NGC_PRIVATE_REGISTRY_NAME;
    }

    @Override
    public String getHostname() {
        // We are using the artifact endpoint to validate helm registry, so need to translate
        // the helm registry hostname to artifact hostname.
        return translateArtifactHostnameToHelmRegistryHostname(
                ngcArtifactRegistryClient.getHostname());
    }

    @Override
    public void validateArtifact(String helmChartUrl, String secret) {
        ngcArtifactRegistryClient.validateHelmChart(helmChartUrl,
                                                    NgcRegistryUtils.getApiKey(secret));
    }

    @Override
    public void validateCredential(String hostname, String secret) {
        ngcArtifactRegistryClient.validateCredential(hostname,
                                                     NgcRegistryUtils.getApiKey(secret));
    }

    @Override
    public void invalidateCache() {
        ngcArtifactRegistryClient.resetAuthTokenCache();
    }

    public static String translateHelmRegistryHostnameToArtifactHostname(String helmHostname) {
        return helmHostname.replace("helm", "api");
    }

    public static String translateArtifactHostnameToHelmRegistryHostname(String artifactHostname) {
        return artifactHostname.replace("api", "helm");
    }
}
