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
package com.nvidia.nvcf.service.registry;

import com.nvidia.boot.registries.service.registry.dto.ArtifactDetails;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import java.util.List;
import java.util.Set;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class RegistryArtifactMapperService {
    public static ArtifactDetails toArtifactDetailsFromResourceUdt(ResourceUdt artifactUdt) {
        return new ArtifactDetails(artifactUdt.getName(), artifactUdt.getVersion(),
                                   artifactUdt.getUrl());
    }

    public static List<ArtifactDetails> toArtifactDetailsFromResoureUdts(
            Set<ResourceUdt> resourceUdts) {
        return resourceUdts.stream()
                .map(RegistryArtifactMapperService::toArtifactDetailsFromResourceUdt)
                .toList();
    }

    public static ArtifactDetails toArtifactDetailsFromFunctionModelDto(
            FunctionModelDto dto) {
        return new ArtifactDetails(
                dto.getName(),
                dto.getVersion(),
                dto.getUri() != null ? dto.getUri().toString() : null);
    }

    public static List<ArtifactDetails> toArtifactDetailsFromFunctionModelDtos(
            List<FunctionModelDto> dtos) {
        return dtos.stream()
                .filter(dto -> dto.getUri() != null)
                .map(RegistryArtifactMapperService::toArtifactDetailsFromFunctionModelDto)
                .toList();
    }
}
