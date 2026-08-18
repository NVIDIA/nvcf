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
package com.nvidia.nvcf.service.account;

import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.nimbusds.oauth2.sdk.util.CollectionUtils;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
import com.nvidia.nvcf.persistence.client.entity.ClientEntity;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryByAccountEntity;
import com.nvidia.nvcf.rest.account.dto.AccountDetailsDto;
import com.nvidia.nvcf.rest.account.dto.AccountDto;
import com.nvidia.nvcf.rest.account.dto.CreateAccountRequest;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import com.nvidia.nvcf.service.registry.RegistryFunctionMapperService;
import com.nvidia.nvcf.service.telemetry.TelemetryMapperService;
import com.nvidia.nvcf.configuration.account.AccountLimitsProperties;
import com.nvidia.nvcf.util.NvcfConstants;
import jakarta.annotation.Nullable;
import java.time.Instant;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class AccountMapperService {
    private static final String MESG_MISSING_REGISTRY_CREDENTIALS =
            "Account '%s': Missing registry credentials";

    private final RegistryFunctionMapperService registryFunctionMapperService;
    private final TelemetryMapperService telemetryMapperService;
    private final FunctionsRepository functionsRepository;

    public static AccountDto toAccountDto(AccountEntity accountEntity) {
        var clientIds = accountEntity.getClientIds() != null ?
                accountEntity.getClientIds().stream().toList() : null;
        return AccountDto.builder()
                .name(accountEntity.getName())
                .ncaId(accountEntity.getNcaId())
                .adminClientIds(clientIds)
                .maxFunctionsAllowed(accountEntity.getMaxFunctionsAllowed())
                .maxTasksAllowed(accountEntity.getMaxTasksAllowed())
                .maxTelemetriesAllowed(accountEntity.getMaxTelemetriesAllowed())
                .maxRegistryCredentialsAllowed(accountEntity.getMaxRegistryCredentialsAllowed())
                .lastUpdatedAt(accountEntity.getLastUpdatedAt())
                .build();
    }

    @SneakyThrows
    public AccountDetailsDto toAccountDetailsDto(
            AccountEntity accountEntity,
            Stream<TelemetryByAccountEntity> telemetryByAccountEntities,
            List<RegistryCredentialDetailsDto> registryCredentialDetailsDtos) {
        var registryCredentials = toRegistryCredentialDtos(registryCredentialDetailsDtos);
        var ncaId = accountEntity.getNcaId();
        var telemetryDtos = telemetryMapperService.toTelemetryDtos(telemetryByAccountEntities);
        var currentNumberOfFunctions = (int) functionsRepository.countByNcaId(ncaId);
        return AccountDetailsDto.builder()
                .ncaId(ncaId)
                .clientIds(accountEntity.getClientIds())
                .name(accountEntity.getName())
                .registryCredentials(registryCredentials)
                .telemetries(CollectionUtils.isNotEmpty(telemetryDtos) ? telemetryDtos : null)
                .maxFunctionsAllowed(accountEntity.getMaxFunctionsAllowed())
                .currentNumberFunctions(currentNumberOfFunctions)
                .maxTasksAllowed(accountEntity.getMaxTasksAllowed())
                .maxTelemetriesAllowed(accountEntity.getMaxTelemetriesAllowed())
                .maxRegistryCredentialsAllowed(accountEntity.getMaxRegistryCredentialsAllowed())
                .lastUpdatedAt(accountEntity.getLastUpdatedAt())
                .build();
    }

    public static AccountEntity toAccountEntity(String ncaId, CreateAccountRequest request,
                                                AccountLimitsProperties accountLimitsProperties) {
        var clientIds = isNotBlank(request.adminClientId()) ?
                Set.of(request.adminClientId()) : null;
        var maxFunctions = Objects
                .requireNonNullElse(request.maxFunctionsAllowed(),
                                    accountLimitsProperties.getMaxFunctionsAllowed());
        var maxTasks = Objects
                .requireNonNullElse(request.maxTasksAllowed(),
                                    accountLimitsProperties.getMaxTasksAllowed());
        var maxTelemetries = Objects
                .requireNonNullElse(request.maxTelemetriesAllowed(),
                                    NvcfConstants.DEFAULT_MAX_TELEMETRIES_ALLOWED);
        var maxRegistryCredentials = Objects.requireNonNullElse(
                request.maxRegistryCredentialsAllowed(),
                NvcfConstants.DEFAULT_MAX_REGISTRY_CREDENTIALS_ALLOWED);
        return AccountEntity.builder()
                .ncaId(ncaId)
                .clientIds(clientIds)
                .name(request.name())
                .maxFunctionsAllowed(maxFunctions)
                .maxTasksAllowed(maxTasks)
                .maxTelemetriesAllowed(maxTelemetries)
                .maxRegistryCredentialsAllowed(maxRegistryCredentials)
                .createdAt(Instant.now())
                .lastUpdatedAt(Instant.now())
                .build();
    }

    public static Optional<ClientEntity> toClientEntity(
            String ncaId,
            String adminClientId,
            String accountName) {
        if (StringUtils.isBlank(adminClientId)) {
            return Optional.empty();
        }
        var clientEntity = ClientEntity.builder()
                .clientId(adminClientId)
                .ncaId(ncaId)
                .name(accountName)
                .createdAt(Instant.now())
                .build();
        return Optional.of(clientEntity);
    }

    @Nullable
    private List<RegistryCredentialDto> toRegistryCredentialDtos(
            List<RegistryCredentialDetailsDto> registryCredentialDetailsDtos) {
        var registryCredentials = registryCredentialDetailsDtos.stream()
                .map(registryFunctionMapperService::toRegistryCredentialDto)
                .filter(Objects::nonNull)
                .toList();

        // Accounts can be setup with no registry credentials.
        return CollectionUtils.isNotEmpty(registryCredentials) ? registryCredentials : null;
    }

}
