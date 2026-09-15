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
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import com.nvidia.nvcf.service.function.FunctionMapperService;
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
            "Account '%s', Function id '%s', version '%s': ";
    private static final String MESG_WRONG_IMPLEMENTATION_VALIDATE_ARTIFACTS =
            "Only container, helm, resource or model registry support 'validateArtifact'" +
                    " method. Input registry type is %s";
    private static final String MESG_VALIDATING_ARTIFACTS =
            "Function id {}, version {}: Validating artifacts";
    private static final String MESG_ARTIFACT_INVALID =
            "Function id '%s': Invalid artifact provided";
    private static final String MESG_LOG_ARTIFACT_VALIDATION =
            "Artifact validation is enabled but exception is not being thrown for " +
                    "invalid artifact so that function creation/deployment can proceed";
    private static final String MESG_MISSING_REGISTRY_CREDENTIALS =
            "Missing %s registry credential for hostname '%s'";

    private final FunctionMapperService functionMapperService;
    private final RegistryCredentialFunctionService registryCredentialFunctionService;
    private final RegistryValidationService registryValidationService;
    private final ModelRegistryService modelRegistryService;
    private final ResourceRegistryService resourceRegistryService;
    private final HelmRegistryService helmRegistryService;
    private final ContainerRegistryService containerRegistryService;
    private final String exceptionHandlingDuringArtifactValidation;

    public RegistryArtifactValidationService(
            FunctionMapperService functionMapperService,
            RegistryCredentialFunctionService registryCredentialFunctionService,
            RegistryValidationService registryValidationService,
            ModelRegistryService modelRegistryService,
            ResourceRegistryService resourceRegistryService,
            HelmRegistryService helmRegistryService,
            ContainerRegistryService containerRegistryService,
            @Value("${nvcf.registries.artifact-validation.exception-handling:throw}") // throw, log
            String exceptionHandlingDuringArtifactValidation) {
        this.functionMapperService = functionMapperService;
        this.registryCredentialFunctionService = registryCredentialFunctionService;
        this.registryValidationService = registryValidationService;
        this.modelRegistryService = modelRegistryService;
        this.resourceRegistryService = resourceRegistryService;
        this.helmRegistryService = helmRegistryService;
        this.containerRegistryService = containerRegistryService;
        this.exceptionHandlingDuringArtifactValidation = exceptionHandlingDuringArtifactValidation;
    }

    public void validateArtifacts(FunctionEntity functionEntity) {
        var functionId = functionEntity.getFunctionId();
        var versionId = functionEntity.getFunctionVersionId();
        var functionModels = functionMapperService.toFunctionModels(functionEntity.getModelSpecs());
        try {
            validateRegistryCredentialsExist(functionEntity, functionModels);
            log.info(MESG_VALIDATING_ARTIFACTS, functionId, versionId);
            validateArtifacts(functionEntity, functionModels, EnumSet.of(
                    ArtifactTypeEnum.MODEL,
                    ArtifactTypeEnum.RESOURCE,
                    ArtifactTypeEnum.HELM,
                    ArtifactTypeEnum.CONTAINER));
        } catch (NotFoundException | UnauthorizedException | ForbiddenException |
                 BadRequestException | TooManyRequestsException e) {
            var mesg = MESG_ARTIFACT_INVALID.formatted(functionId) + " - '{}'";
            log.error(mesg, e.getMessage());
            if (exceptionHandlingDuringArtifactValidation.equals("throw")) {
                throw e;
            } else {
                // Just log a warning and let the request continue so that function creation
                // or deployment can proceed.
                log.warn(MESG_LOG_ARTIFACT_VALIDATION);
            }
        }
    }

    public void validateArtifacts(FunctionEntity function, List<FunctionModelDto> functionModels,
                                   Set<ArtifactTypeEnum> artifactTypes) {
        artifactTypes.forEach(artifactType -> validateArtifacts(function, functionModels,
                                                                artifactType));
    }

    public void validateArtifacts(FunctionEntity function, List<FunctionModelDto> functionModels,
                                   ArtifactTypeEnum artifactType) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var ncaId = function.getNcaId();

        try {
            if (MODEL == artifactType) {
                if (CollectionUtils.isEmpty(functionModels)) {
                    return;
                }
                var modelArtifactDetails = RegistryArtifactMapperService
                        .toArtifactDetailsFromFunctionModelDtos(functionModels);
                if (CollectionUtils.isEmpty(modelArtifactDetails)) {
                    return;
                }
                var secrets = registryCredentialFunctionService
                        .getModelRegistryCredentialValues(function, functionModels);
                modelRegistryService.validateArtifacts(modelArtifactDetails, secrets);
            } else if (RESOURCE == artifactType) {
                if (function.getResources() == null) {
                    return;
                }
                var secrets = registryCredentialFunctionService
                        .getResourceRegistryCredentialValues(function);
                var resourceArtifactDetails = RegistryArtifactMapperService
                        .toArtifactDetailsFromResoureUdts(function.getResources());
                resourceRegistryService.validateArtifacts(resourceArtifactDetails, secrets);
            } else if (HELM == artifactType) {
                if (Strings.isBlank(function.getHelmChart())) {
                    return;
                }
                var secrets =
                        registryCredentialFunctionService.getHelmRegistryCredentialValues(function);
                helmRegistryService.validateArtifact(function.getHelmChart(), secrets);
            } else if (CONTAINER == artifactType) {
                if (Strings.isBlank(function.getContainerImage())) {
                    return;
                }
                var secrets = registryCredentialFunctionService
                        .getContainerRegistryCredentialValues(function);
                containerRegistryService.validateArtifact(function.getContainerImage(), secrets);
            } else {
                throw new IllegalStateException(
                        MESG_WRONG_IMPLEMENTATION_VALIDATE_ARTIFACTS.formatted(artifactType));
            }
        } catch (IllegalStateException | IllegalArgumentException e) {
            String commonPrefix = MESG_COMMON_ERROR_PREFIX.formatted(
                    ncaId, functionId, versionId);
            String enrichedErrorMsg =
                    e.getMessage() != null && e.getMessage().startsWith(commonPrefix) ?
                            e.getMessage() : commonPrefix + e.getMessage();
            log.error(enrichedErrorMsg);
            throw new BadRequestException(enrichedErrorMsg, e);
        } catch (Exception e) {
            String commonPrefix = MESG_COMMON_ERROR_PREFIX.formatted(
                    ncaId, functionId, versionId);
            String enrichedErrorMsg =
                    e.getMessage() != null && e.getMessage().startsWith(commonPrefix) ?
                            e.getMessage() : commonPrefix + e.getMessage();
            log.error(enrichedErrorMsg);
            throw e;
        }
    }

    public void validateContainerRegistryCredentialsExist(FunctionEntity function) {
        if (Strings.isBlank(function.getContainerImage())) return;
        var creds = registryCredentialFunctionService
                .getContainerRegistryCredentialDetails(function);
        var hostname = registryCredentialFunctionService
                .getRegistryHostname(function.getContainerImage());
        validateCredentialsExist(CONTAINER, hostname, creds);
    }

    public void validateHelmRegistryCredentialsExist(FunctionEntity function) {
        if (Strings.isBlank(function.getHelmChart())) return;
        var creds = registryCredentialFunctionService
                .getHelmRegistryCredentialDetails(function);
        var hostname = registryCredentialFunctionService
                .getHelmRegistryHostname(function.getHelmChart());
        validateCredentialsExist(HELM, hostname, creds);
    }

    private void validateRegistryCredentialsExist(FunctionEntity function,
                                                   List<FunctionModelDto> functionModels) {
        try {
            validateContainerRegistryCredentialsExist(function);
            validateHelmRegistryCredentialsExist(function);
            registryCredentialFunctionService.getModelRegistryCredentialsMap(function,
                                                                             functionModels);
            registryCredentialFunctionService.getResourceRegistryCredentialsMap(function);
        } catch (Exception e) {
            log.error(e.getMessage());
            throw new BadRequestException(e.getMessage(), e);
        }
    }

    private void validateCredentialsExist(
            ArtifactTypeEnum artifactType,
            String hostname,
            List<RegistryCredentialDetailsDto> credentials) {
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
}
