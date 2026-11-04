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
package com.nvidia.nvct.service.registry;

import com.nvidia.boot.registries.service.registry.dto.ArtifactDetails;
import com.nvidia.nvct.persistence.task.entity.ModelUdt;
import com.nvidia.nvct.persistence.task.entity.ResourceUdt;
import java.util.List;
import java.util.Set;
import lombok.AllArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@AllArgsConstructor
public class RegistryArtifactMapperService {

    public static ArtifactDetails toArtifactDetailsFromResourceUdt(ResourceUdt artifactUdt) {
        return new ArtifactDetails(artifactUdt.getName(), artifactUdt.getVersion(),
                                   artifactUdt.getUrl());
    }

    public static List<ArtifactDetails> toArtifactDetailsFromResourceUdts(
            Set<ResourceUdt> resourceUdts) {
        return resourceUdts.stream()
                .map(RegistryArtifactMapperService::toArtifactDetailsFromResourceUdt)
                .toList();
    }

    public static ArtifactDetails toArtifactDetailsFromModelUdt(ModelUdt modelUdt) {
        return new ArtifactDetails(modelUdt.getName(), modelUdt.getVersion(),
                                   modelUdt.getUrl());
    }

    public static List<ArtifactDetails> toArtifactDetailsFromModelUdt(Set<ModelUdt> modelUdts) {
        return modelUdts.stream()
                .map(RegistryArtifactMapperService::toArtifactDetailsFromModelUdt)
                .toList();
    }
}
