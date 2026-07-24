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

import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.CONTAINER;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.HELM;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.MODEL;
import static com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum.RESOURCE;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.registries.service.registry.RegistryValidationService;
import com.nvidia.boot.registries.service.registry.container.ContainerRegistryService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistryService;
import com.nvidia.boot.registries.service.registry.model.ModelRegistryService;
import com.nvidia.boot.registries.service.registry.resource.ResourceRegistryService;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.service.account.dto.RegistryCredentialDto;
import java.util.EnumSet;
import java.util.List;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.apache.logging.log4j.util.Strings;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@RefreshScope
@Service
public class RegistryArtifactValidationService {
    private static final String MESG_COMMON_ERROR_PREFIX =
            "Account '%s', Task '%s': ";
    private static final String MESG_WRONG_IMPLEMENTATION_VALIDATE_ARTIFACTS =
            "Only container, helm, resource or model registry support 'validateArtifact'" +
                    " method. Input registry type is %s";
    private static final String MESG_ARTIFACT_INVALID =
            "Task '%s': Invalid artifact provided";
    private static final String MESG_ARTIFACT_VALIDATION =
            "Task '{}': {} artifacts";
    private static final String MESG_LOG_ARTIFACT_VALIDATION =
            "Artifact validation is enabled but exception is not being thrown for " +
                    "invalid artifact so that Task creation can proceed";
    private static final String MESG_MISSING_REGISTRY_CREDENTIALS =
            "Missing %s registry credential for hostname '%s'";

    private final RegistryCredentialService registryCredentialService;
    private final RegistryValidationService registryValidationService;
    private final ModelRegistryService modelRegistryService;
    private final ResourceRegistryService resourceRegistryService;
    private final HelmRegistryService helmRegistryService;
    private final ContainerRegistryService containerRegistryService;
    private final String exceptionHandlingDuringArtifactValidation;

    public RegistryArtifactValidationService(
            RegistryCredentialService registryCredentialService,
            RegistryValidationService registryValidationService,
            ModelRegistryService modelRegistryService,
            ResourceRegistryService resourceRegistryService,
            HelmRegistryService helmRegistryService,
            ContainerRegistryService containerRegistryService,
            @Value("${nvct.registries.artifact-validation.exception-handling:throw}")  // throw, log
            String exceptionHandlingDuringArtifactValidation) {
        this.registryCredentialService = registryCredentialService;
        this.registryValidationService = registryValidationService;
        this.modelRegistryService = modelRegistryService;
        this.resourceRegistryService = resourceRegistryService;
        this.helmRegistryService = helmRegistryService;
        this.containerRegistryService = containerRegistryService;
        this.exceptionHandlingDuringArtifactValidation = exceptionHandlingDuringArtifactValidation;
    }

    public void validateArtifacts(TaskEntity taskEntity) {
        try {
            log.info(MESG_ARTIFACT_VALIDATION, taskEntity.getTaskId(), "Validating");
            validateCredentialsExist(taskEntity);
            validateArtifacts(taskEntity, EnumSet.of(
                    MODEL,
                    RESOURCE,
                    HELM,
                    CONTAINER));
            log.info(MESG_ARTIFACT_VALIDATION, taskEntity.getTaskId(), "Validated");
        } catch (BadRequestException | NotFoundException | UnauthorizedException |
                 ForbiddenException | TooManyRequestsException e) {
            var taskId = taskEntity.getTaskId();
            var errMsg = MESG_ARTIFACT_INVALID.formatted(taskId);
            log.error(errMsg, e);
            if (exceptionHandlingDuringArtifactValidation.equals("throw")) {
                throw e;
            } else {
                // Just log a warning and let the request continue so that function creation
                // or deployment can proceed.
                log.warn(MESG_LOG_ARTIFACT_VALIDATION);
            }
        }
    }

    public void validateArtifacts(TaskEntity taskEntity,
                                  Set<ArtifactTypeEnum> artifactTypes) {
        artifactTypes.forEach(artifactType -> validateArtifacts(taskEntity, artifactType));
    }

