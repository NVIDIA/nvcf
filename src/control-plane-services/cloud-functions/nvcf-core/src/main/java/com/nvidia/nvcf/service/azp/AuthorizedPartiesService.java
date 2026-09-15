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
package com.nvidia.nvcf.service.azp;

import static com.nvidia.nvcf.service.azp.AuthorizedPartiesMapperUtils.toAuthorizedPartiesByFunctionDto;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.google.common.collect.Sets;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesByFunctionDto;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import java.time.Duration;
import java.util.Collections;
import java.util.Comparator;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RequiredArgsConstructor
public class AuthorizedPartiesService {

    public static final String AUTHORIZED_WILDCARD_ACCOUNT = "AUTHORIZED-WILDCARD-ACCOUNT";

    private static final String MESG_AZP_FOR_FUNC_EXISTS =
            "Function id '%s': Account '%s' is already authorized to access the function";
    private static final String MESG_AZP_FOR_VERSION_EXISTS = """
            Function id '%s', version '%s': Account '%s' is already authorized to access
             the function version
            """;
    private static final String MESG_AZPS_FOR_FUNC_NOT_FOUND =
            "Function id '%s': Does not have any authorized parties to be patched";
    private static final String MESG_AZPS_FOR_VERSION_NOT_FOUND =
            "Function id '%s', version '%s': Does not have any authorized parties to be patched";
    private static final String MESG_FUNC_AZP_NOT_FOUND =
            "Function id '%s': Specified account '%s' is not an authorized party for the function";
    private static final String MESG_VERSION_AZP_NOT_FOUND = """
            Function id '%s', version '%s': Specified account '%s' is not an authorized party for
             the function version
            """;

    private final FunctionsRepository functionsRepository;
    private final FunctionLookupService functionLookupService;

    record FuncContextCacheKey(String ncaId, UUID functionId) {}

    private final LoadingCache<FuncContextCacheKey, List<FunctionEntity>>
            functionsByAuthAccountCache =
            Caffeine.newBuilder()
                    .maximumSize(128)
                    .expireAfterWrite(Duration.ofMinutes(3))
                    .scheduler(Scheduler.systemScheduler())
                    .build(this::fetchFunctionsByAuthorizedAccount);

    public AuthorizedPartiesByFunctionDto createAuthorizedParties(
            UUID functionId,
            Optional<UUID> optVersionId,
            String ncaId,
            List<AuthorizedPartyDto> authorizedParties) {
        // Wipes out existing authorized parties with the ones specified in the request.
        var newAuthParties = authorizedParties.stream()
                .map(AuthorizedPartyDto::ncaId)
                .map(authNcaId -> authNcaId.equals("*") ? AUTHORIZED_WILDCARD_ACCOUNT : authNcaId)
                .collect(Collectors.toSet());
        var functions = optVersionId
                .map(versionId -> {
                    var func = functionLookupService.lookupUsingVersionIdOrThrow(versionId);
                    func.setVersionLevelAuthorizedAccounts(newAuthParties);
                    functionsRepository.insert(func);
                    return List.of(func);
                })
                .orElseGet(() -> {
                    var funcs = functionLookupService.lookupUsingFunctionIdOrThrow(functionId);
                    funcs.forEach(func -> {
                        func.setFunctionLevelAuthorizedAccounts(newAuthParties);
                        functionsRepository.insert(func);
                    });
                    return funcs;
                });

        return toAuthorizedPartiesByFunctionDto(getOldestFunction(functions), optVersionId);
    }

