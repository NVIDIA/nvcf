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

import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusResponse;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusesResponse;
import com.nvidia.apikeys.dto.keysV2.KeyResponse;
import com.nvidia.apikeys.dto.keysV2.KeyStatusV2;
import com.nvidia.apikeys.dto.keysV2.ListKeysResponse;
import com.nvidia.apikeys.dto.keysV2.OwnerStatusByAccount;
import com.nvidia.apikeys.dto.keysV2.OwnerStatusByService;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.vo.GeneratedKeyVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.apikeys.vo.OwnerAccountStatusVo;
import com.nvidia.boot.exceptions.UnprocessableEntityException;
import java.time.Instant;
import java.util.List;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

/**
 * Builds the shared v2 responses. Only the create response carries the full key value, every
 * other response carries the masked value.
 */
@Service
@RequiredArgsConstructor
public class KeyV2ResponseBuilder {

    private final JsonMapper jsonMapper;
    private final MillisecondPrecisionClock clock;

    public KeyResponse toCreateResponse(KeyV2Vo key, GeneratedKeyVo generatedKey) {
        return toResponse(key).toBuilder()
                .value(generatedKey.getFormattedApiKey())
                .build();
    }

    public KeyResponse toResponse(KeyV2Vo key) {
        return KeyResponse.builder()
                .id(key.getKeyId())
                .value(key.getApiKeySuffix())
                .status(toStatus(key.getKeyStatus(), key.getExpiresAt()))
                .ownerId(key.getOwnerId())
                .accountId(key.getNcaId())
                .issuerServiceId(key.getIssuerServiceId())
                .audienceServiceIds(key.getAudienceServiceIds())
                .description(key.getDescription())
                .createdAt(key.getCreatedAt())
                .expiresAt(key.getExpiresAt())
                .authorizations(toJsonNode(key.getAuthorizations()))
                .build();
    }

    /**
     * List items omit authorizations.
     */
    public KeyResponse toListItem(KeysByOwnerAndAccountAndServiceVo key) {
        return KeyResponse.builder()
                .id(key.getKeyId())
                .value(key.getApiKeySuffix())
                .status(toStatus(key.getKeyStatus(), key.getExpiresAt()))
                .ownerId(key.getOwnerId())
                .accountId(key.getNcaId())
                .issuerServiceId(key.getIssuerServiceId())
                .audienceServiceIds(key.getAudienceServiceIds())
                .description(key.getDescription())
                .createdAt(key.getCreatedAt())
                .expiresAt(key.getExpiresAt())
                .build();
    }

    public ListKeysResponse toListResponse(
            List<KeysByOwnerAndAccountAndServiceVo> keys, Integer limit, String cursor) {
        return ListKeysResponse.builder()
                .keys(keys.stream().map(this::toListItem).toList())
                .limit(limit)
                .cursor(cursor)
                .build();
    }

    public KeyOwnerStatusResponse toStatusResponse(OwnerStatusByAccountModel status) {
        return KeyOwnerStatusResponse.builder()
                .ownerId(status.getOwnerId())
                .accountId(status.getNcaId())
                .status(status.getOwnerStatus())
                .updatedAt(status.getUpdatedAt())
                .build();
    }

    public KeyOwnerStatusResponse toStatusResponse(OwnerStatusByAccountAndServiceModel status) {
        return KeyOwnerStatusResponse.builder()
                .ownerId(status.getOwnerId())
                .accountId(status.getNcaId())
                .issuerServiceId(status.getIssuerServiceId())
                .status(status.getOwnerStatus())
                .updatedAt(status.getUpdatedAt())
                .build();
    }

    public KeyOwnerStatusesResponse toStatusesResponse(
            String ownerId, List<OwnerAccountStatusVo> accounts, Integer limit, String cursor) {
        return KeyOwnerStatusesResponse.builder()
                .ownerId(ownerId)
                .accounts(accounts.stream().map(this::toAccountStatus).toList())
                .limit(limit)
                .cursor(cursor)
                .build();
    }

    private OwnerStatusByAccount toAccountStatus(OwnerAccountStatusVo account) {
        return OwnerStatusByAccount.builder()
                .accountId(account.ncaId())
                .status(account.status())
                .issuerServices(account.issuerServices().stream()
                                        .map(issuer -> OwnerStatusByService.builder()
                                                .issuerServiceId(issuer.issuerServiceId())
                                                .status(issuer.status())
                                                .updatedAt(issuer.updatedAt())
                                                .build())
                                        .toList())
                .build();
    }

    /**
     * A revoked key stays REVOKED. Any other key past its expiry is EXPIRED.
     */
    private KeyStatusV2 toStatus(KeyStatus status, Instant expiresAt) {
        if (status == KeyStatus.REVOKED) {
            return KeyStatusV2.REVOKED;
        }
        if (status == KeyStatus.EXPIRED || (expiresAt != null && !expiresAt.isAfter(clock.instant()))) {
            return KeyStatusV2.EXPIRED;
        }
        return KeyStatusV2.valueOf(status.name());
    }

    private JsonNode toJsonNode(String authorizations) {
        try {
            return jsonMapper.readTree(authorizations);
        } catch (JacksonException e) {
            throw new UnprocessableEntityException("Failed to build authorizations json");
        }
    }
}
