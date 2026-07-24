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
package com.nvidia.nvct.service.registry;

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
import com.nvidia.nvct.persistence.task.entity.ModelUdt;
import com.nvidia.nvct.persistence.task.entity.ResourceUdt;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.service.task.TaskService;
import java.time.Duration;
import java.util.Collections;
import java.util.Comparator;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.codec.digest.DigestUtils;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@AllArgsConstructor
public class RegistryArtifactService {
    public static final Duration ICMS_MAX_REQUEST_DURATION = Duration.ofMinutes(32);

    private static final int CACHE_HANDLE_LENGTH = 32;
    private static final String NO_ARTIFACT_TRIMMED_SHA256 = "e3b0c44298fc1c149afbf4c8996fb924";
    private static final List<ArtifactTypeEnum> ARTIFACT_TYPES = List.of(MODEL, RESOURCE);

    private static final String MESG_COMMON_ERROR_PREFIX =
            "Account '%s', Task '%s': ";
    private static final String MESG_WRONG_IMPLEMENTATION_GET_ARTIFACTS_SIZE =
            "Only resource or model registries support 'getArtifactSize' method. " +
                    "Input registry type is %s";
    private static final String MESG_WRONG_IMPLEMENTATION_FETCH_ARTIFACTS =
            "Only resource or model registries support 'fetchArtifacts' method. " +
                    "Input registry type is %s";

    private final RegistryCredentialService registryCredentialService;
    private final ModelRegistryService modelRegistryService;
    private final ResourceRegistryService resourceRegistryService;
    private final RegistryArtifactValidationService registryArtifactValidationService;
    private final TaskService taskService;

    public record ArtifactCacheKey(String ncaId, UUID taskId) {
    }

    private final LoadingCache<ArtifactCacheKey, List<Artifact>> artifactsPresignedUrlsCache =
            Caffeine.newBuilder()
                    .maximumSize(512)
                    .scheduler(Scheduler.systemScheduler())
                    .expireAfterWrite(ICMS_MAX_REQUEST_DURATION)
                    .build(this::fetchPresignedUrls);

    private final LoadingCache<ArtifactCacheKey, Long> artifactsSizeCache =
            Caffeine.newBuilder()
                    .maximumSize(512)
                    .scheduler(Scheduler.systemScheduler())
                    .expireAfterWrite(ICMS_MAX_REQUEST_DURATION)
                    .build(this::fetchSize);

    private final LoadingCache<ArtifactCacheKey, String> artifactsCacheHandleCache =
            Caffeine.newBuilder()
                    .maximumSize(512)
                    .scheduler(Scheduler.systemScheduler())
                    .expireAfterWrite(ICMS_MAX_REQUEST_DURATION)
                    .build(this::generateCacheHandle);


    public List<Artifact> getPresignedUrls(String ncaId, UUID taskId) {
        return artifactsPresignedUrlsCache.get(new ArtifactCacheKey(ncaId, taskId));
    }

    public long getSize(String ncaId, UUID taskId) {
        return artifactsSizeCache.get(new ArtifactCacheKey(ncaId, taskId));
    }

    public String getCacheHandle(String ncaId, UUID taskId) {
        return artifactsCacheHandleCache.get(new ArtifactCacheKey(ncaId, taskId));
    }

    public void validateArtifacts(TaskEntity taskEntity) {
        registryArtifactValidationService.validateArtifacts(taskEntity);
    }

    @VisibleForTesting
    public void invalidateCache() {
        artifactsPresignedUrlsCache.invalidateAll();
        artifactsSizeCache.invalidateAll();
        artifactsCacheHandleCache.invalidateAll();
    }