    public AuthorizedPartiesByFunctionDto addAuthorizedParty(
            UUID functionId,
            Optional<UUID> optVersionId,
            AuthorizedPartyDto authorizedParty) {
        var authorizedNcaId = authorizedParty.ncaId();
        var candidateFunction = optVersionId
                .map(versionId -> {
                    var function = functionLookupService.lookupUsingVersionIdOrThrow(versionId);
                    var versionLevelAuthzParties = Objects
                            .requireNonNullElse(function.getVersionLevelAuthorizedAccounts(),
                                                Set.<String>of());
                    if (CollectionUtils.isEmpty(versionLevelAuthzParties)) {
                        var mesg = MESG_AZPS_FOR_VERSION_NOT_FOUND.formatted(functionId, versionId);
                        log.error(mesg);
                        throw new NotFoundException(mesg);
                    }
                    if (versionLevelAuthzParties.contains(authorizedNcaId)) {
                        var mesg = MESG_AZP_FOR_VERSION_EXISTS
                                .formatted(functionId, versionId, authorizedNcaId);
                        log.error(mesg);
                        throw new ConflictException(mesg);
                    }
                    var azps = Sets.union(versionLevelAuthzParties, Set.of(authorizedNcaId));
                    function.setVersionLevelAuthorizedAccounts(azps);
                    functionsRepository.insert(function);
                    return function;
                })
                .orElseGet(() -> {
                    var functions = functionLookupService.lookupUsingFunctionIdOrThrow(functionId);
                    var function = getOldestFunction(functions);
                    var funcLevelAuthzParties = Objects
                            .requireNonNullElse(function.getFunctionLevelAuthorizedAccounts(),
                                                Set.<String>of());
                    if (CollectionUtils.isEmpty(funcLevelAuthzParties)) {
                        var mesg = MESG_AZPS_FOR_FUNC_NOT_FOUND.formatted(functionId);
                        log.error(mesg);
                        throw new NotFoundException(mesg);
                    }
                    if (funcLevelAuthzParties.contains(authorizedNcaId)) {
                        var mesg = MESG_AZP_FOR_FUNC_EXISTS.formatted(functionId, authorizedNcaId);
                        log.error(mesg);
                        throw new ConflictException(mesg);
                    }
                    var azps = Sets.union(funcLevelAuthzParties, Set.of(authorizedNcaId));
                    functions.forEach(func -> {
                        func.setFunctionLevelAuthorizedAccounts(azps);
                        functionsRepository.insert(func);
                    });
                    return function;
                });

        return toAuthorizedPartiesByFunctionDto(candidateFunction, optVersionId);
    }

    public AuthorizedPartiesByFunctionDto removeAuthorizedParty(
            UUID functionId,
            Optional<UUID> optVersionId,
            AuthorizedPartyDto authorizedParty) {
        var authorizedNcaId = authorizedParty.ncaId();
        var candidateFunction = optVersionId
                .map(versionId -> {
                    var function = functionLookupService.lookupUsingVersionIdOrThrow(versionId);
                    var versionLevelAuthzParties = Objects
                            .requireNonNullElse(function.getVersionLevelAuthorizedAccounts(),
                                                Set.<String>of());
                    if (CollectionUtils.isEmpty(versionLevelAuthzParties)) {
                        var mesg = MESG_AZPS_FOR_VERSION_NOT_FOUND.formatted(functionId, versionId);
                        log.error(mesg);
                        throw new NotFoundException(mesg);
                    }
                    if (!versionLevelAuthzParties.contains(authorizedNcaId)) {
                        var mesg = MESG_VERSION_AZP_NOT_FOUND
                                .formatted(functionId, versionId, authorizedNcaId);
                        log.error(mesg);
                        throw new NotFoundException(mesg);
                    }
                    var azps = Sets.difference(versionLevelAuthzParties, Set.of(authorizedNcaId));
                    function.setVersionLevelAuthorizedAccounts(azps);
                    functionsRepository.insert(function);
                    return function;
                })
                .orElseGet(() -> {
                    var functions = functionLookupService.lookupUsingFunctionIdOrThrow(functionId);
                    var function = getOldestFunction(functions);
                    var funcLevelAuthzParties = Objects
                            .requireNonNullElse(function.getFunctionLevelAuthorizedAccounts(),
                                                Set.<String>of());
                    if (CollectionUtils.isEmpty(funcLevelAuthzParties)) {
                        var mesg = MESG_AZPS_FOR_FUNC_NOT_FOUND.formatted(functionId);
                        log.error(mesg);
                        throw new NotFoundException(mesg);
                    }
                    if (!funcLevelAuthzParties.contains(authorizedNcaId)) {
                        var mesg = MESG_FUNC_AZP_NOT_FOUND.formatted(functionId, authorizedNcaId);
                        log.error(mesg);
                        throw new NotFoundException(mesg);
                    }
                    var azps = Sets.difference(funcLevelAuthzParties, Set.of(authorizedNcaId));
                    functions.forEach(func -> {
                        func.setFunctionLevelAuthorizedAccounts(azps);
                        functionsRepository.insert(func);
                    });
                    return function;
                });

        return toAuthorizedPartiesByFunctionDto(candidateFunction, optVersionId);
    }

