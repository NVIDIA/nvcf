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
package com.nvidia.nvct.service.ess;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.binder.cache.CaffeineCacheMetrics;
import java.time.Duration;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

@Service
@Slf4j
public class EssService {

    private static final String CACHE_NAME = "nvctRegistryCredentialSecrets";

    private final EssClient essClient;
    private final LoadingCache<RegistryCredentialKey, SecretDto> registryCredentialSecretCache;

    private record RegistryCredentialKey(String ncaId, UUID registryCredentialId) {
    }

    public EssService(
            EssClient essClient,
            @Value("${nvct.ess.registry-credential-cache.ttl:PT5M}") Duration cacheTtl,
            @Value("${nvct.ess.registry-credential-cache.max-size:3072}") long cacheMaxSize,
            MeterRegistry meterRegistry) {
        this.essClient = essClient;
        this.registryCredentialSecretCache = Caffeine.newBuilder()
                .maximumSize(cacheMaxSize)
                .expireAfterWrite(cacheTtl)
                .scheduler(Scheduler.systemScheduler())
                .recordStats()
                .build(this::loadRegistryCredentialSecret);
        CaffeineCacheMetrics.monitor(meterRegistry, this.registryCredentialSecretCache, CACHE_NAME);
    }

    public UUID saveSecrets(UUID taskId, Set<SecretDto> secrets) {
        return essClient.saveSecrets(taskId, secrets);
    }

    public Optional<Set<String>> getSecretNames(UUID taskId) {
        return essClient.getSecretNames(taskId);
    }

    public Optional<Set<SecretDto>> getSecrets(UUID taskId) {
        var secretDtos = essClient.fetchSecrets(taskId)
                .map(secrets -> secrets.entrySet()
                        .stream()
                        .map(entry -> SecretDto.builder()
                                        .name(entry.getKey())
                                        .value(entry.getValue())
                                        .build())
                        .collect(Collectors.toSet()))
                .orElse(null);
        return Optional.ofNullable(secretDtos);
    }

    public void deleteSecrets(UUID taskId) {
        essClient.deleteSecrets(taskId);
    }

    public void deleteSecretsPath(UUID taskId) {
        essClient.deleteSecretsPath(taskId);
    }

    public boolean telemetrySecretExist(String ncaId, UUID telemetryId) {
        var existingSecrets = essClient.fetchTelemetrySecret(ncaId, telemetryId);
        return existingSecrets.isPresent() && !existingSecrets.get().isEmpty();
    }

    // Registry credential secrets are read from ESS by (ncaId, registryCredentialId) and served
    // from an in-memory cache. ESS is called only on a cache miss or after the entry expires.
    // A missing secret is not cached, so a later ESS write is picked up on the next read.
    public Optional<SecretDto> getRegistryCredentialSecret(String ncaId, UUID registryCredentialId) {
        return Optional.ofNullable(registryCredentialSecretCache.get(
                new RegistryCredentialKey(ncaId, registryCredentialId)));
    }

    private SecretDto loadRegistryCredentialSecret(RegistryCredentialKey key) {
        log.debug("Registry credential secret cache miss; fetching from ESS for account '{}' "
                          + "credential '{}'", key.ncaId(), key.registryCredentialId());
        return essClient.fetchRegistryCredentialSecret(key.ncaId(), key.registryCredentialId())
                .flatMap(secrets -> secrets.entrySet()
                        .stream()
                        .findFirst()
                        .map(entry -> SecretDto.builder()
                                .name(entry.getKey())
                                .value(entry.getValue())
                                .build()))
                .orElse(null);
    }

}
