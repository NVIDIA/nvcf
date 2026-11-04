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

package com.nvidia.nvct.service.task;

import static java.nio.charset.StandardCharsets.UTF_8;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.rest.task.dto.GpuSpecificationDto;
import java.util.Base64;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class GpuSpecificationMapperService {

    private final JsonMapper jsonMapper;
    private final HelmValidationPolicyMapperService helmValidationPolicyMapperService;

    /**
     * Convert DTO to {@link GpuSpecUdt}, including
     * {@code helmValidationPolicy} serialized as JSON.
     */
    public GpuSpecUdt toGpuSpecUdt(GpuSpecificationDto dto) {
        var builder = GpuSpecUdt.builder()
                .backend(dto.backend())
                .gpu(dto.gpu())
                .instanceType(dto.instanceType())
                .clusters(dto.clusters())
                .helmValidationPolicy(
                        helmValidationPolicyMapperService.toHelmValidationPolicyJson(
                                dto.helmValidationPolicy()));

        if (dto.configuration() != null) {
            builder.configuration(
                    Base64.getEncoder().encodeToString(
                            dto.configuration().toString().getBytes(UTF_8)));
        }

        return builder.build();
    }

    /**
     * Convert {@link GpuSpecUdt} to DTO, including {@code helmValidationPolicy}.
     */
    @SneakyThrows
    public GpuSpecificationDto toGpuSpecificationDto(GpuSpecUdt udt) {
        var builder = GpuSpecificationDto.builder()
                .backend(udt.getBackend())
                .gpu(udt.getGpu())
                .instanceType(udt.getInstanceType())
                .clusters(udt.getClusters())
                .helmValidationPolicy(
                        helmValidationPolicyMapperService.toHelmValidationPolicyDto(
                                udt.getHelmValidationPolicy()));

        if (isNotBlank(udt.getConfiguration())) {
            builder.configuration(
                    jsonMapper.readValue(
                            Base64.getDecoder().decode(udt.getConfiguration()),
                            ObjectNode.class));
        }

        return builder.build();
    }
}