    private String generateCacheHandle(ArtifactCacheKey cacheKey) {
        var taskId = cacheKey.taskId;
        Comparator<ModelUdt> modelUdtComparator = Comparator.comparing(ModelUdt::getUrl);
        Comparator<ResourceUdt> resourceUdtComparator = Comparator.comparing(ResourceUdt::getUrl);
        var taskEntity = taskService.fetchTask(taskId);
        var models = CollectionUtils.isEmpty(taskEntity.getModels())
                ? Set.<ModelUdt>of() : taskEntity.getModels();
        var resources = CollectionUtils.isEmpty(taskEntity.getResources())
                ? Set.<ResourceUdt>of() : taskEntity.getResources();
        var hashes = Stream.concat(models.stream().sorted(modelUdtComparator),
                                   resources.stream().sorted(resourceUdtComparator))
                .map(artifactUdt -> DigestUtils.sha256(artifactUdt.getUrl()))
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

    private long getArtifactSize(TaskEntity taskEntity) {
        return ARTIFACT_TYPES.stream()
                .map(registryType -> fetchSize(taskEntity, registryType))
                .mapToLong(Long::longValue)
                .sum();
    }

    private long fetchSize(ArtifactCacheKey cacheKey) {
        var taskEntity = taskService.fetchTask(cacheKey.taskId);
        return getArtifactSize(taskEntity);
    }

    private long fetchSize(TaskEntity taskEntity, ArtifactTypeEnum registryType) {
        var taskId = taskEntity.getTaskId();
        var ncaId = taskEntity.getNcaId();

        try {
            if (MODEL == registryType) {
                if (CollectionUtils.isEmpty(taskEntity.getModels())) {
                    return 0L;
                }
                var secrets =
                        registryCredentialService.getModelRegistryCredentialsValue(taskEntity);
                var models = RegistryArtifactMapperService.toArtifactDetailsFromModelUdt(
                        taskEntity.getModels());
                return modelRegistryService.fetchSize(models, secrets);
            } else if (RESOURCE == registryType) {
                if (CollectionUtils.isEmpty(taskEntity.getResources())) {
                    return 0L;
                }
                var secrets =
                        registryCredentialService.getResourceRegistryCredentialsValue(taskEntity);
                var resources = RegistryArtifactMapperService.toArtifactDetailsFromResourceUdts(
                        taskEntity.getResources());
                return resourceRegistryService.fetchSize(resources, secrets);
            } else {
                var errMsg =
                        MESG_WRONG_IMPLEMENTATION_GET_ARTIFACTS_SIZE.formatted(registryType);
                log.error(errMsg);
                throw new IllegalStateException(errMsg);
            }
        } catch (IllegalStateException | IllegalArgumentException e) {
            var enrichedErrorMsg =
                    MESG_COMMON_ERROR_PREFIX.formatted(ncaId, taskId) + e.getMessage();
            log.error(enrichedErrorMsg);
            throw new BadRequestException(enrichedErrorMsg, e);
        } catch (Exception e) {
            var enrichedErrorMsg =
                    MESG_COMMON_ERROR_PREFIX.formatted(ncaId, taskId) + e.getMessage();
            log.error(enrichedErrorMsg);
            throw e;
        }
    }

    private List<Artifact> fetchArtifacts(TaskEntity taskEntity) {
        return ARTIFACT_TYPES.stream()
                .flatMap(artifactType -> fetchArtifacts(taskEntity, artifactType).stream())
                .toList();
    }

    private List<Artifact> fetchArtifacts(TaskEntity taskEntity, ArtifactTypeEnum artifactType) {
        var taskId = taskEntity.getTaskId();
        var ncaId = taskEntity.getNcaId();
        try {
            if (MODEL == artifactType) {
                if (CollectionUtils.isEmpty(taskEntity.getModels())) {
                    return Collections.emptyList();
                }
                var secrets =
                        registryCredentialService.getModelRegistryCredentialsValue(taskEntity);
                var models = RegistryArtifactMapperService.toArtifactDetailsFromModelUdt(
                        taskEntity.getModels());
                return modelRegistryService.fetchArtifact(models, secrets);
            } else if (RESOURCE == artifactType) {
                if (CollectionUtils.isEmpty(taskEntity.getResources())) {
                    return Collections.emptyList();
                }
                var secrets =
                        registryCredentialService.getResourceRegistryCredentialsValue(taskEntity);
                var resources = RegistryArtifactMapperService
                        .toArtifactDetailsFromResourceUdts(taskEntity.getResources());
                return resourceRegistryService.fetchArtifact(resources, secrets);
            } else {
                throw new IllegalStateException(
                        MESG_WRONG_IMPLEMENTATION_FETCH_ARTIFACTS.formatted(artifactType));
            }
        } catch (IllegalStateException | IllegalArgumentException e) {
            var enrichedErrorMsg =
                    MESG_COMMON_ERROR_PREFIX.formatted(ncaId, taskId) + e.getMessage();
            log.error(enrichedErrorMsg);
            throw new BadRequestException(enrichedErrorMsg, e);
        } catch (Exception e) {
            var enrichedErrorMsg =
                    MESG_COMMON_ERROR_PREFIX.formatted(ncaId, taskId) + e.getMessage();
            log.error(enrichedErrorMsg);
            throw e;
        }
    }

    private List<Artifact> fetchPresignedUrls(ArtifactCacheKey cacheKey) {
        var taskEntity = taskService.fetchTask(cacheKey.taskId);
        return fetchArtifacts(taskEntity);
    }
}
