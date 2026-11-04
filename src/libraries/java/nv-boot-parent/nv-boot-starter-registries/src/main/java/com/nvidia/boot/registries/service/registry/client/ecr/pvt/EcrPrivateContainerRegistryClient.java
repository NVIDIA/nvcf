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

package com.nvidia.boot.registries.service.registry.client.ecr.pvt;

import static com.nvidia.boot.registries.service.registry.client.ecr.EcrArtifactValidationService.validatePrivateArtifact;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrCredentialValidationService.getEcrPrivateAuthorizationToken;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.getEcrWebClientBuilder;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.parsePrivateArtifactUrl;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.parseRegionFromEcrPrivateHostname;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.toBaseUrlForEcrPrivateRegistry;

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
 * Client for interacting with Amazon ECR.
 * Handles container image URL parsing and validation using AWS ECR DescribeImages API.
 */
@Slf4j
public class EcrPrivateContainerRegistryClient {

    /**
     * Expected format: {aws_account_id}.dkr.ecr.{region}.amazonaws.com/{repository}:{tag}
     * or {aws_account_id}.dkr.ecr.{region}.amazonaws.com/{repository}@{digest}
     */
    public static final Pattern ECR_CONTAINER_IMAGE_URL_PATTERN = Pattern.compile(
            "^(?<registryId>\\d{12})\\.dkr\\.ecr\\.(?<region>[a-z0-9-]+)\\.amazonaws\\.com/(?<repository>[a-z0-9][a-z0-9._/-]*[a-z0-9])(?::(?<tag>[A-Za-z0-9._-]+))?(?:@(?<digest>sha256:[a-f0-9]{64}))?$");

    private final EcrArtifactRegistryStubService ecrStubService;
    @Getter
    private String hostname;
    private final String baseUrl;

    public EcrPrivateContainerRegistryClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            String hostname,
            Duration callTimeout) {
        var containerRegistryUrl = hostname.startsWith("http")
                ? hostname : "https://" + hostname;
        this.hostname = URI.create(containerRegistryUrl).getHost();
        this.baseUrl = containerRegistryUrl;
        var timeout = callTimeout != null ? callTimeout : Duration.ofSeconds(30);
        var webClient = getEcrWebClientBuilder(webClientBuilder, timeout).build();
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.ecrStubService = factory.createClient(EcrArtifactRegistryStubService.class);
    }

    /**
     * Validates a container image by checking its existence in the ECR registry using DescribeImages API.
     *
     * @param containerImageUrl The container image URL to validate
     * @param base64ApiKey      Base64 encoded credentials (accessKeyId:secretAccessKey)
     * @throws BadRequestException if the image is invalid or inaccessible
     */
    public void validateContainerImage(String containerImageUrl, String base64ApiKey) {
        var components = parsePrivateArtifactUrl(containerImageUrl,
                                                 ECR_CONTAINER_IMAGE_URL_PATTERN);
        validatePrivateArtifact(ecrStubService,
                                toBaseUrlForEcrPrivateRegistry(baseUrl, components.region()),
                                components,
                                base64ApiKey);
    }

    public void validateCredential(String hostname, String base64EncodedSecret) {
        var region = parseRegionFromEcrPrivateHostname(hostname);
        var ecrEndpointUrl = toBaseUrlForEcrPrivateRegistry(baseUrl, region);

        getEcrPrivateAuthorizationToken(ecrStubService, ecrEndpointUrl, region, base64EncodedSecret);
        log.info("Successfully validated ECR private credentials for hostname: {}", hostname);
    }
}
