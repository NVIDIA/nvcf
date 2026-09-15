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

package com.nvidia.boot.registries.service.registry.client.harbor;

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.buildUrlWithQueryParams;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.toRegistryBaseUrl;

import com.nvidia.boot.registries.service.registry.client.oci.OciRegistryAuthClient;
import java.time.Duration;
import java.util.LinkedHashMap;
import lombok.Getter;
import org.apache.commons.lang3.StringUtils;
import org.springframework.web.reactive.function.client.WebClient;

public class HarborRegistryAuthClient extends OciRegistryAuthClient {

    @Getter
    private final String authBaseUrl;
    private static final String AUTH_SERVICE = "harbor-registry";

    public HarborRegistryAuthClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            String authBaseUrl,
            Duration callTimeout) {
        super(webClientBuilder, callTimeout);
        this.authBaseUrl = authBaseUrl;
    }

    @Override
    protected String getCanonicalAuthTokenUrl(String registryHost, String name) {
        var baseUrl = toRegistryBaseUrl(registryHost, authBaseUrl) + "/service/token";

        var queryParams = new LinkedHashMap<String, String>();
        queryParams.put("service", AUTH_SERVICE);
        
        if (StringUtils.isNotBlank(name)) {
            queryParams.put("scope", "repository:" + name + ":pull");
        }
        
        return buildUrlWithQueryParams(baseUrl, queryParams);
    }
}
