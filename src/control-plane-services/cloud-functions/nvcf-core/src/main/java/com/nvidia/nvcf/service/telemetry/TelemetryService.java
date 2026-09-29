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
package com.nvidia.nvcf.service.telemetry;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.persistence.account.entity.AccountEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.telemetry.TelemetriesByAccountRepository;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetriesUdt;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryByAccountEntity;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryDto;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetryRequest;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import jakarta.annotation.Nullable;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.Collections;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
public class TelemetryService {

    private static final String MESG_TELEMETRY_ALREADY_EXISTS =
            "Account '%s': Telemetry and corresponding secret with name '%s' already " +
                    "exists in the account - Telemetries must have unique names within an account";
    private static final String MESG_TELEMETRY_IN_USE_CANNOT_DELETE =
            "Telemetry '%s': Cannot be deleted as it is in use by Function id '%s', version '%s' - "
                    + "Delete the function first to be able to delete the telemetry.";
    private static final String MESG_TELEMETRY_LIMIT_EXCEEDED =
            "Account '%s': Cannot create telemetry - maximum allowed telemetries (%d) exceeded. " +
                    "Current count: %d";
    private static final String MESG_TELEMETRY_SAVED =
            "Telemetry '{}': Telemetry saved in account '{}'";
    private static final String MESG_TELEMETRY_DELETED =
            "Telemetry '{}': Telemetry deleted from account '{}'";
    private static final String MESG_DELETED_SECRETS =
            "Account '{}': Deleted secrets";

    private final EssService essService;
    private final JsonMapper jsonMapper;
    private final TelemetriesByAccountRepository telemetryByAccountRepository;
    private final TelemetryMapperService telemetryMapperService;
    private final TelemetryLookupService telemetryLookupService;
    private final FunctionLookupService functionLookupService;

    public TelemetryService(
            EssService essService,
            JsonMapper jsonMapper,
            TelemetriesByAccountRepository telemetryByAccountRepository,
            TelemetryMapperService telemetryMapperService,
            TelemetryLookupService telemetryLookupService,
            FunctionLookupService functionLookupService) {
        this.essService = essService;
        this.jsonMapper = jsonMapper;
        this.telemetryByAccountRepository = telemetryByAccountRepository;
        this.telemetryMapperService = telemetryMapperService;
        this.telemetryLookupService = telemetryLookupService;
        this.functionLookupService = functionLookupService;
    }

    public record Telemetries(TelemetryDto logsTelemetry,
                              TelemetryDto metricsTelemetry,
                              TelemetryDto tracesTelemetry) { }

    public record SerializeTelemetriesDto(Telemetries telemetries) { }

    private record TelemetryContext(
            FunctionEntity functionEntity,
            TelemetryByAccountEntity telemetryByAccountEntity) {}

    public TelemetryByAccountEntity saveTelemetry(
            AccountEntity accountEntity, TelemetryRequest telemetryRequest) {
        validateTelemetryRequest(accountEntity, telemetryRequest);

        // Save the Telemetry in the DB.
        var telemetryId = UUID.randomUUID();
        var ncaId = accountEntity.getNcaId();
        var telemetryByAccountEntity = telemetryMapperService
                .toTelemetryByAccountEntity(ncaId, telemetryId, telemetryRequest);
        telemetryByAccountRepository.save(telemetryByAccountEntity);

        saveTelemetrySecret(ncaId, telemetryId, telemetryRequest.secret());

        log.info(MESG_TELEMETRY_SAVED, telemetryId, ncaId);
        return telemetryByAccountEntity;
    }

