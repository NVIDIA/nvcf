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

import com.nimbusds.oauth2.sdk.util.CollectionUtils;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.LlmConfigValidator;
import com.nvidia.nvcf.rest.function.management.dto.LlmInvocationConfigDto;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import jakarta.annotation.Nullable;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

/**
 * Owns the LLM-specific logic of function management: validating that llmInvocationConfig and
 * model config are only used on LLM functions, and reconciling the model-level llmConfig and the
 * function-level llmInvocationConfig across every version sharing a functionId. Extracted from
 * {@link FunctionManagementService} to keep that class focused on the general function lifecycle.
 * Also owns invocation-time priority resolution via {@link #resolveInvocationPriority}.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionLlmService {

    private static final String MESG_FUNCTION_NOT_LLM =
            "Function id '%s', version '%s': Only LLM functions support model config updates";
    private static final String MESG_FUNCTION_NOT_LLM_INVOCATION_CONFIG =
            "Only LLM functions support llmInvocationConfig";
    private static final String MESG_MODEL_CONFIG_MODEL_NOT_FOUND =
            "Function id '%s', version '%s': Model '%s' not found";
    private static final String MESG_MODEL_CONFIG_MISSING =
            "Function id '%s', version '%s': Model '%s' does not have existing llmConfig";
    private static final String MESG_LLM_URIS_IMMUTABLE =
            "Function id '%s', version '%s': 'llmConfig.uris' for model '%s' must match "
                    + "existing versions";
    private static final String MESG_LLM_TOKENIZER_IMMUTABLE =
            "Function id '%s', version '%s': 'llmConfig.tokenizer' for model '%s' must match "
                    + "existing versions";
    private static final String MESG_FUNCTION_TYPE_MISMATCH =
            "Function id '%s': all versions must share the same functionType; "
                    + "existing version '%s' is '%s'";

    private final FunctionLookupService functionLookupService;
    private final FunctionMapperService functionMapperService;

    /**
     * Resolves the caller's invocation priority: the per-account override when the caller has one,
     * otherwise the default, or empty when neither is configured.
     */
    public Optional<Long> resolveInvocationPriority(
            String ncaId, @Nullable LlmInvocationConfigDto llmInvocationConfig) {
        if (llmInvocationConfig == null || llmInvocationConfig.priority() == null) {
            return Optional.empty();
        }
        var priority = llmInvocationConfig.priority();
        var perAccountPriority = priority.perAccountPriority();
        var override = perAccountPriority == null ? null : perAccountPriority.get(ncaId);
        return Optional.ofNullable(override != null ? override : priority.defaultPriority());
    }

    /**
     * Reject a create request that sets llmInvocationConfig on a non-LLM function.
     */
    public void validateCreateFunctionRequestWithLlmConfig(CreateFunctionRequest request) {
        if (request.getLlmInvocationConfig() != null
                && request.getFunctionType() != FunctionTypeEnum.LLM) {
            log.error(MESG_FUNCTION_NOT_LLM_INVOCATION_CONFIG);
            throw new BadRequestException(MESG_FUNCTION_NOT_LLM_INVOCATION_CONFIG);
        }
    }

    /**
     * Reject an update that sets model config or llmInvocationConfig on a non-LLM function.
     */
    public void validateUpdateFunctionRequestWithLlmConfig(
            FunctionEntity function,
            UpdateFunctionRequest request) {
        if (!CollectionUtils.isEmpty(request.modelUpdates())
                && function.getFunctionType() != FunctionType.LLM) {
            var functionId = function.getFunctionId();
            var functionVersionId = function.getFunctionVersionId();
            var mesg = MESG_FUNCTION_NOT_LLM.formatted(functionId, functionVersionId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if (request.llmInvocationConfig() != null
                && function.getFunctionType() != FunctionType.LLM) {
            log.error(MESG_FUNCTION_NOT_LLM_INVOCATION_CONFIG);
            throw new BadRequestException(MESG_FUNCTION_NOT_LLM_INVOCATION_CONFIG);
        }
    }

    /**
     * Reconcile the model-level llmConfig and function-level llmInvocationConfig for a new version
     * of an existing LLM function (the caller guards on LLM version-create). Returns the siblings
     * to resave, keyed by {@code functionVersionId}.
     */
    public Map<UUID, FunctionEntity> reconcileForNewVersion(
            FunctionEntity functionEntity,
            CreateFunctionRequest request,
            List<FunctionEntity> siblings) {
        var siblingsToResave = new HashMap<UUID, FunctionEntity>();
        if (!CollectionUtils.isEmpty(request.getModels())) {
            siblingsToResave.putAll(reconcileModelLlmConfigAcrossVersions(
                    functionEntity.getFunctionId(), request.getModels(), siblings));
        }
        applyLlmInvocationConfigForNewVersion(functionEntity, siblings, siblingsToResave);
        return siblingsToResave;
    }

    /**
     * Apply model-level llmConfig and function-level llmInvocationConfig updates to the target
     * version and its siblings. Returns the siblings to resave, keyed by {@code functionVersionId}.
     */
    public Map<UUID, FunctionEntity> applyLlmUpdates(
            FunctionEntity function,
            UpdateFunctionRequest request) {
        var functionId = function.getFunctionId();
        var functionVersionId = function.getFunctionVersionId();
        var siblings = getLlmFunctions(functionId);
        var siblingsToResave = new HashMap<UUID, FunctionEntity>();
        if (!CollectionUtils.isEmpty(request.modelUpdates())) {
            updateModels(function, functionId, functionVersionId, request.modelUpdates());
            siblingsToResave.putAll(propagateModelUpdatesToSiblings(
                    functionVersionId, request.modelUpdates(), siblings));
        }
        applyLlmInvocationConfigUpdate(
                request, function, functionVersionId, siblings, siblingsToResave);
        return siblingsToResave;
    }

    /**
     * Enforce that each model's {@code uris}/{@code tokenizer} match existing siblings, and
     * propagate the payload's {@code tokenRateLimit}/{@code routingMethod} to them. Returns the
     * siblings to resave, keyed by {@code functionVersionId}; empty when nothing changed.
     */
    private Map<UUID, FunctionEntity> reconcileModelLlmConfigAcrossVersions(
            UUID functionId,
            List<FunctionModelDto> requestedModels,
            List<FunctionEntity> siblingVersions) {
        if (siblingVersions.isEmpty()) {
            return Map.of();
        }

        // Request validators already reject duplicate model names, so toMap is safe here.
        var requestedByName = requestedModels.stream()
                .collect(Collectors.toMap(FunctionModelDto::getName, Function.identity()));

        var overrides = requestedModels.stream()
                .filter(m -> m.getLlmConfig() != null)
                .filter(m -> m.getLlmConfig().getTokenRateLimit() != null
                        || m.getLlmConfig().getRoutingMethod() != null)
                .collect(Collectors.toMap(
                        FunctionModelDto::getName,
                        m -> FunctionModelDto.LlmConfigDto.builder()
                                .tokenRateLimit(m.getLlmConfig().getTokenRateLimit())
                                .routingMethod(m.getLlmConfig().getRoutingMethod())
                                .build()));

        // One pass per sibling: validate uris/tokenizer and apply overrides, re-serializing only
        // when something changed so unchanged siblings avoid a write.
        var mutated = new HashMap<UUID, FunctionEntity>();
        for (var sibling : siblingVersions) {
            var siblingModels = functionMapperService.toFunctionModels(sibling.getModelSpecs());
            var changed = false;
            for (var siblingModel : siblingModels) {
                var requested = requestedByName.get(siblingModel.getName());
                var siblingLlm = siblingModel.getLlmConfig();
                if (requested == null || siblingLlm == null) {
                    continue;
                }
                var requestedLlm = requested.getLlmConfig();
                if (requestedLlm != null) {
                    if (!Objects.equals(requestedLlm.getUris(), siblingLlm.getUris())) {
                        var mesg = MESG_LLM_URIS_IMMUTABLE.formatted(
                                functionId,
                                sibling.getFunctionVersionId(),
                                siblingModel.getName());
                        log.error(mesg);
                        throw new BadRequestException(mesg);
                    }
                    if (!Objects.equals(requestedLlm.getTokenizer(), siblingLlm.getTokenizer())) {
                        var mesg = MESG_LLM_TOKENIZER_IMMUTABLE.formatted(
                                functionId,
                                sibling.getFunctionVersionId(),
                                siblingModel.getName());
                        log.error(mesg);
                        throw new BadRequestException(mesg);
                    }
                }

                var override = overrides.get(siblingModel.getName());
                if (override == null) {
                    continue;
                }
                if (override.getTokenRateLimit() != null
                        && !Objects.equals(override.getTokenRateLimit(),
                                           siblingLlm.getTokenRateLimit())) {
                    siblingLlm.setTokenRateLimit(override.getTokenRateLimit());
                    changed = true;
                }
                if (override.getRoutingMethod() != null
                        && !Objects.equals(override.getRoutingMethod(),
                                           siblingLlm.getRoutingMethod())) {
                    siblingLlm.setRoutingMethod(override.getRoutingMethod());
                    changed = true;
                }
            }
            if (changed) {
                sibling.setModelSpecs(functionMapperService.toModelSpecs(siblingModels));
                mutated.put(sibling.getFunctionVersionId(), sibling);
            }
        }
        return mutated;
    }

    /**
     * Propagate the target version's {@code tokenRateLimit}/{@code routingMethod} overrides to
     * every other sibling so a functionId keeps one source of truth. Returns the siblings to
     * resave, keyed by {@code functionVersionId}.
     */
    private Map<UUID, FunctionEntity> propagateModelUpdatesToSiblings(
            UUID targetFunctionVersionId,
            List<UpdateFunctionRequest.ModelUpdateDto> modelUpdates,
            List<FunctionEntity> siblings) {
        // Request validators already reject duplicate model names, so toMap is safe here.
        var overrides = modelUpdates.stream()
                .filter(u -> u.llmConfig() != null)
                .filter(u -> u.llmConfig().tokenRateLimit() != null
                        || u.llmConfig().routingMethod() != null)
                .collect(Collectors.toMap(
                        UpdateFunctionRequest.ModelUpdateDto::modelName,
                        u -> FunctionModelDto.LlmConfigDto.builder()
                                .tokenRateLimit(u.llmConfig().tokenRateLimit())
                                .routingMethod(u.llmConfig().routingMethod())
                                .build()));
        if (overrides.isEmpty()) {
            return Map.of();
        }

        var mutated = new HashMap<UUID, FunctionEntity>();
        for (var sibling : siblings) {
            if (targetFunctionVersionId.equals(sibling.getFunctionVersionId())) {
                continue;
            }
            var updatedSpecs = functionMapperService.applyLlmConfigOverrides(
                    sibling.getModelSpecs(), overrides);
            if (updatedSpecs == null
                    || Objects.equals(updatedSpecs, sibling.getModelSpecs())) {
                continue;
            }
            sibling.setModelSpecs(updatedSpecs);
            mutated.put(sibling.getFunctionVersionId(), sibling);
        }
        return mutated;
    }

    /**
     * Set the new version's llmInvocationConfig: a supplied value propagates to every sibling;
     * an omitted value is inherited so all versions share one value.
     */
    private void applyLlmInvocationConfigForNewVersion(
            FunctionEntity functionEntity,
            List<FunctionEntity> siblings,
            Map<UUID, FunctionEntity> siblingsToResave) {
        if (functionEntity.getLlmConfig() != null) {
            propagateLlmInvocationConfigToSiblings(
                    functionEntity.getFunctionVersionId(), functionEntity.getLlmConfig(), siblings,
                    siblingsToResave);
        } else {
            functionEntity.setLlmConfig(findLlmInvocationConfig(siblings));
        }
    }

    /**
     * Apply an llmInvocationConfig update. A present llmInvocationConfig replaces the stored value
     * on the target version and propagates to all siblings; an omitted one preserves it.
     */
    private void applyLlmInvocationConfigUpdate(
            UpdateFunctionRequest request,
            FunctionEntity function,
            UUID functionVersionId,
            List<FunctionEntity> siblings,
            Map<UUID, FunctionEntity> siblingsToResave) {
        if (request.llmInvocationConfig() == null) {
            return;
        }
        var llmConfigJson = functionMapperService.toLlmInvocationConfigJson(
                request.llmInvocationConfig());
        function.setLlmConfig(llmConfigJson);
        propagateLlmInvocationConfigToSiblings(
                functionVersionId, llmConfigJson, siblings, siblingsToResave);
    }

    /**
     * Apply the llmInvocationConfig (as stored JSON, or null to clear) to every sibling except the
     * target, reusing any instance already staged in {@code siblingsToResave} so each sibling is
     * saved once.
     */
    private void propagateLlmInvocationConfigToSiblings(
            UUID targetFunctionVersionId,
            String llmConfigJson,
            List<FunctionEntity> siblings,
            Map<UUID, FunctionEntity> siblingsToResave) {
        for (var sibling : siblings) {
            if (targetFunctionVersionId.equals(sibling.getFunctionVersionId())) {
                continue;
            }
            var target = siblingsToResave.getOrDefault(
                    sibling.getFunctionVersionId(), sibling);
            if (!Objects.equals(target.getLlmConfig(), llmConfigJson)) {
                target.setLlmConfig(llmConfigJson);
                siblingsToResave.put(target.getFunctionVersionId(), target);
            }
        }
    }

    /**
     * Load every version sharing the functionId and require they are all LLM.
     */
    private List<FunctionEntity> getLlmFunctions(UUID functionId) {
        var functions = functionLookupService.lookupUsingFunctionId(functionId);
        requireAllSiblingsAreLlm(functionId, functions);
        return functions;
    }

    /**
     * Require every version sharing a functionId to be {@link FunctionType#LLM}, rejecting a
     * mixed-type family rather than silently skipping non-LLM siblings.
     */
    private void requireAllSiblingsAreLlm(
            UUID functionId,
            List<FunctionEntity> siblings) {
        for (var sibling : siblings) {
            if (sibling.getFunctionType() != FunctionType.LLM) {
                var mesg = MESG_FUNCTION_TYPE_MISMATCH.formatted(
                        functionId,
                        sibling.getFunctionVersionId(),
                        sibling.getFunctionType());
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        }
    }

    private void updateModels(
            FunctionEntity function,
            UUID functionId,
            UUID functionVersionId,
            List<UpdateFunctionRequest.ModelUpdateDto> modelUpdates) {
        var models = functionMapperService.toFunctionModels(function.getModelSpecs());
        for (var modelUpdate : modelUpdates) {
            var updated = false;
            for (var model : models) {
                if (!modelUpdate.modelName().equals(model.getName())) {
                    continue;
                }

                var llmConfigUpdate = modelUpdate.llmConfig();
                var llmConfig = model.getLlmConfig();
                if (llmConfig == null) {
                    var mesg = MESG_MODEL_CONFIG_MISSING.formatted(functionId,
                                                                   functionVersionId,
                                                                   modelUpdate.modelName());
                    log.error(mesg);
                    throw new BadRequestException(mesg);
                }
                if (llmConfigUpdate.tokenRateLimit() != null) {
                    LlmConfigValidator.validateTokenRateLimit(
                            modelUpdate.modelName(), llmConfigUpdate.tokenRateLimit());
                    llmConfig.setTokenRateLimit(llmConfigUpdate.tokenRateLimit());
                }
                if (llmConfigUpdate.routingMethod() != null) {
                    LlmConfigValidator.validateRoutingMethod(
                            modelUpdate.modelName(), llmConfigUpdate.routingMethod());
                    llmConfig.setRoutingMethod(llmConfigUpdate.routingMethod());
                }
                updated = true;
                break;
            }

            if (!updated) {
                var mesg = MESG_MODEL_CONFIG_MODEL_NOT_FOUND.formatted(functionId,
                                                                      functionVersionId,
                                                                      modelUpdate.modelName());
                log.error(mesg);
                throw new NotFoundException(mesg);
            }
        }
        function.setModelSpecs(functionMapperService.toModelSpecs(models));
    }

    // llmInvocationConfig stored JSON from the oldest sibling, reusing the already-loaded set.
    private String findLlmInvocationConfig(List<FunctionEntity> siblings) {
        return siblings.stream()
                .min(Comparator.comparing(FunctionEntity::getCreatedAt))
                .map(FunctionEntity::getLlmConfig)
                .orElse(null);
    }
}
