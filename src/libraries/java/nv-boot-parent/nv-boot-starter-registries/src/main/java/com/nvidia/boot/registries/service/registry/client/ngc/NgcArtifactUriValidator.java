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

package com.nvidia.boot.registries.service.registry.client.ngc;

import static com.nvidia.boot.registries.util.RegistriesConstants.CAS_DIRECT_MODELS_REGISTRY_PATH;
import static com.nvidia.boot.registries.util.RegistriesConstants.CAS_DIRECT_RESOURCES_REGISTRY_PATH;
import static com.nvidia.boot.registries.util.RegistriesConstants.CAS_MODELS_VERSIONS_PATH;
import static com.nvidia.boot.registries.util.RegistriesConstants.CAS_RESOURCES_VERSIONS_PATH;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import java.util.regex.Pattern;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

/**
 * Utility class for validating NGC artifact URIs (models and resources).
 *
 * <p>Valid URIs must be fully qualified URLs matching the NGC Registry format:
 * <pre>
 * https://api[.stg|.canary].ngc.nvidia.com/v2/org/:org-name[/team/:team-name]/{models|resources}/:name/:version-id/files
 * </pre>
 *
 * <p>URIs with "/versions/" in the path (extra info format) are rejected as they have
 * a different response format than expected.
 */
@UtilityClass
@Slf4j
public class NgcArtifactUriValidator {

    private static final String MESG_INVALID_ARTIFACT_URI = """
            Invalid request: Specified URI '%s' does not match NGC Model or Resource Registry
            files path format -
            https://api(.stg|canary).ngc.nvidia.com/v2/org/:org-name(/team/:team-name)?/{models|resources}/:name/:version-id/files"
            """;

    /**
     * Validates a single artifact URI.
     *
     * @param uri          the URI to validate
     * @param artifactType the type of artifact (MODEL or RESOURCE)
     * @throws IllegalArgumentException if artifactType is not MODEL or RESOURCE
     */
    public static void validate(String uri, ArtifactTypeEnum artifactType) {
        var patterns = getPatterns(artifactType);
        if (!isValidUri(uri, patterns)) {
            var mesg = MESG_INVALID_ARTIFACT_URI.formatted(uri);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private static ValidationPatterns getPatterns(ArtifactTypeEnum artifactType) {
        return switch (artifactType) {
            case MODEL -> new ValidationPatterns(CAS_DIRECT_MODELS_REGISTRY_PATH,
                                                 CAS_MODELS_VERSIONS_PATH);
            case RESOURCE -> new ValidationPatterns(CAS_DIRECT_RESOURCES_REGISTRY_PATH,
                                                    CAS_RESOURCES_VERSIONS_PATH);
            default -> throw new IllegalArgumentException(
                    "Unsupported artifact type: " + artifactType);
        };
    }

    private static boolean isValidUri(String uri, ValidationPatterns patterns) {
        if (StringUtils.isBlank(uri)) {
            return false;
        }
        var matchesFilesPath = patterns.directPath().matcher(uri).matches();
        // The "versions" path has extra info and a different response format than expected
        var matchesVersionsPath = patterns.versionsPath().matcher(uri).matches();
        return matchesFilesPath && !matchesVersionsPath;
    }

    private record ValidationPatterns(Pattern directPath, Pattern versionsPath) {

    }
}
