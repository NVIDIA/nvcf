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

import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.persistence.registry.RegistryCredentialsByAccountRepository;
import com.nvidia.nvcf.persistence.registry.entity.ArtifactType;
import com.nvidia.nvcf.persistence.registry.entity.ProvisionedBy;
import com.nvidia.nvcf.persistence.registry.entity.RegistryCredentialByAccountEntity;
import com.nvidia.nvcf.persistence.registry.entity.RegistryCredentialByAccountKey;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.registry.dto.AddRegistryCredentialRequest;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.Base64;
import java.util.Locale;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import tools.jackson.databind.JsonNode;

@Slf4j
@Service
@AllArgsConstructor
public class RegistryFunctionMapperService {
    private static final String API_KEY_PREFIX = "nvapi-";
    private static final String LEGACY_KEY_TYPE = "LEGACY";
    private static final String OAUTH_TOKEN_PREFIX = "$oauthtoken:";
    private static final String MESG_MISSING_REGISTRY_SECRET =
            "Account '%s', Registry Credential '%s': Missing secret in ESS (likely db replication" +
                    " delay), returning null";
    private static final String MESG_STALE_CACHED_ENTRY =
            "Account '{}', Registry Credential '{}': Missing in ESS and DB (stale cache entry, " +
                    "likely deleted), returning null";

    private final RegistryCredentialEssService registryCredentialEssService;
    private final RegistryMapperService registryMapperService;
    private final RegistryCredentialsByAccountRepository registryCredentialsByAccountRepository;

    public RegistryCredentialDto toRegistryCredentialDto(
            RegistryCredentialDetailsDto registryCredentialDetailsDto) {
        var registryCredentialId = registryCredentialDetailsDto.registryCredentialId();
        var hostname = registryCredentialDetailsDto.registryHostname();
        var ncaId = registryCredentialDetailsDto.ncaId();
        var artifactTypes = registryCredentialDetailsDto.artifactTypes();
        
        return registryCredentialEssService
                .getRegistryCredentialSecret(ncaId, registryCredentialId)
                .map(secret -> RegistryCredentialDto.builder()
                        .registryHostname(hostname)
                        .artifactTypes(artifactTypes)
                        .secret(secret)
                        .build())
                .orElseGet(() -> {
                    // Secret not found in ESS. Check if the registry credential still exists in DB.
                    // If registry credential still exists in db, it's probably because of the
                    // db replication delay after deletion. Log instead of throwing an error.
                    var existsInDb = registryCredentialsByAccountRepository
                            .findByKeyNcaIdAndKeyRegistryCredentialId(ncaId, registryCredentialId)
                            .isPresent();
                    if (existsInDb) {
                        var mesg = MESG_MISSING_REGISTRY_SECRET
                                .formatted(ncaId, registryCredentialId);
                        log.error(mesg);
                    } else {
                        // If the registry credential doesn't exist in the DB
                        // it was perhaps deleted and cache has a stale entry
                        log.debug(MESG_STALE_CACHED_ENTRY, ncaId, registryCredentialId);
                    }
                    return null;
                });
    }

    public RegistryCredentialDetailsDto toRegistryCredentialDetailsDto(
            RegistryCredentialByAccountEntity entity) {
        return RegistryCredentialDetailsDto.builder()
                .registryCredentialId(entity.getKey().getRegistryCredentialId())
                .ncaId(entity.getKey().getNcaId())
                .registryName(entity.getRegistryName())
                .registryHostname(entity.getRegistryHostname())
                .registryCredentialName(entity.getRegistryCredentialName())
                .artifactTypes(toArtifactTypeEnums(entity.getArtifactTypes()))
                .description(entity.getDescription())
                .tags(entity.getTags())
                .provisionedBy(toProvisionedByEnum(entity.getProvisionedBy()))
                .keyType(toKeyType(entity))   // Temporary to help NGC with key migration
                .lastUpdatedAt(entity.getLastUpdatedAt())
                .createdAt(entity.getCreatedAt())
                .build();
    }

    private String toKeyType(RegistryCredentialByAccountEntity entity) {
        if (!isNgcRegistryHostname(entity.getRegistryHostname())) {
            return null;
        }

        return registryCredentialEssService
                .getRegistryCredentialSecret(
                        entity.getKey().getNcaId(), entity.getKey().getRegistryCredentialId())
                .map(SecretDto::value)
                .filter(JsonNode::isString)
                .map(JsonNode::stringValue)
                .flatMap(this::decodeSecret)
                .flatMap(this::extractApiKey)
                .filter(apiKey -> !apiKey.startsWith(API_KEY_PREFIX))
                .map(apiKey -> LEGACY_KEY_TYPE)
                .orElse(null);
    }

