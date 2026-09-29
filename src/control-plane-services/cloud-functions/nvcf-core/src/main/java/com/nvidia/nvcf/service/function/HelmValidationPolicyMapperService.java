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
package com.nvidia.nvcf.service.function;

import com.nvidia.nvcf.rest.function.deployment.dto.HelmValidationPolicyDto;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import tools.jackson.databind.json.JsonMapper;

@Service
@RequiredArgsConstructor
public class HelmValidationPolicyMapperService {
    private final JsonMapper jsonMapper;

    /**
     * Convert DTO to JSON string for persisting in gpu_specifications table.
     */
    @SneakyThrows
    public String toHelmValidationPolicyJson(HelmValidationPolicyDto dto) {
        if (dto == null) {
            return null;
        }
        return jsonMapper.writeValueAsString(dto);
    }

    /**
     * Convert JSON string (gpu_specifications table) to DTO.
     */
    @SneakyThrows
    public HelmValidationPolicyDto toHelmValidationPolicyDto(String json) {
        if (StringUtils.isBlank(json)) {
            return null;
        }
        return jsonMapper.readValue(json, HelmValidationPolicyDto.class);
    }
}