    private void validateArtifacts(TaskEntity taskEntity, ArtifactTypeEnum artifactType) {
        var taskId = taskEntity.getTaskId();
        var ncaId = taskEntity.getNcaId();

        try {
            if (MODEL == artifactType) {
                if (CollectionUtils.isEmpty(taskEntity.getModels())) {
                    return;
                }
                var secrets = registryCredentialService
                        .getModelRegistryCredentialsValue(taskEntity);
                var modelArtifactDetails = RegistryArtifactMapperService
                        .toArtifactDetailsFromModelUdt(taskEntity.getModels());
                modelRegistryService.validateArtifacts(modelArtifactDetails, secrets);
            } else if (RESOURCE == artifactType) {
                if (CollectionUtils.isEmpty(taskEntity.getResources())) {
                    return;
                }
                var secrets = registryCredentialService
                        .getResourceRegistryCredentialsValue(taskEntity);
                var resourceArtifactDetails = RegistryArtifactMapperService
                        .toArtifactDetailsFromResourceUdts(taskEntity.getResources());
                resourceRegistryService.validateArtifacts(resourceArtifactDetails, secrets);
            } else if (HELM == artifactType) {
                if (Strings.isBlank(taskEntity.getHelmChart())) {
                    return;
                }
                var secrets = registryCredentialService.getHelmRegistryCredentialsValue(taskEntity);
                helmRegistryService.validateArtifact(taskEntity.getHelmChart(), secrets);
            } else if (CONTAINER == artifactType) {
                if (Strings.isBlank(taskEntity.getContainerImage())) {
                    return;
                }
                var secrets = registryCredentialService
                        .getContainerRegistryCredentialsValue(taskEntity);
                containerRegistryService.validateArtifact(taskEntity.getContainerImage(), secrets);
            } else {
                var mesg = MESG_WRONG_IMPLEMENTATION_VALIDATE_ARTIFACTS.formatted(artifactType);
                log.error(mesg);
                throw new IllegalStateException(mesg);
            }
        } catch (IllegalStateException | IllegalArgumentException e) {
            var commonPrefix = MESG_COMMON_ERROR_PREFIX.formatted(ncaId, taskId);
            var enrichedErrorMsg =
                    e.getMessage() != null && e.getMessage().startsWith(commonPrefix) ?
                            e.getMessage() : commonPrefix + e.getMessage();
            log.error(enrichedErrorMsg);
            throw new BadRequestException(enrichedErrorMsg, e);
        } catch (Exception e) {
            var commonPrefix = MESG_COMMON_ERROR_PREFIX.formatted(ncaId, taskId);
            var enrichedErrorMsg =
                    e.getMessage() != null && e.getMessage().startsWith(commonPrefix) ?
                            e.getMessage() : commonPrefix + e.getMessage();
            log.error(enrichedErrorMsg);
            throw e;
        }
    }

    public void validateContainerRegistryCredentialsExist(TaskEntity task) {
        if (Strings.isBlank(task.getContainerImage())) return;
        var creds = registryCredentialService.getContainerRegistryCredentials(task);
        var hostname = registryCredentialService.getRegistryHostname(task.getContainerImage());
        validateCredentialsExist(CONTAINER, hostname, creds);
    }

    public void validateHelmRegistryCredentialsExist(TaskEntity task) {
        if (Strings.isBlank(task.getHelmChart())) return;
        var creds = registryCredentialService.getHelmRegistryCredentials(task);
        var hostname = registryCredentialService.getHelmRegistryHostname(task.getHelmChart());
        validateCredentialsExist(HELM, hostname, creds);
    }

    private void validateCredentialsExist(
            ArtifactTypeEnum artifactType,
            String hostname,
            List<RegistryCredentialDto> credentials) {
        if (!registryValidationService.isArtifactValidationEnabled(artifactType, hostname)) {
            return;
        }

        // Gets here if artifact-validation is enabled. If there are registry creds for the
        // hostname, then we don't have to throw IllegalStateException with missing registry
        // credential message.
        if (!CollectionUtils.isEmpty(credentials)) {
            return;
        }
        var mesg = MESG_MISSING_REGISTRY_CREDENTIALS.formatted(artifactType, hostname);
        log.error(mesg);
        throw new IllegalStateException(mesg);
    }

    private void validateCredentialsExist(TaskEntity taskEntity) {
        try {
            validateContainerRegistryCredentialsExist(taskEntity);
            validateHelmRegistryCredentialsExist(taskEntity);
            registryCredentialService.getModelRegistryCredentials(taskEntity);
            registryCredentialService.getResourceRegistryCredentials(taskEntity);
        } catch (Exception e) {
            if (e instanceof BadRequestException badrequestexception) throw badrequestexception;
            log.error(e.getMessage(), e);
            throw new BadRequestException(e.getMessage(), e);
        }
    }

}
