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

package com.nvidia.boot.registries.service.registry.model.ngc;

import static com.nvidia.boot.registries.service.registry.client.ngc.NgcRegistryUtils.MESG_FETCHED_PRESIGNED_ARTIFACT_URLS;
import static com.nvidia.boot.registries.service.registry.client.ngc.NgcRegistryUtils.MESG_FETCHED_SIZE_ARTIFACT_URLS;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_NAME;

import com.nvidia.boot.registries.service.registry.client.ngc.NgcArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ngc.NgcRegistryUtils;
import com.nvidia.boot.registries.service.registry.dto.Artifact;
import com.nvidia.boot.registries.service.registry.dto.ArtifactDetails;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.boot.registries.service.registry.model.ModelRegistry;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@RequiredArgsConstructor
@Slf4j
public class NgcModelRegistry implements ModelRegistry {
    private final NgcArtifactRegistryClient ngcArtifactRegistryClient;

    @Override
    public String getName() {
        return NGC_PRIVATE_REGISTRY_NAME;
    }

    @Override
    public String getHostname() {
        return ngcArtifactRegistryClient.getHostname();
    }


    @Override
    public long fetchSize(ArtifactDetails model, String secret) {
        var apiKey = NgcRegistryUtils.getApiKey(secret);
        var size = ngcArtifactRegistryClient.fetchModelSize(model.url(), apiKey);
        log.trace(MESG_FETCHED_SIZE_ARTIFACT_URLS, model.name(), model.url());
        return size;
    }

    @Override
    public Artifact fetchArtifact(ArtifactDetails model, String secret) {
        var apiKey = NgcRegistryUtils.getApiKey(secret);
        var preSignedModelURLs =
                ngcArtifactRegistryClient.getPreSignedArtifactURLs(model.url(), apiKey);
        log.trace(MESG_FETCHED_PRESIGNED_ARTIFACT_URLS, model.name(), preSignedModelURLs);
        return new Artifact(model.name(),
                            model.version(),
                            ArtifactTypeEnum.MODEL,
                            preSignedModelURLs);
    }

    @Override
    public void validateArtifact(ArtifactDetails model, String secret) {
        ngcArtifactRegistryClient.validateArtifact(model.url(),
                                                   NgcRegistryUtils.getApiKey(secret),
                                                   ArtifactTypeEnum.MODEL);
    }

    @Override
    public void invalidateCache() {
        ngcArtifactRegistryClient.resetAuthTokenCache();
    }
}
