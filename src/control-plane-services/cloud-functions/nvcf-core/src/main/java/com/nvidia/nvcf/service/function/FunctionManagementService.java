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

import static com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType.LOGS;
import static com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType.METRICS;
import static com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType.TRACES;
import static java.lang.String.format;

import com.nimbusds.oauth2.sdk.util.CollectionUtils;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.persistence.telemetry.entity.TelemetryType;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.InstanceDto;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.VisibilityEnum;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetriesDto;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.service.instance.InstanceService;
import com.nvidia.nvcf.service.registry.RegistryArtifactService;
import com.nvidia.nvcf.service.telemetry.TelemetryLookupService;
import java.util.Collection;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.BooleanSupplier;
import java.util.function.Function;
import java.util.function.Predicate;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionManagementService {

    private static final String MESG_NCA_ID_BLANK = "'ncaId' cannot be empty or null";
    private static final String MESG_DUPLICATE_SECRETS =
            "Function id '%s', version '%s': Duplicate secrets keys in the payload";
    private static final String MESG_CREATE_FUNCTION =
            "Function id '{}': {} new version '{}' with name '{}' in acct '{}'";
    private static final String MESG_FUNCTION_OPERATION =
            "Function id '{}', version id '{}': {}";
    private static final String MESG_RETRIEVE_FUNCTIONS_BY_ACC = "Account '{}': {} functions";
    private static final String MESG_RETRIEVE_FUNCTIONS =
            "Function id '{}': {} function versions";
    private static final String MESG_DELETE_SECRET_ENTITY =
            "Function id '{}': Delete secrets entity, if defined";
    private static final String MESG_FUNCTION_VERSION_NOT_IN_ACCOUNT =
            "Function id '%s', version '%s': Not found in account '%s'";
    private static final String MESG_FUNCTION_NOT_IN_ACCOUNT =
            "Function id '%s': Not found in account '%s'";
    private static final String MESG_TELEMETRY_INVALID_TYPE =
            "Invalid telemetry type";
    private static final String MESG_MAX_ALLOWED_EXCEEDED =
            "Account '%s': Reached or exceeded the max limit for the number of functions " +
                    "allowed: %d. Please contact your account team for more details";
    private static final String MESG_FORBIDDEN_TO_CREATE_FUNC =
            "Forbidden to create function";
    private static final String MESG_FORBIDDEN_TO_CREATE_VERSION =
            "Function id '%s': Forbidden to create function version";
    private static final String MESG_FORBIDDEN_TO_DELETE =
            "Function id '%s', version '%s': Forbidden to delete";
    private static final String MESG_FORBIDDEN_TO_UPDATE =
            "Function id '%s', version '%s': Forbidden to update";
    private static final String MESG_FUNCTION_TYPE_MISMATCH =
            "Function id '%s': all versions must share the same functionType; "
                    + "existing version '%s' is '%s'";
    private static final String MESG_FORBIDDEN_TO_RETRIEVE =
            "Function id '%s', version '%s': Forbidden to retrieve function details";
    private static final String MESG_FORBIDDEN_TO_RETRIEVE_VERSIONS =
            "Function id '%s': Forbidden to retrieve versions";

    private final AccountService accountService;
    private final AuthorizedPartiesService azpsService;
    private final FunctionAuditService functionAuditService;
    private final FunctionLookupService functionLookupService;
    private final FunctionMapperService functionMapperService;
    private final FunctionLlmService functionLlmService;
    private final JsonMapper jsonMapper;
    private final InstanceService instanceService;
    private final FunctionDeploymentService deploymentService;
    private final EssService essService;
    private final TelemetryLookupService telemetryLookupService;
    private final FunctionsRepository functionsRepository;
    private final RegistryArtifactService registryArtifactService;

    public FunctionDto registerFunction(
            String ncaId,
            Optional<UUID> optFunctionId,
            CreateFunctionRequest request,
            BooleanSupplier createFunctionMatch,
            AuditEventPayload.Builder payloadBuilder) {

        preRegistrationValidation(ncaId, optFunctionId, createFunctionMatch);
        validateTelemetry(ncaId, request.getTelemetries());
        functionLlmService.validateCreateFunctionRequestWithLlmConfig(request);

        var functionLevelAuthzAccounts = optFunctionId.map(this::findAuthNcaIds).orElse(null);
        var functionEntity = functionMapperService.toFunctionEntity(
                ncaId, optFunctionId, request, functionLevelAuthzAccounts);
        var functionId = functionEntity.getFunctionId();
        var functionVersionId = functionEntity.getFunctionVersionId();
        registryArtifactService.validateArtifacts(functionEntity);

        // For a new version of an existing function, read the sibling set once. LLM families must
        // stay uniformly LLM, so when the new version or any existing version is LLM, reject a
        // create-version whose type differs from the existing versions. For an LLM function, also reconcile the model-level
        // llmConfig (uris/tokenizer immutable; tokenRateLimit/routingMethod propagated) and the
        // function-level llmInvocationConfig across all sibling versions.
        var siblingsToResave = new HashMap<UUID, FunctionEntity>();
        if (optFunctionId.isPresent()) {
            var siblings = functionLookupService.lookupUsingFunctionId(functionId);
            if (functionEntity.getFunctionType() == FunctionType.LLM
                    || siblings.stream().anyMatch(s -> s.getFunctionType() == FunctionType.LLM)) {
                requireVersionTypeMatchesSiblings(
                        functionId, functionEntity.getFunctionType(), siblings);
            }
            if (functionEntity.getFunctionType() == FunctionType.LLM) {
                siblingsToResave.putAll(
                        functionLlmService.reconcileForNewVersion(functionEntity, request, siblings));
            }
        }

        var name = request.getName();
        log.info(MESG_CREATE_FUNCTION, functionId, "Registering", functionVersionId, name, ncaId);
        var secretNames = saveSecrets(request.getSecrets(), functionId, functionVersionId);
        functionsRepository.save(functionEntity);
        for (var sibling : siblingsToResave.values()) {
            functionsRepository.save(sibling);
        }

        var functionDto = functionMapperService.toFunctionDto(
                functionEntity, Optional.empty(), secretNames);
        functionAuditService.auditFunctionCreate(payloadBuilder, functionEntity);
        log.info(MESG_CREATE_FUNCTION, functionId, "Registered", functionVersionId, name, ncaId);
        return functionDto;
    }

    /**
     * Returns list of DTO objects corresponding to all the versions of all the functions
     * that are either -- a. owned by the specified account, b. authorized to be
     * invoked/listed by the specified account, or c. public functions
     *
     * @param ncaId                            NVIDIA Cloud Account Id
     * @param authFilterForPrivateFunctions    filter to determine if function in own account should
     *                                         be included
     * @param authFilterForAuthorizedFunctions filter to determine if function in authorized account
     *                                         should be included
     * @param visibilities                     private,authorized,public
     * @return list of DTOs
     */
    public List<FunctionDto> getFunctions(
            String ncaId,
            Predicate<FunctionEntity> authFilterForPrivateFunctions,
            Predicate<FunctionEntity> authFilterForAuthorizedFunctions,
            Set<VisibilityEnum> visibilities) {
        if (StringUtils.isBlank(ncaId)) {
            log.error(MESG_NCA_ID_BLANK);
            throw new BadRequestException(MESG_NCA_ID_BLANK);
        }

        log.debug(MESG_RETRIEVE_FUNCTIONS_BY_ACC, ncaId, "Retrieving");
        List<FunctionDto> publicFunctions = List.of();
        if (visibilities.contains(VisibilityEnum.PUBLIC)) {
            publicFunctions = azpsService.lookupPublicFunctions()
                    .stream()
                    .map(func -> toFunctionDto(func, false, false))
                    .filter(Objects::nonNull)
                    .toList();
        }
        log.debug(MESG_RETRIEVE_FUNCTIONS_BY_ACC, ncaId, "Retrieved public");

        List<FunctionDto> privateFunctions = List.of();
        if (visibilities.contains(VisibilityEnum.PRIVATE)) {
            privateFunctions = functionLookupService.lookupEntitiesUsingAccountId(ncaId)
                    .filter(authFilterForPrivateFunctions)
                    .map(func -> toFunctionDto(func, false, false))
                    .filter(Objects::nonNull)
                    .toList();
        }
        log.debug(MESG_RETRIEVE_FUNCTIONS_BY_ACC, ncaId, "Retrieved own");

        List<FunctionDto> authorizedFunctions = List.of();
        if (visibilities.contains(VisibilityEnum.AUTHORIZED)) {
            authorizedFunctions = azpsService.lookupFunctionsByAuthorizedAccount(ncaId)
                    .filter(authFilterForAuthorizedFunctions)
                    .map(func -> toFunctionDto(func, false, false))
                    .filter(Objects::nonNull)
                    .toList();
        }
        log.debug(MESG_RETRIEVE_FUNCTIONS_BY_ACC, ncaId, "Retrieved authorized");

        return Stream.of(publicFunctions, privateFunctions, authorizedFunctions)
                .flatMap(Collection::stream)
                .filter(distinct(FunctionDto::versionId))
                .toList();
    }

    /**
     * Returns list of DTO objects corresponding to all the versions of the specified function.
     *
     * @param ncaId                            NVIDIA Cloud Account Id
     * @param functionId                       ID of the function
     * @param authFilterForPrivateFunctions    filter to determine if function in own account should
     *                                         be included
     * @param authFilterForAuthorizedFunctions filter to determine if function in authorized account
     *                                         should be included
     * @return list of DTOs
     */
    public List<FunctionDto> getFunctions(
            String ncaId,
            UUID functionId,
            Predicate<FunctionEntity> authFilterForPrivateFunctions,
            Predicate<FunctionEntity> authFilterForAuthorizedFunctions) {
        if (StringUtils.isBlank(ncaId)) {
            log.error(MESG_NCA_ID_BLANK);
            throw new BadRequestException(MESG_NCA_ID_BLANK);
        }

        log.debug(MESG_RETRIEVE_FUNCTIONS, functionId, "Retrieving");
        var publicFunctionDtos = azpsService
                .lookupPublicFunctionsByFunctionId(functionId)
                .stream()
                .map(func -> toFunctionDto(func, false, false))
                .filter(Objects::nonNull)
                .toList();
        log.debug(MESG_RETRIEVE_FUNCTIONS_BY_ACC, ncaId, "Retrieved public");

        var privateFunctions = functionLookupService
                .lookupUsingAccountIdAndFunctionId(ncaId, functionId).toList();
        var privateFunctionCount = privateFunctions.size();
        var privateFunctionDtos = privateFunctions
                .stream()
                .filter(authFilterForPrivateFunctions)
                .map(func -> toFunctionDto(func, false, false))
                .filter(Objects::nonNull)
                .toList();
        log.debug(MESG_RETRIEVE_FUNCTIONS, functionId, "Retrieved own");

        var authorizedFunctions = azpsService
                .lookupFunctionsByAuthorizedAccountAndFunctionId(ncaId, functionId).toList();
        var authorizedFunctionsCount = authorizedFunctions.size();
        var authorizedFunctionDtos = authorizedFunctions
                .stream()
                .filter(authFilterForAuthorizedFunctions)
                .map(func -> toFunctionDto(func, false, false))
                .filter(Objects::nonNull)
                .toList();

        log.debug(MESG_RETRIEVE_FUNCTIONS_BY_ACC, ncaId, "Retrieved auth");

        var dtos = Stream.of(publicFunctionDtos, privateFunctionDtos, authorizedFunctionDtos)
                .flatMap(Collection::stream)
                .filter(distinct(FunctionDto::versionId))
                .toList();
        if (CollectionUtils.isEmpty(dtos)) {
            // Check whether we ended with an empty list of functions because the auth filters
            // were responsible for filtering them away. If so, we want to respond with 403.
            if ((privateFunctionCount > 0) || (authorizedFunctionsCount > 0)) {
                var mesg = MESG_FORBIDDEN_TO_RETRIEVE_VERSIONS.formatted(functionId);
                log.error(mesg);
                throw new ForbiddenException(mesg);
            }

            var mesg = MESG_FUNCTION_NOT_IN_ACCOUNT.formatted(functionId, ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }
        return dtos;
    }

    /**
     * Returns DTO object for the specified version of the function owned by the specified account
     * or authorized for the specified account.
     *
     * @param ncaId                           NVIDIA Cloud Account Id
     * @param functionId                      ID of the function
     * @param versionId                       Version id
     * @param authFilterForPrivateFunction    filter to determine if function in own account should
     *                                        be included
     * @param authFilterForAuthorizedFunction filter to determine if function in authorized account
     *                                        should be included
     * @param includeDetails                  Include instance details in the response
     * @param includeSecrets                  Include secret names in the response
     * @return DTO
     */
    public FunctionDto getFunction(
            String ncaId,
            UUID functionId,
            UUID versionId,
            Predicate<FunctionEntity> authFilterForPrivateFunction,
            Predicate<FunctionEntity> authFilterForAuthorizedFunction,
            boolean includeDetails,
            boolean includeSecrets) {
        log.debug(MESG_FUNCTION_OPERATION, functionId, versionId, "Getting function details");

        var publicFunction = azpsService
                .lookupPublicFunctionsByFunctionIdAndVersionId(functionId, versionId);
        var publicFunctionDto = publicFunction
                .map(func -> toFunctionDto(
                        func, ncaId.equals(func.getNcaId()) && includeDetails, includeSecrets));
        if (publicFunctionDto.isPresent()) {
            log.debug(MESG_FUNCTION_OPERATION, functionId, versionId, "Return function details");
            return publicFunctionDto.get();
        }

        var privateFunctionDto = functionLookupService
                .lookupUsingAccountIdAndFunctionIdAndVersionId(ncaId, functionId, versionId);
        if (privateFunctionDto.isPresent()) {
            return privateFunctionDto.filter(authFilterForPrivateFunction)
                    .map(func -> toFunctionDto(func, includeDetails, includeSecrets))
                    .orElseThrow(() -> {
                        // If the auth filter is responsible for the Optional not having a value,
                        // then, we want to respond with 403.
                        var mesg = MESG_FORBIDDEN_TO_RETRIEVE.formatted(functionId, versionId);
                        log.error(mesg);
                        return new ForbiddenException(mesg);
                    });
        }

        var authorizedFunction = azpsService
                .lookupFunctionByAuthorizedAccountAndFunctionIdAndVersionId(ncaId,
                                                                            functionId, versionId);
        if (authorizedFunction.isPresent()) {
            return authorizedFunction
                    .filter(authFilterForAuthorizedFunction)
                    .map(func -> toFunctionDto(func,
                                               ncaId.equals(func.getNcaId()) && includeDetails,
                                               includeSecrets))
                    .orElseThrow(() -> {
                        // If the auth filter is responsible for the Optional not having a value,
                        // then, we want to respond with 403.
                        var mesg = MESG_FORBIDDEN_TO_RETRIEVE.formatted(functionId, versionId);
                        log.error(mesg);
                        return new ForbiddenException(mesg);
                    });
        }

        var mesg = MESG_FUNCTION_VERSION_NOT_IN_ACCOUNT.formatted(functionId, versionId, ncaId);
        log.error(mesg);
        throw new NotFoundException(mesg);
    }


    public void deleteFunction(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            Predicate<FunctionEntity> authForFuncDeletion,
            AuditEventPayload.Builder payloadBuilder) {
        var function = functionLookupService
                .lookupUsingNcaIdAndFunctionIdAndVersionIdOrThrow(ncaId, functionId,
                                                                  functionVersionId);
        if (!authForFuncDeletion.test(function)) {
            var mesg = MESG_FORBIDDEN_TO_DELETE.formatted(functionId, functionVersionId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        log.info(MESG_FUNCTION_OPERATION, functionId, functionVersionId, "Deleting");

        // Delete vestiges of the function from various tables.
        functionsRepository.delete(function);

        // If the function is currently deployed, delete the queue, the Workers associated
        // with the function, and the deployment.
        deploymentService.forceDeleteFunctionDeployment(functionVersionId);

        // Delete any secrets associated with the function
        essService.deleteFunctionVersionSecrets(functionId, functionVersionId);

        // If there are no more versions of a function, we should delete the function-specific
        // secrets path in ESS and also delete any remaining vestiges such as authorized parties
        // for wildcard version from azps tables.
        try {
            functionLookupService.lookupUsingFunctionIdOrThrow(functionId);
        } catch (NotFoundException ex) {
            log.info(MESG_DELETE_SECRET_ENTITY, functionId);
            essService.deleteFunctionSecrets(functionId);
        }
        functionAuditService.auditFunctionDelete(payloadBuilder, function);
        log.info(MESG_FUNCTION_OPERATION, functionId, functionVersionId, "Deleted");
    }

    public FunctionDto updateFunction(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            Predicate<FunctionEntity> authForFuncUpdate,
            UpdateFunctionRequest request,
            AuditEventPayload.Builder auditEventPayloadBuilder) {
        var function = functionLookupService
                .lookupUsingNcaIdAndFunctionIdAndVersionIdOrThrow(ncaId, functionId,
                                                                  functionVersionId);
        var jsonBefore = jsonMapper.valueToTree(function);

        if (!authForFuncUpdate.test(function)) {
            var mesg = MESG_FORBIDDEN_TO_UPDATE.formatted(functionId, functionVersionId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        functionLlmService.validateUpdateFunctionRequestWithLlmConfig(function, request);
        if (request.tags() != null) {
            function.setTags(request.tags());
        }
        if (request.rateLimit() != null) {
            function.setRateLimit(
                    functionMapperService.toRateLimitUdt(request.rateLimit()));
        }
        var siblingsToResave = new HashMap<UUID, FunctionEntity>();
        if (function.getFunctionType() == FunctionType.LLM) {
            siblingsToResave.putAll(functionLlmService.applyLlmUpdates(function, request));
        }

        functionsRepository.save(function);
        for (var sibling : siblingsToResave.values()) {
            functionsRepository.save(sibling);
        }
        var functionDto = functionMapperService.toFunctionDto(
                function, Optional.empty(), Optional.empty());
        log.info(MESG_FUNCTION_OPERATION, functionId, functionVersionId,
                 "Function updated");
        functionAuditService.auditFunctionUpdate(auditEventPayloadBuilder, jsonBefore, function);
        return functionDto;
    }

    /**
     * Reject a create-version whose type differs from the existing versions. The caller invokes
     * this only when the new version or an existing sibling is {@link FunctionType#LLM}: LLM
     * families must stay uniformly LLM, while mixing among non-LLM types is tolerated for
     * backward compatibility. Note an omitted {@code functionType} defaults to {@code DEFAULT},
     * so a new version of an LLM function must set the type explicitly.
     */
    private void requireVersionTypeMatchesSiblings(
            UUID functionId, FunctionType newType, List<FunctionEntity> siblings) {
        var normalizedNewType = normalizeFunctionType(newType);
        for (var sibling : siblings) {
            // Legacy rows may store a null function_type, which is semantically DEFAULT (see the
            // mapper's null handling). Normalize both sides so such a row is not falsely rejected.
            var siblingType = normalizeFunctionType(sibling.getFunctionType());
            if (siblingType != normalizedNewType) {
                var mesg = MESG_FUNCTION_TYPE_MISMATCH.formatted(
                        functionId,
                        sibling.getFunctionVersionId(),
                        siblingType);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        }
    }

    /** A null stored {@link FunctionType} is treated as {@link FunctionType#DEFAULT}. */
    private static FunctionType normalizeFunctionType(FunctionType type) {
        return type == null ? FunctionType.DEFAULT : type;
    }

    private void validateTelemetry(String ncaId, TelemetriesDto telemetry) {
        if (telemetry == null) {
            return;
        }

        if (telemetry.logsTelemetryId() != null) {
            validateTelemetryType(ncaId, telemetry.logsTelemetryId(), LOGS);
        }

        if (telemetry.metricsTelemetryId() != null) {
            validateTelemetryType(ncaId, telemetry.metricsTelemetryId(), METRICS);
        }

        if (telemetry.tracesTelemetryId() != null) {
            validateTelemetryType(ncaId, telemetry.tracesTelemetryId(), TRACES);
        }
    }

    private void validateTelemetryType(
            String ncaId,
            UUID telemetryId,
            TelemetryType expectedType) {
        var telemetryEntity = telemetryLookupService
                .lookupByAccountAndTelemetryIdOrThrow(ncaId, telemetryId);

        if (!telemetryEntity.getTypes().contains(expectedType)) {
            log.error(MESG_TELEMETRY_INVALID_TYPE);
            throw new BadRequestException(MESG_TELEMETRY_INVALID_TYPE);
        }
    }

    private void preRegistrationValidation(
            String ncaId,
            Optional<UUID> optFunctionId,
            BooleanSupplier authForFuncCreation) {
        // Check account exists. This would throw NotFoundException, if account does not
        // exist.
        var accountEntity = accountService.getAccount(ncaId);

        // If a new version of an existing function is being created, make sure that the existing
        // function belongs to the specified account.
        optFunctionId.ifPresent(functionId -> functionLookupService
                .lookupUsingAccountIdAndFunctionIdOrThrow(ncaId, functionId));

        // Check if the auth token allows function creation. In case of ApiKey, this can be
        // the resource entry in the policy result. No-op for JWT and others.
        if (!authForFuncCreation.getAsBoolean()) {
            var mesg = optFunctionId
                    .map(MESG_FORBIDDEN_TO_CREATE_VERSION::formatted)
                    .orElse(MESG_FORBIDDEN_TO_CREATE_FUNC);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        // New function is going to be created, check if total number of functions in the account
        // below the threshold.
        var maxFunctionsAllowed = accountEntity.getMaxFunctionsAllowed();
        if (optFunctionId.isEmpty()
                && functionsRepository.countByNcaId(ncaId) >= maxFunctionsAllowed) {
            var mesg = format(MESG_MAX_ALLOWED_EXCEEDED, ncaId, maxFunctionsAllowed);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private FunctionDto toFunctionDto(
            FunctionEntity function,
            boolean includeDetails,
            boolean includeSecrets) {
        var instances = getActiveInstancesForFunction(function, includeDetails);
        var secretNames = getSecretNames(function, includeDetails, includeSecrets);
        return functionMapperService.toFunctionDto(function, instances, secretNames);
    }

    private Optional<List<InstanceDto>> getActiveInstancesForFunction(
            FunctionEntity function,
            boolean includeDetails) {
        var ncaId = function.getNcaId();
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        return includeDetails ?
                Optional.ofNullable(instanceService.getActiveInstancesForFunction(
                        ncaId, functionId, versionId)) : Optional.empty();
    }

    private Optional<Set<String>> getSecretNames(
            FunctionEntity function,
            boolean includeDetails,
            boolean includeSecrets) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var hasSecrets = function.hasSecrets();
        return includeDetails && includeSecrets && hasSecrets ?
                essService.getFunctionVersionSecretNames(functionId, versionId) : Optional.empty();
    }

    private Optional<Set<String>> saveSecrets(
            Set<SecretDto> secrets,
            UUID functionId,
            UUID versionId) {
        if (CollectionUtils.isNotEmpty(secrets)) {
            if (essService.hasDupeSecrets(secrets)) {
                var mesg = MESG_DUPLICATE_SECRETS.formatted(functionId, versionId);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }

            essService.saveFunctionVersionSecrets(functionId, versionId, secrets);
            return Optional.of(secrets.stream()
                                       .map(SecretDto::name)
                                       .collect(Collectors.toSet()));
        }
        return Optional.empty();
    }

    // finds and return function-level authorized accounts from the first version of the function
    private Set<String> findAuthNcaIds(UUID functionId) {
        return functionsRepository.findAllByFunctionId(functionId)
                .min(Comparator.comparing(FunctionEntity::getCreatedAt))
                .map(FunctionEntity::getFunctionLevelAuthorizedAccounts)
                .orElse(null);
    }

    private static <T> Predicate<T> distinct(Function<? super T, Object> keyExtractor) {
        Set<Object> seen = new HashSet<>();
        return t -> seen.add(keyExtractor.apply(t));
    }
}
