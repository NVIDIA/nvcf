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

import com.nvidia.nvct.rest.task.dto.SecretDto;
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

}