    public AuthorizedPartiesByFunctionDto deleteAuthorizedParties(
            UUID functionId,
            Optional<UUID> optVersionId) {
        var functions = optVersionId
                .map(versionId -> {
                    var func = functionLookupService.lookupUsingVersionIdOrThrow(versionId);
                    func.setVersionLevelAuthorizedAccounts(null);
                    functionsRepository.save(func); // Can't use insert() method because of null
                    return List.of(func);
                })
                .orElseGet(() -> {
                    var funcs = functionLookupService.lookupUsingFunctionIdOrThrow(functionId);
                    funcs.forEach(func -> {
                        func.setFunctionLevelAuthorizedAccounts(null);
                        functionsRepository.save(func); // Can't use insert() method because of null
                    });
                    return funcs;
                });

        // Response does not include any of remaining authz accounts that may still be
        // present either at the function level or the version level.
        return AuthorizedPartiesByFunctionDto.builder()
                .id(functionId)
                .ncaId(functions.getFirst().getNcaId())
                .versionId(optVersionId.orElse(null))
                .build();
    }

    public AuthorizedPartiesByFunctionDto getAuthorizedParties(
            FunctionEntity function) {
        var versionId = function.getFunctionVersionId();
        var functionLevelAzps = toAuthorizedPartiesByFunctionDto(function, Optional.empty());
        var versionLevelAzps = toAuthorizedPartiesByFunctionDto(function, Optional.of(versionId));
        return mergeAuthorizedParties(function,
                                      Optional.ofNullable(functionLevelAzps),
                                      Optional.ofNullable(versionLevelAzps));
    }

    public Stream<FunctionEntity> lookupFunctionsByAuthorizedAccount(String authNcaId) {
        var functions = functionsRepository.findAllByFunctionLevelAuthorizedAccount(authNcaId);
        var functionVersions = functionsRepository
                .findAllByVersionLevelAuthorizedAccount(authNcaId);
        return Stream.concat(functions, functionVersions);
    }

    public Stream<FunctionEntity> lookupFunctionsByAuthorizedAccountAndFunctionId(
            String authNcaId,
            UUID functionId) {
        return functionsRepository.findAllByFunctionId(functionId)
                .filter(function -> {
                    var functionLevelCheck = Objects
                            .requireNonNullElse(function.getFunctionLevelAuthorizedAccounts(),
                                                Set.<String>of()).contains(authNcaId);
                    var versionLevelCheck = Objects
                            .requireNonNullElse(function.getVersionLevelAuthorizedAccounts(),
                                                Set.<String>of()).contains(authNcaId);
                    return functionLevelCheck || versionLevelCheck;
                });
    }

    public Optional<FunctionEntity> lookupFunctionByAuthorizedAccountAndFunctionIdAndVersionId(
            String authNcaId,
            UUID functionId,
            UUID versionId) {
        return functionLookupService.lookupUsingFunctionIdAndVersionId(functionId, versionId)
                .filter(function -> {
                    var functionLevelCheck = Objects
                            .requireNonNullElse(function.getFunctionLevelAuthorizedAccounts(),
                                                Set.<String>of()).contains(authNcaId);
                    var versionLevelCheck = Objects
                            .requireNonNullElse(function.getVersionLevelAuthorizedAccounts(),
                                                Set.<String>of()).contains(authNcaId);
                    return functionLevelCheck || versionLevelCheck;
                });
    }

    public List<FunctionEntity> lookupPublicFunctions() {
        return functionsByAuthAccountCache.get(
                new FuncContextCacheKey(AUTHORIZED_WILDCARD_ACCOUNT, null));
    }

    public List<FunctionEntity> lookupPublicFunctionsByFunctionId(UUID functionId) {
        return functionsByAuthAccountCache.get(
                        new FuncContextCacheKey(AUTHORIZED_WILDCARD_ACCOUNT, functionId))
                .stream()
                .filter(function -> function.getFunctionId().equals(functionId))
                .toList();
    }

    public Optional<FunctionEntity> lookupPublicFunctionsByFunctionIdAndVersionId(
            UUID functionId,
            UUID versionId) {
        return functionsByAuthAccountCache.get(
                        new FuncContextCacheKey(AUTHORIZED_WILDCARD_ACCOUNT, functionId))
                .stream()
                .filter(function -> function.getFunctionId().equals(functionId)
                        && function.getFunctionVersionId().equals(versionId))
                .findFirst();
    }

