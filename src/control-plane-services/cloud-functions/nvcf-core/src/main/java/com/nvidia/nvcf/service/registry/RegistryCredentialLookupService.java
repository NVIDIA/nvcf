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

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.google.common.collect.Sets;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.persistence.registry.RegistryCredentialsByAccountRepository;
import com.nvidia.nvcf.persistence.registry.entity.RegistryCredentialByAccountEntity;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import java.time.Duration;
import java.util.Collections;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RequiredArgsConstructor
public class RegistryCredentialLookupService {
    private static final String MESG_REGISTRY_NOT_FOUND_ACCOUNT =
            "Registry Credential '%s': Not found in account '%s'";

    private final RegistryCredentialsByAccountRepository registryCredentialsByAccountRepository;
    private final RegistryMapperService registryMapperService;
    private final RegistryFunctionMapperService registryFunctionMapperService;
    private final LoadingCache<String, List<RegistryCredentialByAccountEntity>>
            registryCredentialsByAccountCache = Caffeine.newBuilder()
                .maximumSize(1024)   // Approximate number of accounts in Prod env.
                .expireAfterWrite(Duration.ofMinutes(1))
                .scheduler(Scheduler.systemScheduler())
                .build(this::fetchRegistryCredentialsByAccount);


    public void saveRegistryCredential(String ncaId, RegistryCredentialByAccountEntity entity) {
        registryCredentialsByAccountRepository.save(entity);
        invalidateCache(ncaId);
    }

    public void deleteRegistryCredential(String ncaId, RegistryCredentialByAccountEntity entity) {
        registryCredentialsByAccountRepository.deleteById(entity.getKey());
        invalidateCache(ncaId);
    }

    public int getRegistryCredentialCountByAccount(String ncaId) {
        return (int) registryCredentialsByAccountRepository.countByKeyNcaId(ncaId);
    }

    public RegistryCredentialByAccountEntity lookupRegistryCredentialByAccountAndIdOrThrow(
            String ncaId,
            UUID registryCredentialId) {
        return registryCredentialsByAccountRepository
                .findByKeyNcaIdAndKeyRegistryCredentialId(ncaId, registryCredentialId)
                .orElseThrow(() -> {
                    var message = MESG_REGISTRY_NOT_FOUND_ACCOUNT
                            .formatted(registryCredentialId, ncaId);
                    log.debug(message);
                    return new NotFoundException(message);
                });
    }

    public Stream<RegistryCredentialByAccountEntity> lookupRegistryCredentialByAccount(
            String ncaId) {
        return registryCredentialsByAccountRepository.findByKeyNcaId(ncaId);
    }

    public List<RegistryCredentialDetailsDto> getRegistryCredentialDtos(String ncaId) {
        var registryCredentials = registryCredentialsByAccountCache.get(ncaId);
        if (CollectionUtils.isEmpty(registryCredentials)) {
            return Collections.emptyList();
        }

        return registryCredentials.stream()
                .map(registryFunctionMapperService::toRegistryCredentialDetailsDto)
                .toList();
    }

    public List<RegistryCredentialDetailsDto> getRegistryCredentialDtos(
            String ncaId,
            Set<ArtifactTypeEnum> artifactTypeEnums) {
        var registryCredentials = registryCredentialsByAccountCache.get(ncaId);
        if (CollectionUtils.isEmpty(registryCredentials)) {
            return Collections.emptyList();
        }

        var artifactTypes = RegistryFunctionMapperService.toArtifactTypes(artifactTypeEnums);
        return registryCredentials
                .stream()
                .filter(regCred -> !Sets
                        .intersection(regCred.getArtifactTypes(), artifactTypes)
                        .isEmpty())
                .map(registryFunctionMapperService::toRegistryCredentialDetailsDto)
                .toList();
    }

    public List<RegistryCredentialDetailsDto> getRegistryCredentialDtos(
            String ncaId,
            Set<ArtifactTypeEnum> artifactTypeEnums,
            Set<ProvisionedByEnum> provisionedByEnums) {
        var registryCredentials = registryCredentialsByAccountCache.get(ncaId);
        if (CollectionUtils.isEmpty(registryCredentials)) {
            return Collections.emptyList();
        }

        var artifactTypes = RegistryFunctionMapperService.toArtifactTypes(artifactTypeEnums);
        var provisionedBys = provisionedByEnums.stream()
                .map(RegistryFunctionMapperService::toProvisionedBy)
                .collect(Collectors.toSet());
        return registryCredentials
                .stream()
                .filter(regCred -> !Sets
                        .intersection(regCred.getArtifactTypes(), artifactTypes)
                        .isEmpty())
                .filter(regCred -> provisionedBys.contains(regCred.getProvisionedBy()))
                .map(registryFunctionMapperService::toRegistryCredentialDetailsDto)
                .toList();
    }

    public List<RegistryCredentialDetailsDto> getRegistryCredentialDtos(
            String ncaId,
            String hostname,
            ArtifactTypeEnum artifactTypeEnum) {
        var registryCredentials = registryCredentialsByAccountCache.get(ncaId);
        if (CollectionUtils.isEmpty(registryCredentials)) {
            return Collections.emptyList();
        }

        var normalizedHostname = registryMapperService.toNormalizedHostname(hostname);
        var artifactType = RegistryFunctionMapperService.toArtifactType(artifactTypeEnum);
        return registryCredentials
                .stream()
                .filter(rc -> rc.getArtifactTypes().contains(artifactType)
                        && rc.getRegistryHostname().equals(normalizedHostname))
                .map(registryFunctionMapperService::toRegistryCredentialDetailsDto)
                .toList();
    }

    @VisibleForTesting
    public void invalidateCache() {
        registryCredentialsByAccountCache.invalidateAll();
    }

    private void invalidateCache(String ncaId) {
        registryCredentialsByAccountCache.invalidate(ncaId);
    }

    private List<RegistryCredentialByAccountEntity> fetchRegistryCredentialsByAccount(
            String ncaId) {
        return registryCredentialsByAccountRepository.findByKeyNcaId(ncaId).toList();
    }
}
