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
package com.nvidia.nvcf.rest.secret;

import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.privateFunctionMatch;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RequiredArgsConstructor
public class SecretManagementFacade {

    private static final String MESG_FUNCTION_MISSING_SECRETS =
            "Function id '%s', version '%s': Missing secrets in the payload";
    private static final String MESG_FUNCTION_DUPLICATE_SECRETS =
            "Function id '%s', version '%s': Duplicate secrets keys in the payload";
    private static final String MESG_FUNCTION_INVALID_FUNCTION_STATUS =
            "Function id '%s', version '%s': Add secrets only when the function is 'INACTIVE'";
    private static final String MESG_FUNCTION_NOT_FOUND =
            "Function id '%s', version '%s': Not found in account '%s'";
    private static final String MESG_FUNCTION_FORBIDDEN =
            "Function id '%s', version '%s': Forbidden to update secrets for this function";
    private static final String MESG_TELEMETRY_SECRETS_UPDATED =
            "Account '%s', telemetryId '%s' : Successfully updated secrets";

    private final EssService essService;
    private final FunctionLookupService functionLookupService;
    private final FunctionsRepository functionsRepository;

    public void updateFunctionSecrets(
            String ncaId,
            UUID functionId,
            UUID versionId,
            Set<SecretDto> secrets,
            Authentication authentication) {
        if (CollectionUtils.isEmpty(secrets)) {
            var mesg = MESG_FUNCTION_MISSING_SECRETS.formatted(functionId, versionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
        if (essService.hasDupeSecrets(secrets)) {
            var mesg = MESG_FUNCTION_DUPLICATE_SECRETS.formatted(functionId, versionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, versionId);
        if (!function.getNcaId().equals(ncaId)) {
            var mesg = MESG_FUNCTION_NOT_FOUND.formatted(functionId, versionId, ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }

        if (!privateFunctionMatch(ncaId, authentication, function)) {
            var mesg = MESG_FUNCTION_FORBIDDEN.formatted(functionId, versionId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        var existingSecrets = essService.getFunctionVersionSecrets(functionId, versionId)
                .orElseGet(Set::of)
                .stream()
                .collect(Collectors.toMap(SecretDto::name, Function.identity()));
        var status = function.getFunctionStatus();

        // Only allowed to add secrets if the function is in INACTIVE state. Secrets
        // can be updated when the function is in DEPLOYING, ACTIVE, etc. states.
        if (status != FunctionStatus.INACTIVE && existingSecrets.isEmpty()) {
            var mesg = MESG_FUNCTION_INVALID_FUNCTION_STATUS.formatted(functionId, versionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        // Get the existing secrets first and then merge them with the new secrets
        // Preferring new secrets values if the secret name is the same.
        var newSecrets = secrets.stream()
                .collect(Collectors.toMap(SecretDto::name, Function.identity()));
        var mergedSecrets = Stream
                .concat(existingSecrets.entrySet().stream(), newSecrets.entrySet().stream())
                .collect(Collectors.toMap(Map.Entry::getKey,
                                          Map.Entry::getValue,
                                          (oldValue, newValue) -> newValue));
        essService.saveFunctionVersionSecrets(functionId,
                                              versionId,
                                              new HashSet<>(mergedSecrets.values()));
        
        // Update the has_secrets field in the function entity if it's not already true
        if (!mergedSecrets.isEmpty() && !function.hasSecrets()) {
            var updatedFunction = function.toBuilder().hasSecrets(true).build();
            functionsRepository.save(updatedFunction);
        }
    }

    public void updateTelemetrySecret(String ncaId, UUID telemetryId, SecretDto secret) {
        essService.saveTelemetrySecret(ncaId, telemetryId, secret);

        // ### TODO: Set last updated timestamp on the account when it becomes available.
        log.info(MESG_TELEMETRY_SECRETS_UPDATED.formatted(ncaId, telemetryId));
    }
}
