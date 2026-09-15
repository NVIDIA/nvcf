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

package com.nvidia.boot.registries.service.registry.client.ecr.pub;

import static com.nvidia.boot.registries.service.registry.client.ecr.EcrArtifactValidationService.validatePublicArtifact;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrCredentialValidationService.getEcrPublicAuthorizationToken;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.getEcrWebClientBuilder;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.parsePublicArtifactUrl;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.toBaseUrlForEcrPublicRegistry;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.client.ecr.EcrArtifactRegistryStubService;
import java.net.URI;
import java.time.Duration;
import java.util.regex.Pattern;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

/**
 * Client for interacting with ECR Public Artifact Registry.
 * Handles artifact image URL parsing, signature generation, and validation.
 */
@Slf4j
public class EcrPublicArtifactRegistryClient {

    /**
     * Expected format: oci://public.ecr.aws/{registry_alias}/{repository}:{tag}
     * or oci://public.ecr.aws/{registry_alias}/{repository}@{digest}
     */
    public static final Pattern ECR_PUBLIC_HELM_CHART_URL_PATTERN = Pattern.compile(
            "^oci://public\\.ecr\\.aws/(?<registryAlias>[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?)/(?<repository>[a-z0-9](?:[a-z0-9._/-]*[a-z0-9])?)(?::(?<tag>[A-Za-z0-9._-]+))?(?:@(?<digest>sha256:[a-f0-9]{64}))?$"
    );

    private final EcrArtifactRegistryStubService ecrPublicStubService;
    @Getter
    private final String hostname;

    public EcrPublicArtifactRegistryClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            String hostname,
            Duration callTimeout) {
        var artifactRegistryUrl = hostname.startsWith("http")
                ? hostname : "https://" + hostname;
        this.hostname = URI.create(artifactRegistryUrl).getHost();
        var timeout = callTimeout != null ? callTimeout : Duration.ofSeconds(30);
        var webClient = getEcrWebClientBuilder(webClientBuilder, timeout)
                .baseUrl(toBaseUrlForEcrPublicRegistry(artifactRegistryUrl))
                .build();
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.ecrPublicStubService = factory.createClient(EcrArtifactRegistryStubService.class);
    }

    /**
     * Validates a helm chart by checking its existence in the ECR Public registry.
     *
     * @param helmChartUrl The helm chart URL to validate (format: oci://public.ecr.aws/{repository}:{tag})
     * @param base64ApiKey Base64 encoded credentials (accessKeyId:secretAccessKey)
     * @throws BadRequestException if the chart is invalid or inaccessible
     */
    public void validateHelmChart(String helmChartUrl, String base64ApiKey) {
        var components = parsePublicArtifactUrl(helmChartUrl, ECR_PUBLIC_HELM_CHART_URL_PATTERN);
        validatePublicArtifact(ecrPublicStubService, components, base64ApiKey);
    }

    public void validateCredential(String hostname, String base64EncodedSecret) {
        getEcrPublicAuthorizationToken(ecrPublicStubService, base64EncodedSecret);
        log.info("Successfully validated ECR public credentials for hostname: {}", hostname);
    }
}
