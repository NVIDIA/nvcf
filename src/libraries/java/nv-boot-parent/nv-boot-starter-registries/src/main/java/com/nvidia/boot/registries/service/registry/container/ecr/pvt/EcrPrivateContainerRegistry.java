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

package com.nvidia.boot.registries.service.registry.container.ecr.pvt;

import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PRIVATE_REGISTRY_NAME;

import com.nvidia.boot.registries.service.registry.client.ecr.pvt.EcrPrivateContainerRegistryClient;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;

@RequiredArgsConstructor
@Slf4j
public class EcrPrivateContainerRegistry implements ContainerRegistry {

    private final EcrPrivateContainerRegistryClient ecrPrivateContainerRegistryClient;

    @Override
    public String getName() {
        return ECR_PRIVATE_REGISTRY_NAME;
    }

    @Override
    public String getHostname() {
        return ecrPrivateContainerRegistryClient.getHostname();
    }

    @Override
    public void validateArtifact(String containerImageUrl, String secret) {
        ecrPrivateContainerRegistryClient.validateContainerImage(containerImageUrl, secret);
    }

    @Override
    public void validateCredential(String hostname, String secret) {
        ecrPrivateContainerRegistryClient.validateCredential(hostname, secret);
    }

    @Override
    public void invalidateCache() {

    }
}