    public AuthorizedPartiesByFunctionDto mergeAuthorizedParties(
            FunctionEntity functionEntity,
            Optional<AuthorizedPartiesByFunctionDto> functionLevelAzps,
            Optional<AuthorizedPartiesByFunctionDto> versionLevelAzps) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var ncaId = functionEntity.getNcaId();
        var builder = AuthorizedPartiesByFunctionDto.builder()
                .id(functionId)
                .versionId(versionId)
                .ncaId(ncaId);

        if (functionLevelAzps.isEmpty() && versionLevelAzps.isEmpty()) {
            return builder.authorizedParties(Collections.emptyList()).build();
        }

        var dto = functionLevelAzps.orElseGet(versionLevelAzps::get);
        var azps = dto.authorizedParties();

        if (functionLevelAzps.isPresent() && versionLevelAzps.isPresent()) {
            var flAzps = functionLevelAzps
                    .filter(azp -> azp.authorizedParties() != null)
                    .map(AuthorizedPartiesByFunctionDto::authorizedParties)
                    .orElseGet(List::<AuthorizedPartyDto>of);
            var vlAzps = versionLevelAzps
                    .filter(azp -> azp.authorizedParties() != null)
                    .map(AuthorizedPartiesByFunctionDto::authorizedParties)
                    .orElseGet(List::<AuthorizedPartyDto>of);
            azps = Stream.concat(flAzps.stream(), vlAzps.stream()).distinct().toList();
        }

        return builder.authorizedParties(azps).build();
    }

    public void verifyNotAPublicFunction(
            UUID functionId,
            Supplier<String> errorMessageSupplier) {
        if (isFunctionPublic(functionId)) {
            var mesg = errorMessageSupplier.get();
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    public void verifyNotAPublicFunction(
            UUID functionId,
            UUID versionId,
            Supplier<String> errorMessageSupplier) {
        if (isFunctionPublic(functionId, versionId)) {
            var mesg = errorMessageSupplier.get();
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    @VisibleForTesting
    public void clearPublicFunctionCache() { // Used only in tests.
        functionsByAuthAccountCache.invalidateAll();
    }

    @VisibleForTesting
    public boolean isFunctionPublic(UUID functionId) {
        return !lookupPublicFunctionsByFunctionId(functionId).isEmpty();
    }

    @VisibleForTesting
    public boolean isFunctionPublic(UUID functionId, UUID versionId) {
        return lookupPublicFunctionsByFunctionIdAndVersionId(functionId, versionId).isPresent();
    }

    private List<FunctionEntity> fetchFunctionsByAuthorizedAccount(FuncContextCacheKey key) {
        if (key.functionId() == null) {
            return fetchFunctionsByAuthorizedAccount(key.ncaId());
        }

        return fetchFunctionsByAuthorizedAccount(key.ncaId(), key.functionId());
    }

    private List<FunctionEntity> fetchFunctionsByAuthorizedAccount(
            String authNcaId, UUID functionId) {
        return functionsRepository.findAllByFunctionId(functionId)
                .filter(function -> {
                    var functionLevelAuthCheck =
                            Objects.requireNonNullElse(function.getFunctionLevelAuthorizedAccounts(),
                                                       Set.<String>of()).contains(authNcaId);
                    var versionLevelAuthCheck =
                            Objects.requireNonNullElse(function.getVersionLevelAuthorizedAccounts(),
                                                       Set.<String>of()).contains(authNcaId);
                    return functionLevelAuthCheck || versionLevelAuthCheck;
                })
                .toList();
    }

    private List<FunctionEntity> fetchFunctionsByAuthorizedAccount(String authNcaId) {
        var authAtFunctionLevel = functionsRepository
                .findAllByFunctionLevelAuthorizedAccount(authNcaId);
        var authAtVersionLevel = functionsRepository
                .findAllByVersionLevelAuthorizedAccount(authNcaId);
        return Stream.concat(authAtFunctionLevel, authAtVersionLevel).toList();
    }

    private static FunctionEntity getOldestFunction(List<FunctionEntity> functions) {
        return functions.stream()
                .min(Comparator.comparing(FunctionEntity::getCreatedAt))
                .orElse(null);
    }
}
