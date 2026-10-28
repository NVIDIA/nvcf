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

package com.nvidia.apikeys.services;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.function.UnaryOperator;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import org.apache.commons.collections4.MapUtils;
import org.apache.commons.lang3.Strings;
import org.springframework.stereotype.Service;

/**
 * Maps the configured legacy account onto the legacy keys and keys_by_owner_and_service tables.
 *
 * <p>V2 callers use the service IDs that are keys of apikeys.service-id-map. Legacy rows store
 * the mapped values. Only a mapped issuer service ID can have legacy keys. Audience service IDs
 * without a mapping are kept as they are, so stored keys round-trip unchanged.
 */
@Service
@RequiredArgsConstructor
public class LegacyAccountTranslator {

    static final String MESG_UNKNOWN_ISSUER_SERVICE = "Unknown issuer service: '%s'";

    private final NakProperties nakProperties;

    public boolean isLegacy(String ncaId) {
        return Strings.CS.equals(nakProperties.getNcaId(), ncaId);
    }

    public Optional<String> findLegacyIssuerServiceId(String serviceId) {
        return Optional.ofNullable(serviceIdMap().get(serviceId));
    }

    private String toLegacyIssuerServiceId(String serviceId) {
        return findLegacyIssuerServiceId(serviceId)
                .orElseThrow(() -> new BadRequestException(
                        MESG_UNKNOWN_ISSUER_SERVICE.formatted(serviceId)));
    }

    public Optional<String> findServiceId(String legacyServiceId) {
        return serviceIdMap().entrySet().stream()
                .filter(entry -> Strings.CS.equals(entry.getValue(), legacyServiceId))
                .map(Map.Entry::getKey)
                .findFirst();
    }

    public String toServiceId(String legacyServiceId) {
        return findServiceId(legacyServiceId).orElse(legacyServiceId);
    }

    public KeyVo toLegacy(KeyV2Vo key) {
        return KeyVo.builder()
                .keyStatus(key.getKeyStatus())
                .ownerType(key.getOwnerType())
                .ownerId(key.getOwnerId())
                .issuerServiceId(toLegacyIssuerServiceId(key.getIssuerServiceId()))
                .audienceServiceIds(translate(key.getAudienceServiceIds(), this::toLegacyServiceId))
                .keyId(key.getKeyId())
                .keyHash(key.getKeyHash())
                .createdAt(key.getCreatedAt())
                .expiresAt(key.getExpiresAt())
                .deletesAt(key.getDeletesAt())
                .apiKeySuffix(key.getApiKeySuffix())
                .authorizations(key.getAuthorizations())
                .description(key.getDescription())
                .build();
    }

    public KeyV2Vo fromLegacy(KeyVo key) {
        return KeyV2Vo.builder()
                .keyStatus(key.getKeyStatus())
                .ncaId(nakProperties.getNcaId())
                .ownerType(key.getOwnerType())
                .ownerId(key.getOwnerId())
                .issuerServiceId(toServiceId(key.getIssuerServiceId()))
                .audienceServiceIds(translate(key.getAudienceServiceIds(), this::toServiceId))
                .keyId(key.getKeyId())
                .keyHash(key.getKeyHash())
                .createdAt(key.getCreatedAt())
                .expiresAt(key.getExpiresAt())
                .deletesAt(key.getDeletesAt())
                .apiKeySuffix(key.getApiKeySuffix())
                .authorizations(key.getAuthorizations())
                .description(key.getDescription())
                .build();
    }

    public KeysByOwnerAndAccountAndServiceVo fromLegacy(KeyByOwnerAndServiceVo key) {
        return KeysByOwnerAndAccountAndServiceVo.builder()
                .ncaId(nakProperties.getNcaId())
                .ownerType(key.getOwnerType())
                .ownerId(key.getOwnerId())
                .issuerServiceId(toServiceId(key.getIssuerServiceId()))
                .keyId(key.getKeyId())
                .createdAt(key.getCreatedAt())
                .expiresAt(key.getExpiresAt())
                .deletesAt(key.getDeletesAt())
                .keyStatus(key.getKeyStatus())
                .keyHash(key.getKeyHash())
                .apiKeySuffix(key.getApiKeySuffix())
                .description(key.getDescription())
                .audienceServiceIds(translate(key.getAudienceServiceIds(), this::toServiceId))
                .build();
    }

    private String toLegacyServiceId(String serviceId) {
        return serviceIdMap().getOrDefault(serviceId, serviceId);
    }

    private Map<String, String> serviceIdMap() {
        return MapUtils.emptyIfNull(nakProperties.getServiceIdMap());
    }

    private static Set<String> translate(Set<String> serviceIds, UnaryOperator<String> translation) {
        if (serviceIds == null) {
            return null;
        }
        return serviceIds.stream().map(translation).collect(Collectors.toUnmodifiableSet());
    }
}
