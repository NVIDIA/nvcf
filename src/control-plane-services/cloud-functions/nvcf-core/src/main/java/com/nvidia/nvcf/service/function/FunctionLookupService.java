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
package com.nvidia.nvcf.service.function;

import static java.lang.String.format;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionLookupService {

    public static final String MESG_FUNCTION_NOT_FOUND =
            "Function id '%s': Not found";
    public static final String MESG_VERSION_NOT_FOUND =
            "Version '%s': Not found";
    public static final String MESG_INVALID_IDS =
            "Function id '%s', version '%s': Not found";
    public static final String MESG_FUNCTION_NOT_FOUND_IN_ACCOUNT =
            "Function id '%s': Not found in account '%s'";
    public static final String MESG_FUNCTION_VERSION_NOT_FOUND_IN_ACCOUNT =
            "Function id '%s', version '%s': Not found in account '%s'";
    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be empty or null";

    private final FunctionsRepository functionsRepository;

    // A single function(with a unique id) may have multiple versions. Each version will have
    // a unique version id.
    public List<FunctionEntity> lookupUsingFunctionId(UUID functionId) {
        return functionsRepository.findAllByFunctionId(functionId).toList();
    }

    public List<FunctionEntity> lookupUsingFunctionIdOrThrow(UUID functionId) {
        var functions = functionsRepository.findAllByFunctionId(functionId).toList();
        if (CollectionUtils.isEmpty(functions)) {
            var mesg = format(MESG_FUNCTION_NOT_FOUND, functionId);
            log.debug(mesg);
            throw new NotFoundException(mesg);
        }
        return functions;
    }

    public FunctionEntity lookupUsingVersionIdOrThrow(UUID versionId) {
        return functionsRepository.getByFunctionVersionId(versionId)
                .orElseThrow(() -> {
                    var mesg = format(MESG_VERSION_NOT_FOUND, versionId);
                    log.debug(mesg);
                    return new NotFoundException(mesg);
                });
    }

    // A single function(with a unique id) may have multiple versions. Each version will have
    // a unique version id.
    public FunctionEntity lookupFirstUsingFunctionId(UUID functionId) {
        return functionsRepository.findAllByFunctionId(functionId).findFirst().orElse(null);
    }

    public FunctionEntity lookupFirstUsingFunctionIdOrThrow(UUID functionId) {
        var function = lookupFirstUsingFunctionId(functionId);
        if (function == null) {
            var mesg = format(MESG_FUNCTION_NOT_FOUND, functionId);
            log.debug(mesg);
            throw new NotFoundException(mesg);
        }
        return function;
    }

    public Optional<FunctionEntity> lookupUsingFunctionIdAndVersionId(
            UUID functionId,
            UUID functionVersionId) {
        return functionsRepository.getByFunctionVersionId(functionVersionId)
                .map(function -> {
                    // Function version is the primary key now. So, we should check whether
                    // the passed in functionId matches with the one on the FunctionEntity.
                    if (!function.getFunctionId().equals(functionId)) {
                        return null;
                    }
                    return function;
                });
    }

    public FunctionEntity lookupUsingFunctionIdAndVersionIdOrThrow(
            UUID functionId,
            UUID functionVersionId) {
        var optFunction = lookupUsingFunctionIdAndVersionId(functionId, functionVersionId);
        return optFunction
                .orElseThrow(() -> {
                    var mesg = format(MESG_INVALID_IDS, functionId, functionVersionId);
                    log.debug(mesg);
                    return new NotFoundException(mesg);
                });
    }

    public FunctionEntity lookupUsingNcaIdAndFunctionIdAndVersionIdOrThrow(
            String ncaId,
            UUID functionId,
            UUID functionVersionId) {
        return functionsRepository
                .getByNcaIdAndFunctionIdAndFunctionVersionId(ncaId, functionId, functionVersionId)
                .orElseThrow(() -> {
                    var mesg = format(MESG_FUNCTION_VERSION_NOT_FOUND_IN_ACCOUNT,
                                      functionId, functionVersionId, ncaId);
                    log.debug(mesg);
                    return new NotFoundException(mesg);
                });
    }

    // There can be multiple functions with same id (but different version ids) belonging to an
    // account.
    public List<FunctionEntity> lookupUsingAccountIdAndFunctionIdOrThrow(
            String ncaId,
            UUID functionId) {
        // Lookup functions using ncaId and functionId.
        var functions = functionsRepository
                .findAllByNcaIdAndFunctionId(ncaId, functionId).toList();
        if (CollectionUtils.isEmpty(functions)) {
            var mesg = format(MESG_FUNCTION_NOT_FOUND_IN_ACCOUNT, functionId, ncaId);
            log.debug(mesg);
            throw new NotFoundException(mesg);
        }
        return functions;
    }

    public Stream<FunctionEntity> lookupUsingAccountIdAndFunctionId(
            String ncaId,
            UUID functionId) {
        return functionsRepository
                .findAllByNcaIdAndFunctionId(ncaId, functionId);
    }

    public Optional<FunctionEntity> lookupUsingAccountIdAndFunctionIdAndVersionId(
            String ncaId,
            UUID functionId,
            UUID versionId) {
        return functionsRepository
                .getByNcaIdAndFunctionIdAndFunctionVersionId(ncaId, functionId, versionId);
    }

    public FunctionEntity lookupUsingAccountIdAndFunctionIdAndVersionIdOrThrow(
            String ncaId,
            UUID functionId,
            UUID versionId) {
        return functionsRepository
                .getByNcaIdAndFunctionIdAndFunctionVersionId(ncaId, functionId, versionId)
                .orElseThrow(() -> {
                    var mesg = MESG_FUNCTION_VERSION_NOT_FOUND_IN_ACCOUNT
                                            .formatted(functionId, versionId, ncaId);
                    log.error(mesg);
                    return new NotFoundException(mesg);
                });
    }

    public Stream<FunctionEntity> lookupEntitiesUsingAccountId(String ncaId) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = String.format(MESG_BLANK_PARAMETER, "ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        return functionsRepository.findAllByNcaId(ncaId);
    }
}
