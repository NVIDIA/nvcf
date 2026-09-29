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
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.service.ess.EssClient;
import java.time.Duration;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
@Slf4j
public class RegistryCredentialEssService {

    private record RegistryCredentialKey(String ncaId, UUID registryCredentialId) {}

    private final EssClient essClient;
    private final LoadingCache<RegistryCredentialKey, SecretDto>
            registryCredentialSecretsCache =
                Caffeine.newBuilder()
                        .maximumSize(3 * 1024)    // 3 times approx number of accounts in Prod env.
                        .expireAfterWrite(Duration.ofMinutes(5))
                        .scheduler(Scheduler.systemScheduler())
                        .build(this::fetchRegistryCredentialSecret);

    public UUID saveRegistryCredentialSecret(
            String ncaId,
            UUID registryCredentialId,
            SecretDto secret) {
        var uuid = essClient.saveRegistryCredentialSecret(ncaId, registryCredentialId, secret);
        invalidateCache(ncaId, registryCredentialId);
        return uuid;
    }

    public Optional<SecretDto> getRegistryCredentialSecret(
            String ncaId,
            UUID registryCredentialId) {
        var key = new RegistryCredentialKey(ncaId, registryCredentialId);
        return Optional.ofNullable(registryCredentialSecretsCache.get(key));
    }

    public void deleteRegistryCredentialSecret(
            String ncaId,
            UUID registryCredentialId) {
        essClient.deleteRegistryCredentialSecret(ncaId, registryCredentialId);
        invalidateCache(ncaId, registryCredentialId);
    }

    @VisibleForTesting
    public void invalidateCache() {
        registryCredentialSecretsCache.invalidateAll();
    }

    private void invalidateCache(String ncaId, UUID registryCredentialId) {
        var key = new RegistryCredentialKey(ncaId, registryCredentialId);
        registryCredentialSecretsCache.invalidate(key);
    }

    private SecretDto fetchRegistryCredentialSecret(RegistryCredentialKey key) {
        return essClient
                .fetchRegistryCredentialSecret(key.ncaId(), key.registryCredentialId())
                .flatMap(secretMap -> secretMap.entrySet()
                        .stream()
                        .findFirst()
                        .map(entry -> SecretDto.builder()
                                .name(entry.getKey())
                                .value(entry.getValue())
                                .build()))
                .orElse(null);
    }
}
