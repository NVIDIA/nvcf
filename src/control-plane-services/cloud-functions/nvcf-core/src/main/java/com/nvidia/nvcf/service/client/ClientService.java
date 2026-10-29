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
package com.nvidia.nvcf.service.client;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.client.ClientsRepository;
import com.nvidia.nvcf.persistence.client.entity.ClientEntity;
import com.nvidia.nvcf.rest.client.dto.ClientDetailsDto;
import java.time.Duration;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class ClientService {

    private static final String MESG_PARAM_BLANK = "'%s' cannot be empty or null";
    private static final String MESG_CLIENT_ID_NOT_FOUND =
            "Client '%s' is not found and may not be onboarded yet";

    private final ClientsRepository clientsRepository;

    private final LoadingCache<String, ClientEntity> clientCache =
            Caffeine.newBuilder()
                    .maximumSize(512)
                    .scheduler(Scheduler.systemScheduler())
                    .expireAfterWrite(Duration.ofMinutes(60))
                    .build(this::lookupClientUncached);

    private ClientEntity lookupClientUncached(String clientId) {
        validateClientId(clientId);
        return clientsRepository.findById(clientId).orElse(null);
    }

    public Optional<ClientEntity> lookupClient(String clientId) {
        validateClientId(clientId);
        return Optional.ofNullable(clientCache.get(clientId));
    }

    public ClientEntity lookupClientOrThrow(String clientId) {
        validateClientId(clientId);

        var clientEntity = clientCache.get(clientId);
        if (clientEntity == null) {
            var mesg = String.format(MESG_CLIENT_ID_NOT_FOUND, clientId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }
        return clientEntity;
    }

    public ClientEntity saveClient(ClientEntity clientEntity) {
        return clientsRepository.save(clientEntity);
    }

    public void deleteClient(String clientId) {
        validateClientId(clientId);
        clientsRepository.deleteById(clientId);
        clientCache.invalidate(clientId);
    }

    public ClientDetailsDto getClient(String clientId) {
        validateClientId(clientId);
        return toClientDetailsDto(lookupClientOrThrow(clientId));
    }

    @VisibleForTesting
    LoadingCache<String, ClientEntity> getClientCache() {
        return clientCache;
    }

    @VisibleForTesting
    public void clearClientCache() {
        clientCache.invalidateAll();
    }

    private static ClientDetailsDto toClientDetailsDto(ClientEntity clientEntity) {
        return ClientDetailsDto.builder()
                .clientId(clientEntity.getClientId())
                .ncaId(clientEntity.getNcaId())
                .name(clientEntity.getName())
                .build();
    }

    private static void validateClientId(String clientId) {
        if (StringUtils.isBlank(clientId)) {
            var mesg = String.format(MESG_PARAM_BLANK, "clientId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }
    }
}
