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
package com.nvidia.nvcf.service.ess;

import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
@Slf4j
public class EssService {

    private final EssClient essClient;

    public UUID saveFunctionVersionSecrets(
            UUID functionId,
            UUID versionId,
            Set<SecretDto> secrets) {
        return essClient.saveFunctionVersionSecrets(functionId, versionId, secrets);
    }

    public boolean telemetrySecretExist(String ncaId, UUID telemetryId) {
        var existingSecrets = essClient.fetchTelemetrySecret(ncaId, telemetryId);
        return existingSecrets.isPresent() && !existingSecrets.get().isEmpty();
    }

    public UUID saveTelemetrySecret(String ncaId, UUID telemetryId, SecretDto secret) {
        return essClient.saveTelemetrySecret(ncaId, telemetryId, secret);
    }

    public Optional<Set<String>> getFunctionVersionSecretNames(UUID functionId, UUID versionId) {
        return essClient.getFunctionVersionSecretNames(functionId, versionId);
    }

    public Optional<Set<SecretDto>> getFunctionVersionSecrets(UUID functionId, UUID versionId) {
        var secretDtos = essClient.fetchFunctionVersionSecrets(functionId, versionId)
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

    public Optional<SecretDto> getTelemetrySecret(String ncaId, UUID telemetryId) {
        return essClient.fetchTelemetrySecret(ncaId, telemetryId)
                .flatMap(secrets -> secrets.entrySet()
                        .stream()
                        .findFirst()
                        .map(entry -> SecretDto.builder()
                                .name(entry.getKey())
                                .value(entry.getValue())
                                .build()));
    }

    public void deleteFunctionVersionSecrets(UUID functionId, UUID versionId) {
        essClient.deleteFunctionVersionSecrets(functionId, versionId);
    }

    public void deleteTelemetrySecret(String ncaId, UUID telemetryId) {
        essClient.deleteTelemetrySecret(ncaId, telemetryId);
    }

    public void deleteFunctionSecrets(UUID functionId) {
        essClient.deleteFunctionSecrets(functionId);
    }

    public boolean hasDupeSecrets(Set<SecretDto> secrets) {
        var dedupedCount = (Long) secrets.stream()
                .map(SecretDto::name)
                .distinct()
                .count();
        return dedupedCount != secrets.size();
    }

}
