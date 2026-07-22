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

import static com.nvidia.boot.registries.service.registry.client.oci.OciRegistryClient.IMAGE_MEDIA_TYPES;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Expiry;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import java.net.URI;
import java.time.Duration;
import java.util.Optional;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * Client for interacting with NGC Container Registry.
 * Handles container image URL parsing and validation.
 */
@Slf4j
public class NgcContainerRegistryClient {

    private static final String DEFAULT_IMAGE_TAG = "latest";

    private static final String MESG_INVALID_NGC_DOCKER_IMAGE_URL_FORMAT_RESPONSE =
            "Invalid NGC docker image url format.";
    private static final String MESG_EMPTY_CONTAINER_URL_RESPONSE =
            "Container image URL cannot be null or empty";
    private static final String MESG_NULL_RESPONSE =
            "Null response from getting bearer token from registry %s";

    private final NgcContainerRegistryStub ngcContainerRegistryStub;
    private final String containerBaseUrl;

    private record RegistryAuthKey(String repository, String imageName, String apiKey) {

    }

    // Base on prod metrics, there are around 1000 request for function/deployment
    // creation or updates per hour. We doubled the number to allow extremely cases.
    private static final int AUTH_TOKEN_CACHE_SIZE = 2048;
    private final LoadingCache<RegistryAuthKey, NgcContainerRegistryStub.NgcRegistryAuthResponse>
            registryAuthCache =
            Caffeine.newBuilder()
                    .maximumSize(AUTH_TOKEN_CACHE_SIZE)
                    .expireAfter(new AuthTokenExpiry())
                    .scheduler(Scheduler.systemScheduler())
                    .build(this::fetchAuthToken);

    @Getter
    private String hostname;

    /**
     * Record to hold the components of a container image URL.
     */
    public record ContainerImageComponents(
            String registryHost,
            String repository,
            String imageName,
            String tag,
            String digest
    ) {

    }

    private static final Pattern CONTAINER_IMAGE_URL_PATTERN = Pattern.compile(
            "^(?<registryHost>[^/]+)/(?<repository>(?:[^/]+/)*)(?<imageName>[^/:@]+)(?::(?<tag>[^@]+))?(?:@(?<digest>.+))?$");