    private static boolean isNgcRegistryHostname(String hostname) {
        var normalizedHostname = hostname.toLowerCase(Locale.ROOT);
        return normalizedHostname.endsWith("nvcr.io")
                || normalizedHostname.endsWith("ngc.nvidia.com");
    }

    private Optional<String> decodeSecret(String encodedSecret) {
        try {
            return Optional.of(new String(
                    Base64.getDecoder().decode(encodedSecret), StandardCharsets.UTF_8));
        } catch (IllegalArgumentException ex) {
            return Optional.empty();
        }
    }

    private Optional<String> extractApiKey(String decodedSecret) {
        if (!decodedSecret.startsWith(OAUTH_TOKEN_PREFIX)) {
            return Optional.empty();
        }

        return Optional.of(decodedSecret.substring(OAUTH_TOKEN_PREFIX.length()))
                .filter(apiKey -> !apiKey.isEmpty());
    }

    public static RegistryCredentialByAccountEntity toRegistryCredentialByAccountEntity(
            String ncaId,
            UUID registryCredentialId,
            String registryName,
            ProvisionedByEnum provisionedBy,
            AddRegistryCredentialRequest request) {
        var key = RegistryCredentialByAccountKey.builder()
                .ncaId(ncaId)
                .registryCredentialId(registryCredentialId)
                .build();
        return RegistryCredentialByAccountEntity.builder()
                .key(key)
                .registryName(registryName)
                .registryHostname(request.registryHostname())
                .registryCredentialName(request.secret().name())
                .artifactTypes(toArtifactTypes(request.artifactTypes()))
                .tags(request.tags())
                .description(request.description())
                .provisionedBy(toProvisionedBy(provisionedBy))
                .createdAt(Instant.now())
                .build();
    }

    public static AddRegistryCredentialRequest toAddRegistryCredentialRequest(
            RegistryCredentialDto registryCredential) {
        return AddRegistryCredentialRequest.builder()
                .registryHostname(registryCredential.registryHostname())
                .artifactTypes(registryCredential.artifactTypes())
                .secret(registryCredential.secret())
                .tags(registryCredential.tags())
                .description(registryCredential.description())
                .build();
    }

    public RegistryCredentialDetailsDto toRegistryCredentialDetailsWithCanaryHostname(
            RegistryCredentialDetailsDto registryCredentialDetails) {
        return registryCredentialDetails.toBuilder()
                .registryHostname(registryMapperService.toCanaryHostname(
                        registryCredentialDetails.registryHostname()))
                .build();
    }

    public static ArtifactType toArtifactType(ArtifactTypeEnum artifactTypeEnum) {
        return switch (artifactTypeEnum) {
            case CONTAINER -> ArtifactType.CONTAINER;
            case HELM -> ArtifactType.HELM;
            case MODEL -> ArtifactType.MODEL;
            case RESOURCE -> ArtifactType.RESOURCE;
        };
    }

    public static Set<ArtifactType> toArtifactTypes(Set<ArtifactTypeEnum> artifactTypeEnums) {
        return artifactTypeEnums.stream()
                .map(RegistryFunctionMapperService::toArtifactType)
                .collect(Collectors.toSet());
    }

    public static ArtifactTypeEnum toArtifactTypeEnum(ArtifactType artifactType) {
        return switch (artifactType) {
            case CONTAINER -> ArtifactTypeEnum.CONTAINER;
            case HELM -> ArtifactTypeEnum.HELM;
            case MODEL -> ArtifactTypeEnum.MODEL;
            case RESOURCE -> ArtifactTypeEnum.RESOURCE;
        };
    }

    public static Set<ArtifactTypeEnum> toArtifactTypeEnums(Set<ArtifactType> artifactTypes) {
        return artifactTypes.stream()
                .map(RegistryFunctionMapperService::toArtifactTypeEnum)
                .collect(Collectors.toSet());
    }

    public static ProvisionedBy toProvisionedBy(ProvisionedByEnum provisionedByEnum) {
        return switch (provisionedByEnum) {
            case SYSTEM -> ProvisionedBy.SYSTEM;
            case USER -> ProvisionedBy.USER;
        };
    }

    public static ProvisionedByEnum toProvisionedByEnum(ProvisionedBy provisionedBy) {
        return switch (provisionedBy) {
            case SYSTEM -> ProvisionedByEnum.SYSTEM;
            case USER -> ProvisionedByEnum.USER;
        };
    }
}
