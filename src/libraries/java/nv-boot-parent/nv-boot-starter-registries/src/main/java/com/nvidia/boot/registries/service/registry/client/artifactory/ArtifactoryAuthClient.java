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

package com.nvidia.boot.registries.service.registry.client.artifactory;

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.buildUrlWithQueryParams;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toRegistryBaseUrl;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.client.oci.OciRegistryAuthClient;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.web.reactive.function.client.WebClient;

@Slf4j
public class ArtifactoryAuthClient extends OciRegistryAuthClient {

    // Pattern to parse: <repository>/<image-name>
    private static final Pattern NAME_PATTERN = Pattern.compile("^([^/]+)/(.+)$");
    private static final String MESG_INVALID_IMAGE_NAME_FORMAT =
            "Invalid image name format: %s.";

    @Getter
    private final String authBaseUrl;

    public ArtifactoryAuthClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            String authBaseUrl,
            Duration callTimeout) {
        super(webClientBuilder, callTimeout);
        this.authBaseUrl = authBaseUrl;
    }

    @Override
    protected String getCanonicalAuthTokenUrl(String registryHost, String name) {
        String baseUrl = toRegistryBaseUrl(registryHost, authBaseUrl) + "/v2/token";
        var queryParams = new LinkedHashMap<String, String>();
        queryParams.put("service", registryHost);
        
        if (StringUtils.isNotBlank(name)) {
            // Extract repository and image name from the full name (format: repository/image-name)
            Matcher matcher = NAME_PATTERN.matcher(name);
            if (matcher.matches()) {
                var imageName = matcher.group(2);
                queryParams.put("scope", "repository:" + imageName + ":pull");
            } else {
                var message = String.format(MESG_INVALID_IMAGE_NAME_FORMAT, name);
                log.error(message);
                throw new BadRequestException(message);
            }
        }
        
        return buildUrlWithQueryParams(baseUrl, queryParams);
    }
}