    public void deleteTelemetry(String ncaId, UUID telemetryId) {
        var telemetryEntity = telemetryLookupService
                .lookupByAccountAndTelemetryIdOrThrow(ncaId, telemetryId);
        var messages = deleteTelemetryInternal(ncaId, telemetryEntity);
        if (!CollectionUtils.isEmpty(messages)) {
            var mesg = messages.toString();
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        essService.deleteTelemetrySecret(ncaId, telemetryId);
        log.info(MESG_TELEMETRY_DELETED, telemetryId, ncaId);
    }

    public void deleteAllTelemetries(String ncaId) {
        var telemetryByAccountEntities = telemetryLookupService.lookupByAccount(ncaId).toList();
        if (telemetryByAccountEntities.isEmpty()) {
            return;
        }

        telemetryByAccountEntities.forEach(telemetryByAccountEntity -> {
            var telemetryId = telemetryByAccountEntity.getKey().getTelemetryId();
            deleteTelemetry(ncaId, telemetryId);
            essService.deleteTelemetrySecret(ncaId, telemetryId);
        });

        log.info(MESG_DELETED_SECRETS, ncaId);
    }

    public int getTelemetryCountByAccount(String ncaId) {
        return (int) telemetryByAccountRepository.countByKeyNcaId(ncaId);
    }

    @SneakyThrows
    public String base64Encode(String ncaId, TelemetriesUdt telemetriesUdt) {
        if (telemetriesUdt.getLogsTelemetryId() == null &&
                telemetriesUdt.getMetricsTelemetryId() == null &&
                telemetriesUdt.getTracesTelemetryId() == null) {
            return StringUtils.EMPTY;
        }

        // Serialized JSON should be as defined in section 4.2.5 of the SDD.
        var logsTelemetry = toTelemetryDto(ncaId, telemetriesUdt.getLogsTelemetryId());
        var metricsTelemetry = toTelemetryDto(ncaId, telemetriesUdt.getMetricsTelemetryId());
        var tracesTelemetry = toTelemetryDto(ncaId, telemetriesUdt.getTracesTelemetryId());
        var telemetries = new Telemetries(logsTelemetry.orElse(null),
                                          metricsTelemetry.orElse(null),
                                          tracesTelemetry.orElse(null));
        var json = jsonMapper.writeValueAsString(new SerializeTelemetriesDto(telemetries));
        return Base64.getEncoder().encodeToString(json.getBytes(StandardCharsets.UTF_8));
    }

    // Add a new secret to the existing set of account-specific secret. Based on the
    // validation, there will not be any existing secret with the same name as the new
    // secret specified as a parameter. However, if there is an existing account-specific
    // secret that has the same name as the name of the newly specified secret, then the
    // existing secret value will be overridden with the new secret value.
    @VisibleForTesting
    public void saveTelemetrySecret(String ncaId, UUID telemetryId, SecretDto secretDto) {
        essService.saveTelemetrySecret(ncaId, telemetryId, secretDto);
    }

    private void validateTelemetryRequest(AccountEntity accountEntity, TelemetryRequest request) {
        validateMaxTelemetryLimit(accountEntity);
        validateTelemetrySecretName(accountEntity.getNcaId(), request);
    }

    private void validateMaxTelemetryLimit(AccountEntity accountEntity) {
        var ncaId = accountEntity.getNcaId();
        var maxTelemetriesAllowed = accountEntity.getMaxTelemetriesAllowed();
        var currentTelemetryCount = getTelemetryCountByAccount(ncaId);

        if (currentTelemetryCount >= maxTelemetriesAllowed) {
            var mesg = MESG_TELEMETRY_LIMIT_EXCEEDED.formatted(ncaId, maxTelemetriesAllowed,
                                                               currentTelemetryCount);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private void validateTelemetrySecretName(String ncaId, TelemetryRequest request) {
        // Telemetries must have unique names within a NVIDIA Cloud Account. Validate
        // whether a telemetry with the same name already exists under the specified account.
        var telemetryName = request.secret().name();  // Secret name serves as the telemetry name.
        var telemetryByAccountEntities = telemetryLookupService.lookupByAccount(ncaId);
        telemetryByAccountEntities.forEach(telemetryByAccountEntity -> {
            if (telemetryByAccountEntity.getName().equals(telemetryName)) {
                var mesg = MESG_TELEMETRY_ALREADY_EXISTS.formatted(ncaId, telemetryName);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        });
    }

    private Optional<TelemetryDto> toTelemetryDto(String ncaId, UUID telemetryId) {
        if (telemetryId == null) {
            return Optional.empty();
        }

        var telemetryByAccountEntity = telemetryLookupService
                .lookupByAccountAndTelemetryIdOrThrow(ncaId, telemetryId);
        return Optional.of(telemetryMapperService.toTelemetryDto(telemetryByAccountEntity));
    }

    private List<String> deleteTelemetryInternal(
            String ncaId,
            TelemetryByAccountEntity telemetryByAccountEntity) {
        var keys = getTelemetryDependentFunctions(ncaId, telemetryByAccountEntity);
        if (CollectionUtils.isEmpty(keys)) {
            // No function is dependent on the Telemetry that is to be deleted. Proceed with
            // deletion.
            telemetryByAccountRepository.delete(telemetryByAccountEntity);
            return Collections.emptyList();
        }

        // There are one or more functions that are using the specified Telemetry. Unless those
        // function(s) are deleted, the specified Telemetry cannot be deleted.
        var telemetryId = telemetryByAccountEntity.getKey().getTelemetryId();
        return keys.stream().map(key -> toMessage(telemetryId, key)).toList();
    }

    private List<FunctionEntity> getTelemetryDependentFunctions(
            String ncaId,
            TelemetryByAccountEntity telemetryByAccountEntity) {
        var functions = functionLookupService.lookupEntitiesUsingAccountId(ncaId);

        return functions
                .map(function -> telemetryContext(function, telemetryByAccountEntity))
                .map(this::telemetryDependentFunction)
                .filter(Objects::nonNull)
                .toList();
    }

    private TelemetryContext telemetryContext(
            FunctionEntity functionEntity,
            TelemetryByAccountEntity telemetryByAccountEntity) {
        return new TelemetryContext(functionEntity, telemetryByAccountEntity);
    }

    // Returns FunctionKey if the function in the context depends on the Telemetry in the
    // context. Otherwise, returns null.
    @Nullable
    private FunctionEntity telemetryDependentFunction(TelemetryContext context) {
        var telemetries = context.functionEntity().getTelemetries();
        if (telemetries == null) {
            return null;  // Function does not have any telemetries associated with it.
        }

        var telemetryId = context.telemetryByAccountEntity().getKey().getTelemetryId();
        if (telemetryId.equals(telemetries.getLogsTelemetryId()) ||
                telemetryId.equals(telemetries.getMetricsTelemetryId()) ||
                telemetryId.equals(telemetries.getTracesTelemetryId())) {
            return context.functionEntity();
        }

        // Telemetries associated with the function in the context do not match with the telemetry
        // in the context.
        return null;
    }

    private String toMessage(UUID telemetryId, FunctionEntity functionEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        return MESG_TELEMETRY_IN_USE_CANNOT_DELETE.formatted(telemetryId, functionId, versionId);
    }
}
