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
package com.nvidia.nvcf.service.registry;

import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.MODEL;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.RESOURCE;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.dto.Artifact;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.boot.registries.service.registry.model.ModelRegistryService;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistryService;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import java.time.Duration;
import java.util.Collections;
import java.util.Comparator;
import java.util.List;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.codec.digest.DigestUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
public class RegistryArtifactService {
    private static final int CACHE_HANDLE_LENGTH = 32;
    private static final Duration ARTIFACT_CACHE_TTL = Duration.ofMinutes(32);
    private static final String NO_ARTIFACT_TRIMMED_SHA256 = "e3b0c44298fc1c149afbf4c8996fb924";
    
    private static final String MESG_COMMON_ERROR_PREFIX =
            "Account '%s', Function '%s', Version '%s': ";
    private static final String MESG_WRONG_IMPLEMENTATION_GET_ARTIFACTS_SIZE =
            "Only resource or model registries support 'getArtifactSize' method. " +
                    "Input registry type is %s";
    private static final String MESG_WRONG_IMPLEMENTATION_FETCH_ARTIFACTS =
            "Only resource or model registries support 'fetchArtifacts' method. " +
                    "Input registry type is %s";

    private static final List<ArtifactTypeEnum> ARTIFACT_TYPES = List.of(MODEL, RESOURCE);
    private final FunctionMapperService functionMapperService;
    private final FunctionLookupService functionLookupService;
    private final RegistryCredentialFunctionService registryCredentialFunctionService;
    private final ModelRegistryService modelRegistryService;
    private final ResourceRegistryService resourceRegistryService;
    private final RegistryArtifactValidationService registryArtifactValidationService;

    public RegistryArtifactService(
            FunctionMapperService functionMapperService,
            FunctionLookupService functionLookupService,
            RegistryCredentialFunctionService registryCredentialFunctionService,
            ModelRegistryService modelRegistryService,
            ResourceRegistryService resourceRegistryService,
            RegistryArtifactValidationService registryArtifactValidationService) {
        this.functionMapperService = functionMapperService;
        this.functionLookupService = functionLookupService;
        this.registryCredentialFunctionService = registryCredentialFunctionService;
        this.modelRegistryService = modelRegistryService;
        this.resourceRegistryService = resourceRegistryService;
        this.registryArtifactValidationService = registryArtifactValidationService;
        log.info("RegistryArtifactService is loaded.");
    }

    private record CachedFunction(UUID functionId, UUID versionId) {
    }

    private final LoadingCache<CachedFunction, List<Artifact>> artifactsCache =
            Caffeine.newBuilder()
                    .maximumSize(512)
                    .expireAfterWrite(ARTIFACT_CACHE_TTL)
                    .scheduler(Scheduler.systemScheduler())
                    .build(functionKey -> fetchArtifactsUncached(functionKey.functionId(),
                                                                 functionKey.versionId()));

    private final LoadingCache<CachedFunction, Long> artifactsSizeCache =
            Caffeine.newBuilder()
                    .maximumSize(512)
                    .expireAfterWrite(ARTIFACT_CACHE_TTL)
                    .scheduler(Scheduler.systemScheduler())
                    .build(functionKey -> fetchArtifactsSizeUncached(
                            functionKey.functionId(),
                            functionKey.versionId()));

    private final LoadingCache<CachedFunction, String> artifactsCacheHandleCache =
            Caffeine.newBuilder()
                    .maximumSize(512)
                    .expireAfterWrite(ARTIFACT_CACHE_TTL)
                    .scheduler(Scheduler.systemScheduler())
                    .build(this::generateCacheHandle);

    public long getArtifactsSize(FunctionEntity function) {
        return artifactsSizeCache.get(new CachedFunction(function.getFunctionId(),
                                                         function.getFunctionVersionId()));
    }

    public List<Artifact> fetchArtifacts(
            UUID functionId, UUID functionVersionId) {
        return artifactsCache.get(new CachedFunction(functionId, functionVersionId));
    }

    public String getCacheHandle(UUID functionId, UUID functionVersionId) {
        return artifactsCacheHandleCache.get(new CachedFunction(functionId, functionVersionId));
    }

