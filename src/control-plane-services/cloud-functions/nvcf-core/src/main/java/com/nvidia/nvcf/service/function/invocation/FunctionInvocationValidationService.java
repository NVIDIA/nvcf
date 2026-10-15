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
package com.nvidia.nvcf.service.function.invocation;


import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.DEGRADING;
import static com.nvidia.nvcf.service.apikeys.ApiKeysService.isApiKeyAuth;
import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.authorizedFunctionMatch;
import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.privateFunctionMatch;
import static com.nvidia.nvcf.util.NvcfUtils.functionNotFoundMessage;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.PaymentRequiredException;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import jakarta.annotation.Nullable;
import java.util.HashSet;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.Function;
import java.util.function.Predicate;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionInvocationValidationService {

    private static final String MESG_FUNCTION_NOT_FOUND =
            "Function id '%s' version '%s': Specified function in account '%s' is not found";
    private static final String MESG_MAPPED_FUNCTION_USING_FID =
            "Function id '{}': Mapped to an active function with version id '{}' in account '{}'";
    private static final String MESG_FUNCTION_PAYMENT_REQUIRED =
            "Account '%s': Cloud credits expired - Please contact NVIDIA representatives";
    private static final String MESG_FUNCTION_NOT_AUTHORIZED =
            "Function '%s':  Not shared with the account '%s'";
    private static final String MESG_VERSION_NOT_AUTHORIZED =
            "Function id '%s', version '%s': Not shared with the account '%s'";
    private static final String MESG_FUNCTION_MISSING_RESOURCE_IN_API_KEY =
            "Function '%s':  Missing resource entry in ApiKey for the function";
    private static final String MESG_VERSION_MISSING_RESOURCE_IN_API_KEY =
            "Function id '%s', version '%s': Missing resource entry in ApiKey for the function";
    private static final String MESG_DEGRADED_NOT_ALLOWED =
            "Function id '%s': DEGRADED function cannot be invoked";

    private final AuthorizedPartiesService azpsService;
    private final FunctionLookupService functionLookupService;

    public record FunctionContext(String subject, String ncaId, FunctionEntity targetFunction) {

    }

    /**
     * finds functions that are invocable by the given authorized user and nca id
     */
    public List<FunctionContext> lookupAndValidateAccess(
            Authentication authentication,
            String ncaId,
            UUID functionId,
            @Nullable UUID versionId) {
        Stream<Supplier<List<FunctionEntity>>> functionSuppliers = Stream.of(
                () -> getPublicFunctions(ncaId, authentication, functionId, versionId),
                () -> getPrivateFunctions(ncaId, authentication, functionId, versionId),
                () -> getAuthorizedFunctions(ncaId, authentication, functionId, versionId));

        // Lazy evaluation with fallback.
        var candidateFunctions = functionSuppliers
                .map(Supplier::get)
                .filter(list -> !list.isEmpty()) // Only keep non-empty one.
                .findFirst()                     // Evaluate the first non-empty stream.
                .orElse(List.of())
                .stream()
                .filter(distinct(FunctionEntity::getFunctionVersionId))
                .toList();
        if (CollectionUtils.isEmpty(candidateFunctions)) {
            throw getThrowable(() -> lookupPublicFunctions(functionId, versionId),
                               () -> lookupOwnFunctions(ncaId, functionId, versionId),
                               () -> lookupAuthorizedFunctions(ncaId, functionId, versionId),
                               authentication, ncaId, functionId, versionId);
        }

        AtomicBoolean hasDegraded = new AtomicBoolean(false);
        var contexts = candidateFunctions
                .stream()
                .filter(function -> {
                    if (function.getFunctionStatus() == FunctionStatus.DEGRADED) {
                        hasDegraded.set(true);
                    }
                    return function.getFunctionStatus() == FunctionStatus.ACTIVE
                            || function.getFunctionStatus() == DEGRADING;
                })
                .map(function -> {
                    var fId = function.getFunctionId();
                    var version = function.getFunctionVersionId();
                    var subject = authentication.getName();
                    log.debug(MESG_MAPPED_FUNCTION_USING_FID, fId, version, function.getNcaId());
                    return new FunctionContext(subject, ncaId, function);
                })
                .toList();

        if (CollectionUtils.isEmpty(contexts)) {
            if (hasDegraded.get()) {
                var mesg = MESG_DEGRADED_NOT_ALLOWED.formatted(functionId);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
            var mesg = MESG_FUNCTION_NOT_FOUND.formatted(functionId, versionId, ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }

        return contexts;
    }

    private List<FunctionEntity> getPublicFunctions(
            String ncaId,
            Authentication authentication,
            UUID functionId,
            @Nullable UUID versionId) {
        return lookupPublicFunctions(functionId, versionId);
    }

    private List<FunctionEntity> getPrivateFunctions(
            String ncaId,
            Authentication authentication,
            UUID functionId,
            @Nullable UUID versionId) {
        var unfilteredOwnFunctions = lookupOwnFunctions(ncaId, functionId, versionId);
        return unfilteredOwnFunctions.stream()
                .filter(function -> privateFunctionMatch(ncaId, authentication, function))
                .toList();
    }

    private List<FunctionEntity> getAuthorizedFunctions(
            String ncaId,
            Authentication authentication,
            UUID functionId,
            @Nullable UUID versionId) {
        return lookupAuthorizedFunctions(ncaId, functionId, versionId)
                .stream()
                .filter(function -> authorizedFunctionMatch(ncaId, authentication, function))
                .toList();
    }

    private static RuntimeException getThrowable(
            Supplier<List<FunctionEntity>> publicFunctions,
            Supplier<List<FunctionEntity>> ownFunctions,
            Supplier<List<FunctionEntity>> authorizedFunctions,
            Authentication authentication,
            String ncaId,
            UUID functionId,
            @Nullable UUID versionId) {
        return publicFunctionsException(publicFunctions, ncaId)
                .orElseGet(() -> privateFunctionsException(ownFunctions,
                                                           authentication,
                                                           ncaId,
                                                           functionId,
                                                           versionId)
                        .orElseGet(() -> authorizedFunctionsException(authorizedFunctions,
                                                                      authentication,
                                                                      ncaId,
                                                                      functionId,
                                                                      versionId)
                                .orElseGet(() -> defaultFunctionNotFoundException(ncaId,
                                                                                  functionId,
                                                                                  versionId))));
    }

    private static Optional<RuntimeException> publicFunctionsException(
            Supplier<List<FunctionEntity>> publicFunctions,
            String ncaId) {
        if (CollectionUtils.isEmpty(publicFunctions.get())) {
            return Optional.empty();
        }
        var mesg = MESG_FUNCTION_PAYMENT_REQUIRED.formatted(ncaId);
        log.error(mesg);
        return Optional.of(new PaymentRequiredException(mesg));
    }

    private static Optional<RuntimeException> authorizedFunctionsException(
            Supplier<List<FunctionEntity>> authorizedFunctions,
            Authentication authentication,
            String ncaId,
            UUID functionId,
            UUID versionId) {
        if (CollectionUtils.isEmpty(authorizedFunctions.get())) {
            return Optional.empty();
        }
        var mesg = authorizedFunctionsForbiddenMessage(authentication,
                                                       ncaId,
                                                       functionId,
                                                       versionId);
        log.error(mesg);
        return Optional.of(new ForbiddenException(mesg));
    }

    private static Optional<RuntimeException> privateFunctionsException(
            Supplier<List<FunctionEntity>> privateFunctions,
            Authentication authentication,
            String ncaId,
            UUID functionId,
            UUID versionId) {
        if (CollectionUtils.isEmpty(privateFunctions.get())) {
            return Optional.empty();
        }
        var mesg = privateFunctionsForbiddenMessage(authentication,
                                                    ncaId,
                                                    functionId,
                                                    versionId);
        log.error(mesg);
        return Optional.of(new ForbiddenException(mesg));
    }

    private static RuntimeException defaultFunctionNotFoundException(
            String ncaId,
            UUID functionId,
            @Nullable UUID versionId) {
        var mesg = functionNotFoundMessage(ncaId, functionId, versionId);
        log.error(mesg);
        return new NotFoundException(mesg);
    }

    private static String authorizedFunctionsForbiddenMessage(
            Authentication authentication,
            String ncaId,
            UUID functionId,
            @Nullable UUID versionId) {
        return isApiKeyAuth(authentication)
                .map(access -> {
                    if (access.hasResourcesScopedForFunction(functionId, versionId)) {
                        if (versionId == null) {
                            return MESG_FUNCTION_NOT_AUTHORIZED.formatted(functionId, ncaId);
                        }
                        return MESG_VERSION_NOT_AUTHORIZED.formatted(functionId, versionId, ncaId);
                    } else {
                        if (versionId == null) {
                            return MESG_FUNCTION_MISSING_RESOURCE_IN_API_KEY.formatted(functionId);
                        }
                        return MESG_VERSION_MISSING_RESOURCE_IN_API_KEY.formatted(functionId,
                                                                              versionId);
                    }
                })
                .orElseGet(() -> functionNotFoundMessage(ncaId, functionId, versionId));
    }

    private static String privateFunctionsForbiddenMessage(
            Authentication authentication,
            String ncaId,
            UUID functionId,
            @Nullable UUID versionId) {
        return isApiKeyAuth(authentication)
                .map(access -> {
                    if (access.hasResourcesScopedForFunction(functionId, versionId)) {
                        return functionNotFoundMessage(ncaId, functionId, versionId);
                    } else {
                        if (versionId == null) {
                            return MESG_FUNCTION_MISSING_RESOURCE_IN_API_KEY.formatted(functionId);
                        }
                        return MESG_VERSION_MISSING_RESOURCE_IN_API_KEY.formatted(functionId,
                                                                              versionId);
                    }
                })
                .orElseGet(() -> functionNotFoundMessage(ncaId, functionId, versionId));
    }

    private List<FunctionEntity> lookupAuthorizedFunctions(
            String ncaId, UUID functionId, @Nullable UUID versionId) {
        if (versionId != null) {
            var authorizedFunction = azpsService
                    .lookupFunctionByAuthorizedAccountAndFunctionIdAndVersionId(ncaId, functionId,
                                                                                versionId);
            if (authorizedFunction.isPresent()) {
                return List.of(authorizedFunction.get());
            }
        }

        return azpsService
                .lookupFunctionsByAuthorizedAccountAndFunctionId(ncaId, functionId)
                .filter(distinct(FunctionEntity::getFunctionVersionId))
                .toList();
    }


    private List<FunctionEntity> lookupOwnFunctions(
            String ncaId, UUID functionId, @Nullable UUID versionId) {
        if (versionId != null) {
            return functionLookupService
                    .lookupUsingAccountIdAndFunctionIdAndVersionId(ncaId, functionId, versionId)
                    .map(List::of)
                    .orElseGet(List::of);
        }
        return functionLookupService
                .lookupUsingAccountIdAndFunctionId(ncaId, functionId)
                .toList();
    }

    private List<FunctionEntity> lookupPublicFunctions(
            UUID functionId, @Nullable UUID versionId) {
        if (versionId != null) {
            var function = azpsService
                    .lookupPublicFunctionsByFunctionIdAndVersionId(functionId, versionId)
                    .orElse(null);
            return (function == null) ? List.of() : List.of(function);
        }
        return azpsService.lookupPublicFunctionsByFunctionId(functionId);
    }


    private static <T> Predicate<T> distinct(Function<? super T, Object> keyExtractor) {
        Set<Object> seen = new HashSet<>();
        return t -> seen.add(keyExtractor.apply(t));
    }
}