    public NgcContainerRegistryClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            String containerHostname,
            Duration exchangeTimeout,
            Duration responseTimeout,
            Duration writeTimeout,
            Duration connectTimeout) {
        var containerRegistryUrl = containerHostname.startsWith("http")
                ? containerHostname : "https://" + containerHostname;
        // Normalized again.
        this.hostname = URI.create(containerRegistryUrl).getHost();

        // Design for 3rd Party Registry requires hostnames to be unique. However, when
        // integration tests involving multiple registries are being executed, this
        // becomes an issue as all the registries use "localhost" as the hostname in the
        // baseUrl. To make the hostnames unique in the application-test.yaml files of
        // apps such as NVCF API and NVCT API, we use localhost-<registry-key>:<port>
        // as the baseUrl. For example, localhost-ngc:<port>, localhost-docker:<port>,
        // etc. When using the baseUrl, we remove the `-<registry-key>` part so that
        // the client can communicate with the registry-specific mock server.
        log.info("NgcContainerRegistryClient init for hostname {} with exchangeTimeout: {},"
                + " connectTimeout: {}, responseTimeout: {}, writeTimeout: {}",
                hostname, exchangeTimeout, connectTimeout, responseTimeout, writeTimeout);
        this.containerBaseUrl = containerRegistryUrl.replace("-ngc", "");
        var webClient = WebClientUtils.createWebClient(
                webClientBuilder,
                containerBaseUrl, exchangeTimeout, connectTimeout, responseTimeout, writeTimeout,
                0);
        this.ngcContainerRegistryStub = WebClientUtils.createStubService(
                webClient, NgcContainerRegistryStub.class);
    }

    @VisibleForTesting
    public void setHostname(String hostname) {
        this.hostname = hostname;
    }

    public void resetAuthTokenCache() {
        this.registryAuthCache.invalidateAll();
    }

    /**
     * Parses a container image URL into its components.
     * Expected format: [registry-host]/[repository]/[image-name]:[tag] or [registry-host]/[repository]/[image-name]@[digest]
     *
     * @param containerImageUrl The full container image URL to parse
     * @return ContainerImageComponents containing the parsed components
     * @throws BadRequestException if the URL format is invalid
     */
    public static ContainerImageComponents parseContainerImageUrl(String containerImageUrl) {
        validateContainerImageUrl(containerImageUrl);
        return extractContainerImageComponents(containerImageUrl);
    }

    private static void validateContainerImageUrl(String containerImageUrl) {
        if (containerImageUrl == null || containerImageUrl.isBlank()) {
            throw new BadRequestException(MESG_EMPTY_CONTAINER_URL_RESPONSE);
        }

        // Check for multiple colons or @ symbols before regex matching
        if (containerImageUrl.chars().filter(ch -> ch == ':').count() > 1 ||
                containerImageUrl.chars().filter(ch -> ch == '@').count() > 1) {
            throw new BadRequestException(MESG_INVALID_NGC_DOCKER_IMAGE_URL_FORMAT_RESPONSE);
        }
    }

    private static ContainerImageComponents extractContainerImageComponents(
            String containerImageUrl) {
        Matcher matcher = CONTAINER_IMAGE_URL_PATTERN.matcher(containerImageUrl);
        if (!matcher.matches()) {
            throw new BadRequestException(MESG_INVALID_NGC_DOCKER_IMAGE_URL_FORMAT_RESPONSE);
        }

        String registryHost = matcher.group("registryHost");
        String repository = matcher.group("repository").replaceAll("/$", "");
        String imageName = matcher.group("imageName");
        String tag = matcher.group("tag");
        String digest = matcher.group("digest");

        if (tag == null && digest == null) {
            tag = DEFAULT_IMAGE_TAG;
        }

        return new ContainerImageComponents(registryHost, repository, imageName, tag, digest);
    }

    /**
     * Validates a container image by checking its existence and accessibility in the NGC registry.
     *
     * @param containerImageUrl The container image URL to validate
     * @param base64ApiKey      The base64 encoded API key in format "username:password" for authentication
     * @throws BadRequestException if the image is invalid or inaccessible
     */
    public void validateContainerImage(String containerImageUrl, String base64ApiKey) {
        ContainerImageComponents components = parseContainerImageUrl(containerImageUrl);
        RegistryAuthKey authKey =
                new RegistryAuthKey(components.repository(), components.imageName(), base64ApiKey);
        String bearerToken = registryAuthCache.get(authKey).getToken();
        validateImageManifest(components, bearerToken);
    }

    public String validateCredential(String registryHost, String base64EncodedSecret) {
        var authResponse = ngcContainerRegistryStub.proxyAuth(
                "Basic " + base64EncodedSecret, "$oauthtoken", null);
        return Optional.ofNullable(authResponse)
                .map(NgcContainerRegistryStub.NgcRegistryAuthResponse::getToken)
                .orElseThrow(() -> {
                    var mesg = MESG_NULL_RESPONSE
                            .formatted(registryHost);
                    log.error(mesg);
                    return new ForbiddenException(mesg);
                });
    }

    private NgcContainerRegistryStub.NgcRegistryAuthResponse fetchAuthToken(
            RegistryAuthKey authKey) {
        String base64ApiKey = authKey.apiKey();
        String scope =
                "repository:%s:pull".formatted(authKey.repository() + "/" + authKey.imageName());
        log.info("authenticate registry scope: {}", scope);
        return ngcContainerRegistryStub.proxyAuth(
                "Basic " + base64ApiKey, "$oauthtoken", scope);
    }

    private void validateImageManifest(ContainerImageComponents components, String bearerToken) {
        String tag = components.digest() != null ? components.digest() : components.tag();
        String imagePath = components.repository() + "/" + components.imageName();
        var uri = URI.create(containerBaseUrl + "/v2/" + imagePath + "/manifests/" + tag);
        ngcContainerRegistryStub.validateManifest(
                uri, "Bearer " + bearerToken, IMAGE_MEDIA_TYPES);
    }

    private static class AuthTokenExpiry
            implements Expiry<RegistryAuthKey, NgcContainerRegistryStub.NgcRegistryAuthResponse> {

        @Override
        public long expireAfterCreate(RegistryAuthKey key,
                                      NgcContainerRegistryStub.NgcRegistryAuthResponse value,
                                      long currentTime) {
            return Duration.ofSeconds(value.getExpiresIn()).toNanos() * 3 / 4;
        }

        @Override
        public long expireAfterUpdate(RegistryAuthKey key,
                                      NgcContainerRegistryStub.NgcRegistryAuthResponse value,
                                      long currentTime, long currentDuration) {
            return currentDuration;
        }

        @Override
        public long expireAfterRead(RegistryAuthKey key,
                                    NgcContainerRegistryStub.NgcRegistryAuthResponse value,
                                    long currentTime, long currentDuration) {
            return currentDuration;
        }
    }
}