    private List<Artifact> fetchArtifactsUncached(
            UUID functionId, UUID functionVersionId) {
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, functionVersionId);
        return fetchArtifacts(function, ARTIFACT_TYPES);
    }

    private long fetchArtifactsSizeUncached(UUID functionId, UUID functionVersionId) {
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, functionVersionId);
        return getArtifactSize(function, ARTIFACT_TYPES);
    }

    @VisibleForTesting
    public void clearArtifactCache() {
        artifactsCache.invalidateAll();
        artifactsSizeCache.invalidateAll();
        artifactsCacheHandleCache.invalidateAll();
    }

    public long getArtifactSize(FunctionEntity function, List<ArtifactTypeEnum> registryTypes) {
        return registryTypes.stream()
                .map(registryType -> getArtifactSize(function, registryType))
                .mapToLong(Long::longValue)
                .sum();
    }

    public long getArtifactSize(FunctionEntity function, ArtifactTypeEnum registryType) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var ncaId = function.getNcaId();

        try {
            if (MODEL == registryType) {
                var models = functionMapperService.toFunctionModels(function.getModelSpecs());
                if (models.isEmpty()) {
                    return 0L;
                }
                var modelArtifactDetails = RegistryArtifactMapperService
                        .toArtifactDetailsFromFunctionModelDtos(models);
                if (modelArtifactDetails.isEmpty()) {
                    return 0L;
                }
                var secrets = registryCredentialFunctionService
                        .getModelRegistryCredentialValues(function, models);
                return modelRegistryService.fetchSize(modelArtifactDetails, secrets);
            } else if (RESOURCE == registryType) {
                if (function.getResources() == null) {
                    return 0L;
                }
                var resourceArtifactDetails = RegistryArtifactMapperService
                        .toArtifactDetailsFromResoureUdts(function.getResources());
                var secrets = registryCredentialFunctionService
                        .getResourceRegistryCredentialValues(function);
                return resourceRegistryService.fetchSize(resourceArtifactDetails, secrets);
            } else {
                String errMsg =
                        MESG_WRONG_IMPLEMENTATION_GET_ARTIFACTS_SIZE.formatted(registryType);
                log.error(errMsg);
                throw new IllegalStateException(errMsg);
            }
        } catch (IllegalStateException | IllegalArgumentException e) {
            String enrichedErrorMsg =
                    MESG_COMMON_ERROR_PREFIX.formatted(
                            ncaId, functionId, versionId) + e.getMessage();
            log.error(enrichedErrorMsg);
            throw new BadRequestException(enrichedErrorMsg, e);
        } catch (Exception e) {
            String enrichedErrorMsg =
                    MESG_COMMON_ERROR_PREFIX.formatted(
                            ncaId, functionId, versionId) + e.getMessage();
            log.error(enrichedErrorMsg);
            throw e;
        }
    }

    public void validateArtifacts(FunctionEntity functionEntity) {
        registryArtifactValidationService.validateArtifacts(functionEntity);
    }

    private List<Artifact> fetchArtifacts(FunctionEntity function,
                                          List<ArtifactTypeEnum> artifactTypes) {
        return artifactTypes.stream()
                .flatMap(artifactType -> fetchArtifacts(function, artifactType).stream())
                .toList();
    }

    private List<Artifact> fetchArtifacts(FunctionEntity function, ArtifactTypeEnum artifactType) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var ncaId = function.getNcaId();
        try {
            if (MODEL == artifactType) {
                var models = functionMapperService.toFunctionModels(function.getModelSpecs());
                if (models.isEmpty()) {
                    return Collections.emptyList();
                }
                var modelArtifactDetails = RegistryArtifactMapperService
                        .toArtifactDetailsFromFunctionModelDtos(models);
                if (modelArtifactDetails.isEmpty()) {
                    return Collections.emptyList();
                }
                var secrets = registryCredentialFunctionService
                        .getModelRegistryCredentialValues(function, models);
                return modelRegistryService.fetchArtifact(modelArtifactDetails, secrets);
            } else if (RESOURCE == artifactType) {
                if (function.getResources() == null) {
                    return Collections.emptyList();
                }
                var resourceArtifactDetails = RegistryArtifactMapperService
                        .toArtifactDetailsFromResoureUdts(function.getResources());
                var secrets = registryCredentialFunctionService
                        .getResourceRegistryCredentialValues(function);
                return resourceRegistryService.fetchArtifact(resourceArtifactDetails, secrets);
            } else {
                throw new IllegalStateException(
                        MESG_WRONG_IMPLEMENTATION_FETCH_ARTIFACTS.formatted(artifactType));
            }
        } catch (IllegalStateException | IllegalArgumentException e) {
            String enrichedErrorMsg =
                    MESG_COMMON_ERROR_PREFIX.formatted(
                            ncaId, functionId, versionId) + e.getMessage();
            log.error(enrichedErrorMsg);
            throw new BadRequestException(enrichedErrorMsg, e);
        } catch (Exception e) {
            String enrichedErrorMsg =
                    MESG_COMMON_ERROR_PREFIX.formatted(
                            ncaId, functionId, versionId) + e.getMessage();
            log.error(enrichedErrorMsg);
            throw e;
        }
    }

    private String generateCacheHandle(CachedFunction cachedFunction) {
        var function = functionLookupService.lookupUsingFunctionIdAndVersionIdOrThrow(
                cachedFunction.functionId(), cachedFunction.versionId());
        var functionModels = functionMapperService.toFunctionModels(function.getModelSpecs());

        Comparator<FunctionModelDto> modelComparator =
                Comparator.comparing(e -> e.getUri().toString());
        Comparator<ResourceUdt> resourceUdtComparator = Comparator.comparing(ResourceUdt::getUrl);

        var resources = CollectionUtils.isEmpty(function.getResources())
                ? List.<ResourceUdt>of() : function.getResources().stream().toList();

        var hashes = Stream.concat(
                functionModels.stream()
                        .filter(e -> e.getUri() != null)
                        .sorted(modelComparator)
                        .map(e -> DigestUtils.sha256(e.getUri().toString())),
                resources.stream().sorted(resourceUdtComparator)
                        .map(r -> DigestUtils.sha256(r.getUrl())))
                .toList();
        
        if (hashes.isEmpty()) {
            return NO_ARTIFACT_TRIMMED_SHA256;
        }
        
        var messageDigest = DigestUtils.getSha256Digest();
        for (var hash : hashes) {
            messageDigest = DigestUtils.updateDigest(messageDigest, hash);
        }
        return DigestUtils.sha256Hex(messageDigest.digest()).substring(0, CACHE_HANDLE_LENGTH);
    }
}
