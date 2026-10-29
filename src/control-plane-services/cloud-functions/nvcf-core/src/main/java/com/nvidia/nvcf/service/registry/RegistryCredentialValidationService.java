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

import com.nvidia.boot.registries.service.registry.container.ContainerRegistryService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.boot.registries.service.registry.helm.HelmRegistryService;
import java.util.List;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
public class RegistryCredentialValidationService {

    private static final String MESG_CREDENTIAL_VALIDATION_FAILED =
            "Credential validation failed for hostname '{}': {}";
    private static final String MESG_SKIPPING_CREDENTIAL_VALIDATION =
            "Skipping credential validation for artifact type: {}";
    private static final String MESG_LOG_CREDENTIAL_VALIDATION =
            "Credential validation is enabled but exception is not being thrown for " +
                    "invalid credentials so that the operation can proceed";

    private final ContainerRegistryService containerRegistryService;
    private final HelmRegistryService helmRegistryService;
    private final String exceptionHandlingDuringCredentialValidation;

    public RegistryCredentialValidationService(
            ContainerRegistryService containerRegistryService,
            HelmRegistryService helmRegistryService,
            @Value("${nvcf.registries.credential-validation.exception-handling:throw}")
            String exceptionHandlingDuringCredentialValidation) {
        this.containerRegistryService = containerRegistryService;
        this.helmRegistryService = helmRegistryService;
        this.exceptionHandlingDuringCredentialValidation =
                exceptionHandlingDuringCredentialValidation;
    }

    public void validateRegistryCredentials(
            String hostname,
            Set<ArtifactTypeEnum> artifactTypes,
            List<String> secrets) {
        try {
            for (ArtifactTypeEnum artifactType : artifactTypes) {
                switch (artifactType) {
                    case ArtifactTypeEnum.CONTAINER ->
                            containerRegistryService.validateCredentials(hostname, secrets);
                    case ArtifactTypeEnum.HELM ->
                            helmRegistryService.validateCredentials(hostname, secrets);
                    default -> log.debug(MESG_SKIPPING_CREDENTIAL_VALIDATION, artifactType);
                }
            }
        } catch (Exception e) {
            log.error(MESG_CREDENTIAL_VALIDATION_FAILED, hostname, e.getMessage());
            if (exceptionHandlingDuringCredentialValidation.equals("throw")) {
                throw e;
            } else {
                log.warn(MESG_LOG_CREDENTIAL_VALIDATION);
            }
        }
    }
}
