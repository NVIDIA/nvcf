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

package com.nvidia.boot.registries.service.registry.client.volcengine;

import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineArtifactValidationService.validateArtifact;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineCredentialValidationService.getAuthorizationToken;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.createVolcengineWebClient;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.parseArtifactUrl;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.parseHostname;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.toBaseUrl;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineArtifactComponents;
import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineArtifactType;
import java.net.URI;
import java.time.Duration;
import java.util.regex.Pattern;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

/**
 * Client for interacting with Volcengine Artifact Registry.
 * Handles artifact image URL parsing, signature generation, and validation.
 */
@Slf4j
public class VolcengineArtifactRegistryClient {

    public static final Pattern HELM_CHART_URL_PATTERN = Pattern.compile(
            "^oci://(?<registry>[\\w-]+)-(?<region>(?:cn|ap)-[\\w-]+)\\.cr\\.volces\\.com/(?<namespace>[\\w._-]+)/(?<repository>[\\w._/-]+)(?::(?<tag>[\\w._-]+))?$"
    );

    private final VolcengineArtifactRegistryStubService volcengineStubService;
    @Getter
    private String hostname;

    public VolcengineArtifactRegistryClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            String hostname,
            Duration callTimeout) {
        var artifactRegistryUrl = hostname.startsWith("http")
                ? hostname : "https://" + hostname;
        this.hostname = URI.create(artifactRegistryUrl).getHost();

        var timeout = callTimeout != null ? callTimeout : Duration.ofSeconds(30);
        var webClient = createVolcengineWebClient(webClientBuilder, toBaseUrl(artifactRegistryUrl), timeout);
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.volcengineStubService =
                factory.createClient(VolcengineArtifactRegistryStubService.class);
    }

    /**
     * Validates a helm chart by checking its existence in the Volcengine registry.
     *
     * @param helmChartUrl The helm chart URL to validate
     * @throws BadRequestException if the chart is invalid or inaccessible
     */
    public void validateHelmChart(String helmChartUrl, String base64ApiKey) {
        VolcengineArtifactComponents components = parseArtifactUrl(
                helmChartUrl,
                HELM_CHART_URL_PATTERN,
                VolcengineArtifactType.CHART);
        validateArtifact(volcengineStubService, components, base64ApiKey);
    }

    /**
     * Validates credentials for a Volcengine registry by retrieving an authorization token.
     *
     * @param hostname The Volcengine registry hostname
     * @param base64EncodedSecret The base64-encoded credentials
     */
    public void validateCredential(String hostname, String base64EncodedSecret) {
        var registryInfo = parseHostname(hostname);
        getAuthorizationToken(volcengineStubService, registryInfo.registry(), 
                             registryInfo.region(), base64EncodedSecret);
    }
}
