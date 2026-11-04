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

package com.nvidia.boot.registries.service.registry.resource;

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
public class ResourceRegistryService {
    private static final String MESG_INVALID_RESOURCE_ARTIFACT =
            "Resource artifact either does not exist or not accessible with account resource " +
                    "registry secrets";
    private static final String MESG_UNSUPPORTED_HOSTNAME =
            "Unsupported registry hostname: %s";
    private static final String MESG_MISSING_RESOURCE_ARTIFACT_SECRET =
            "Missing secret for resource artifact: %s";
    private static final String MESG_ARTIFACT_VALIDATION_DISABLED =
            "Artifact validation disabled for resource registry hostname: {}";

    private final Map<String, ResourceRegistry> resourceRegistryMap;
    private final RegistryMapperService registryMapperService;
    private final RegistryValidationService registryValidationService;

    public ResourceRegistryService(List<ResourceRegistry> resourceRegistries,
                                   RegistryMapperService registryMapperService,
                                   RegistryValidationService registryValidationService) {
        this.resourceRegistryMap = resourceRegistries.stream()
                .collect(Collectors.toMap(ResourceRegistry::getHostname, r -> r));
        this.registryMapperService = registryMapperService;
        this.registryValidationService = registryValidationService;
    }

    // When we run the tests locally, the hostname will be a localhost domain.
    // However, the test artifacts are staging or canary materials, that means we won't be able to
    // map them back to the localhost agent during lookup. The function is served for overwriting
    // the map during test.
    @VisibleForTesting
    public void overwriteRegistryHostnameMap(String originalHost, String newHost) {
        this.resourceRegistryMap.put(newHost, this.resourceRegistryMap.get(originalHost));
    }

    private ResourceRegistry lookupRegistry(String hostname) {
        var normalizedHostname =
                registryMapperService.toNormalizedRecognizedRegistryHostname(hostname);
        return Optional.ofNullable(resourceRegistryMap.get(normalizedHostname))
                .orElseThrow(
                        () -> new NotFoundException(MESG_UNSUPPORTED_HOSTNAME.formatted(hostname)));
    }

    @SneakyThrows
    private String extractRegistryHostname(String modelRegistryUrl) {
        var registryHostName = new URI(modelRegistryUrl).getHost();
        if (Strings.isBlank(registryHostName)) {
            throw new BadRequestException(MESG_INVALID_RESOURCE_ARTIFACT);
        }
        return registryMapperService.toNormalizedHostname(registryHostName);
    }

    @SneakyThrows
    private <T> T processResourceWithSecrets(
            ArtifactDetails resource,
            String registryHostName,
            List<String> secrets,
            ResourceProcessor<T> processor) {
        if (secrets == null) {
            throw new BadRequestException(
                    MESG_MISSING_RESOURCE_ARTIFACT_SECRET.formatted(registryHostName));
        }
        Exception lastException = null;
        for (var secret : secrets) {
            try {
                var retval = processor.process(lookupRegistry(registryHostName), resource, secret);
                log.info(MESG_ARTIFACT_VALIDATED, resource.url());
                return retval;
            } catch (BadRequestException | UnauthorizedException |
                     ForbiddenException | NotFoundException e) {
                // Remember the exception, then try the next secret.
                lastException = e;
                log.debug(e.getMessage(), e);
            }
        }
        // No secret succeeded. Just use the last saved one.
        if (lastException != null) {
            log.error(MESG_ARTIFACT_VALIDATION_FAILED, resource.url(), lastException.getMessage());
            throw lastException;
        }
        // Should not get here.
        throw new IllegalStateException(MESG_ARTIFACT_VALIDATION_FAILED_UNKNOWN_REASON
                                                .formatted(resource.url()));
    }

    public long fetchSize(
            Collection<ArtifactDetails> resources,
            Map<String, List<String>> registrySecrets) {
        return resources.stream()
                .mapToLong(resource -> {
                    var registryHostName = extractRegistryHostname(resource.url());
                    return processResourceWithSecrets(
                            resource,
                            registryHostName,
                            registrySecrets.get(registryHostName),
                            ResourceRegistry::fetchSize);
                })
                .sum();
    }

    public List<Artifact> fetchArtifact(
            Collection<ArtifactDetails> resources,
            Map<String, List<String>> registrySecrets) {
        return resources.stream()
                .map(resource -> {
                    var registryHostName = extractRegistryHostname(resource.url());
                    return processResourceWithSecrets(
                            resource,
                            registryHostName,
                            registrySecrets.get(registryHostName),
                            ResourceRegistry::fetchArtifact);
                })
                .toList();
    }

    public void validateArtifacts(
            Collection<ArtifactDetails> resources,
            Map<String, List<String>> registrySecrets) {
        for (var resource : resources) {
            var registryHostName = extractRegistryHostname(resource.url());
            if (!registryValidationService.isArtifactValidationEnabled(
                    ArtifactTypeEnum.RESOURCE, registryHostName)) {
                log.info(MESG_ARTIFACT_VALIDATION_DISABLED, registryHostName);
                continue;
            }
            processResourceWithSecrets(
                    resource,
                    registryHostName,
                    registrySecrets.get(registryHostName),
                    (registry, r, secret) -> {
                        registry.validateArtifact(r, secret);
                        return null;
                    });
        }
    }

    @FunctionalInterface
    private interface ResourceProcessor<T> {
        T process(ResourceRegistry registry, ArtifactDetails resource, String secret);
    }
}
