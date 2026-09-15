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

package com.nvidia.boot.registries.service.registry.model;

import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_ARTIFACT_VALIDATED;
import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_ARTIFACT_VALIDATION_FAILED;
import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_ARTIFACT_VALIDATION_FAILED_UNKNOWN_REASON;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.RegistryValidationService;
import com.nvidia.boot.registries.service.registry.dto.Artifact;
import com.nvidia.boot.registries.service.registry.dto.ArtifactDetails;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import java.net.URI;
import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.stream.Collectors;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.logging.log4j.util.Strings;


@Slf4j
public class ModelRegistryService {
    private static final String MESG_INVALID_MODEL_ARTIFACT =
            "Model artifact either does not exist or not accessible with the account model " +
                    "registry secrets";
    private static final String MESG_MISSING_MODEL_ARTIFACT_SECRET =
            "Missing secret for model artifact: {}";
    private static final String MESG_UNSUPPORTED_HOSTNAME = "Unsupported registry hostname: %s";
    private static final String MESG_ARTIFACT_VALIDATION_DISABLED =
            "Artifact validation disabled for model registry hostname: {}";

    private final Map<String, ModelRegistry> modelRegistryMap;
    private final RegistryMapperService registryMapperService;
    private final RegistryValidationService registryValidationService;

    public ModelRegistryService(List<ModelRegistry> modelRegistries,
                                RegistryMapperService registryMapperService,
                                RegistryValidationService registryValidationService) {
        this.modelRegistryMap = modelRegistries.stream()
                .collect(Collectors.toMap(ModelRegistry::getHostname, m -> m));
        this.registryMapperService = registryMapperService;
        this.registryValidationService = registryValidationService;
    }

    // When we run the tests locally, the hostname will be a localhost domain.
    // However, the test artifacts are staging or canary materials, that means we won't be able to
    // map them back to the localhost agent during lookup. The function is served for overwriting
    // the map during test.
    @VisibleForTesting
    public void overwriteRegistryHostnameMap(String originalHost, String newHost) {
        this.modelRegistryMap.put(newHost, this.modelRegistryMap.get(originalHost));
    }

    private ModelRegistry lookupRegistry(String hostname) {
        var normalizedHostname =
                registryMapperService.toNormalizedRecognizedRegistryHostname(hostname);
        return Optional.ofNullable(modelRegistryMap.get(normalizedHostname))
                .orElseThrow(() -> new IllegalArgumentException(
                        MESG_UNSUPPORTED_HOSTNAME.formatted(hostname)));
    }

    @SneakyThrows
    private String extractRegistryHostname(String modelRegistryUrl) {
        var registryHostName = new URI(modelRegistryUrl).getHost();
        if (Strings.isBlank(registryHostName)) {
            throw new BadRequestException(MESG_INVALID_MODEL_ARTIFACT);
        }
        return registryMapperService.toNormalizedHostname(registryHostName);
    }

    @SneakyThrows
    private <T> T processModelWithSecrets(
            ArtifactDetails model,
            String registryHostName,
            List<String> secrets,
            ModelProcessor<T> processor) {
        if (secrets == null) {
            throw new BadRequestException(MESG_MISSING_MODEL_ARTIFACT_SECRET);
        }
        Exception lastException = null;
        for (var secret : secrets) {
            try {
                var retval = processor.process(lookupRegistry(registryHostName), model, secret);
                log.info(MESG_ARTIFACT_VALIDATED, model.url());
                return retval;
            } catch (BadRequestException | UnauthorizedException |
                     ForbiddenException | NotFoundException e) {
                // Remember the exception, then try the next secret.
                lastException = e;
                log.debug(e.getMessage(), model.url());
            }
        }
        // No secret succeeded. Just use the last saved one.
        if (lastException != null) {
            log.error(MESG_ARTIFACT_VALIDATION_FAILED, model.url(), lastException.getMessage());
            throw lastException;
        }
        // Should not get here.
        throw new IllegalStateException(MESG_ARTIFACT_VALIDATION_FAILED_UNKNOWN_REASON
                                                .formatted(model.url()));
    }

    public long fetchSize(
            Collection<ArtifactDetails> models,
            Map<String, List<String>> registrySecrets) {

        return models.stream()
                .mapToLong(model -> {
                    var registryHostName = extractRegistryHostname(model.url());
                    return processModelWithSecrets(
                            model,
                            registryHostName,
                            registrySecrets.get(registryHostName),
                            ModelRegistry::fetchSize);
                })
                .sum();
    }

    public List<Artifact> fetchArtifact(
            Collection<ArtifactDetails> models,
            Map<String, List<String>> registrySecrets) {
        return models.stream()
                .map(model -> {
                    var registryHostName = extractRegistryHostname(model.url());
                    return processModelWithSecrets(
                            model,
                            registryHostName,
                            registrySecrets.get(registryHostName),
                            ModelRegistry::fetchArtifact);
                })
                .toList();
    }

    @SneakyThrows
    public void validateArtifacts(
            Collection<ArtifactDetails> models,
            Map<String, List<String>> registrySecrets) {
        for (var model : models) {
            var registryHostName = extractRegistryHostname(model.url());
            if (!registryValidationService.isArtifactValidationEnabled(
                    ArtifactTypeEnum.MODEL, registryHostName)) {
                log.info(MESG_ARTIFACT_VALIDATION_DISABLED, registryHostName);
                continue;
            }
            processModelWithSecrets(
                    model,
                    registryHostName,
                    registrySecrets.get(registryHostName),
                    (registry, m, secret) -> {
                        registry.validateArtifact(m, secret);
                        return null;
                    });
        }
    }

    @FunctionalInterface
    private interface ModelProcessor<T> {
        T process(ModelRegistry registry, ArtifactDetails model, String secret);
    }
}
