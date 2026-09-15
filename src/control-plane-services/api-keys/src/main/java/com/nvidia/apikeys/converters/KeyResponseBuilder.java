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

package com.nvidia.apikeys.converters;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.apikeys.dto.introspection.IntrospectionResponse;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.vo.GeneratedKeyVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.UnprocessableEntityException;
import java.util.List;
import lombok.RequiredArgsConstructor;
import org.apache.commons.collections4.ListUtils;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class KeyResponseBuilder {

    private final JsonMapper jsonMapper;

    public KeyDto toDto(KeyVo key, GeneratedKeyVo generatedKeyVo) {
        return fillMandatoryFields(key).toBuilder()
                .value(generatedKeyVo.getFormattedApiKey())
                .authorizations(toJsonNode(key.getAuthorizations()))
                .build();
    }

    private JsonNode toJsonNode(String authorizations) {
        try {
            return jsonMapper.readValue(authorizations, JsonNode.class);
        } catch (JacksonException e) {
            throw new UnprocessableEntityException("Failed to build authorizations json");
        }
    }

    public ListKeysResponse toListResponse(List<KeyByOwnerAndServiceVo> keys) {
        List<KeyDto> dtos = ListUtils.emptyIfNull(keys).stream()
                .map(this::toDto)
                .toList();

        return ListKeysResponse.builder()
                .keys(dtos)
                .build();
    }

    public KeyDto toDto(KeyVo key) {
        return fillMandatoryFields(key).toBuilder()
                .value(key.getApiKeySuffix())
                .authorizations(toJsonNode(key.getAuthorizations()))
                .build();
    }

    public IntrospectionResponse toIntrospectionResponse(KeyVo key) {
        return IntrospectionResponse.builder()
                .ownerId(key.getOwnerId())
                .ownerType(key.getOwnerType())
                .keyId(key.getKeyId())
                .issuerServiceId(key.getIssuerServiceId())
                .authorizations(toJsonNode(key.getAuthorizations()))
                .build();
    }

    public KeyDto toLookupDto(KeyVo key) {
        return fillMandatoryFields(key);
    }

    private KeyDto toDto(KeyByOwnerAndServiceVo vo) {
        return KeyDto.builder()
                .id(vo.getKeyId())
                .value(vo.getApiKeySuffix())
                .status(vo.getKeyStatus())
                .ownerType(vo.getOwnerType())
                .ownerId(vo.getOwnerId())
                .issuerServiceId(vo.getIssuerServiceId())
                .audienceServiceIds(vo.getAudienceServiceIds())
                .description(vo.getDescription())
                .createdAt(vo.getCreatedAt())
                .expiresAt(vo.getExpiresAt())
                .build();
    }

    private static KeyDto fillMandatoryFields(KeyVo key) {
        return KeyDto.builder()
                .id(key.getKeyId())
                .ownerType(key.getOwnerType())
                .ownerId(key.getOwnerId())
                .createdAt(key.getCreatedAt())
                .expiresAt(key.getExpiresAt())
                .status(key.getKeyStatus())
                .description(key.getDescription())
                .audienceServiceIds(key.getAudienceServiceIds())
                .issuerServiceId(key.getIssuerServiceId())
                .build();
    }
}
