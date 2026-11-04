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
package com.nvidia.nvcf.rest.registry;

import com.nvidia.boot.registries.service.registry.container.ContainerRegistry;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistry;
import com.nvidia.boot.registries.service.registry.model.ModelRegistry;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistry;
import java.util.List;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Service
public class TestRegistryService {
    @Autowired
    private List<ContainerRegistry> containerRegistries;
    @Autowired
    private List<HelmRegistry> helmRegistries;
    @Autowired
    private List<ModelRegistry> modelRegistries;
    @Autowired
    private List<ResourceRegistry> resourceRegistries;

    public void clearAll() {
        containerRegistries.forEach(ContainerRegistry::invalidateCache);
        helmRegistries.forEach(HelmRegistry::invalidateCache);
        modelRegistries.forEach(ModelRegistry::invalidateCache);
        resourceRegistries.forEach(ResourceRegistry::invalidateCache);
    }
}
