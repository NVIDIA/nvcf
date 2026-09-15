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

package com.nvidia.boot.registries.service.registry.helm;

import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_ARTIFACT_VALIDATED;
import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_ARTIFACT_VALIDATION_FAILED;
import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_ARTIFACT_VALIDATION_FAILED_UNKNOWN_REASON;
import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_REGISTRY_CREDENTIALS_VALIDATION_FAILED;
import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_REGISTRY_CREDENTIALS_VALIDATION_FAILED_UNKNOWN_REASON;
import static com.nvidia.boot.registries.util.RegistriesConstants.MESG_UNSUPPORTED_HOSTNAME;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.RegistryValidationService;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.stream.Collectors;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;

@Slf4j
public class HelmRegistryService {
    private static final String MESG_ARTIFACT_VALIDATION_DISABLED =
            "Artifact validation disabled for helm registry hostname: {}";
    private static final String MESG_CREDENTIAL_VALIDATION_DISABLED =
            "Credential validation disabled for helm registry hostname: {}";

    private final Map<String, HelmRegistry> helmRegistryMap;
    private final RegistryMapperService registryMapperService;
    private final RegistryValidationService registryValidationService;

    public HelmRegistryService(List<HelmRegistry> helmRegistries,
                               RegistryMapperService registryMapperService,
                               RegistryValidationService registryValidationService) {
        this.helmRegistryMap = helmRegistries.stream()
                .collect(Collectors.toMap(HelmRegistry::getHostname, h -> h));
        this.registryMapperService = registryMapperService;
        this.registryValidationService = registryValidationService;
    }

    // When we run the tests locally, the hostname will be a localhost domain.
    // However, the test artifacts are staging or canary materials, that means we won't be able to
    // map them back to the localhost agent during lookup. The function is served for overwriting
    // the map during test.
    @VisibleForTesting
    public void overwriteRegistryHostnameMap(String originalHost, String newHost) {
        this.helmRegistryMap.put(newHost, this.helmRegistryMap.get(originalHost));
    }

    private HelmRegistry lookupRegistry(String hostname) {
        String normalizedHostname =
                registryMapperService.toNormalizedRecognizedRegistryHostname(hostname);
        if (helmRegistryMap.containsKey(normalizedHostname)) {
            return helmRegistryMap.get(normalizedHostname);
        } else {
            throw new IllegalArgumentException(MESG_UNSUPPORTED_HOSTNAME.formatted(hostname));
        }
    }

    @SneakyThrows
    private String extractRegistryHostname(String helmChartUrl) {
        return new URI(helmChartUrl).getHost();
    }

    @SneakyThrows
    public void validateArtifact(String helmChartUrl,
                                 List<String> secrets) {
        var registryHostname = extractRegistryHostname(helmChartUrl);
        if (!registryValidationService.isArtifactValidationEnabled(
                ArtifactTypeEnum.HELM, registryHostname)) {
            log.info(MESG_ARTIFACT_VALIDATION_DISABLED, registryHostname);
            return;
        }

        Exception lastException = null;

        for (String secret : secrets) {
            try {
                // Validate the artifact and let exception bubble.
                lookupRegistry(registryHostname).validateArtifact(helmChartUrl, secret);

                // If we get here, validation succeeded using at least one of the secrets, just
                // return.
                log.info(MESG_ARTIFACT_VALIDATED, helmChartUrl);
                return;
            } catch (BadRequestException
                     | UnauthorizedException
                     | ForbiddenException
                     | NotFoundException
                     | TooManyRequestsException
                     | IllegalArgumentException e) {
                // Remember the exception, then try the next secret.
                lastException = e;
                log.warn(MESG_ARTIFACT_VALIDATION_FAILED, helmChartUrl,
                         e.getMessage()); // We don't have the secret name to log.
            }
        }

        // No secret succeeded. Just use the last saved one.
        if (lastException != null) {
            log.error(MESG_ARTIFACT_VALIDATION_FAILED, helmChartUrl, lastException.getMessage());
            throw lastException;
        }

        // Should not get here.
        throw new IllegalStateException(MESG_ARTIFACT_VALIDATION_FAILED_UNKNOWN_REASON
                                                .formatted(helmChartUrl));
    }

    @SneakyThrows
    public void validateCredentials(
            String registryHostname,
            List<String> secrets) {
        if (!registryValidationService.isCredentialValidationEnabled(
                ArtifactTypeEnum.HELM, registryHostname)) {
            log.info(MESG_CREDENTIAL_VALIDATION_DISABLED, registryHostname);
            return;
        }

        Exception lastException = null;

        for (String secret : secrets) {
            try {
                lookupRegistry(registryHostname).validateCredential(registryHostname, secret);
                return;
            } catch (BadRequestException
                     | UnauthorizedException
                     | ForbiddenException
                     | NotFoundException
                     | TooManyRequestsException
                     | IllegalArgumentException e) {
                // Remember the exception, then try the next secret.
                lastException = e;
                log.warn(MESG_REGISTRY_CREDENTIALS_VALIDATION_FAILED, e.getMessage());
            }
        }

        // No secret succeeded. Just use the last saved one.
        if (lastException != null) {
            log.error(MESG_REGISTRY_CREDENTIALS_VALIDATION_FAILED, lastException.getMessage());
            throw lastException;
        }

        throw new IllegalStateException(MESG_REGISTRY_CREDENTIALS_VALIDATION_FAILED_UNKNOWN_REASON);
    }
}
