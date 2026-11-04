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

package com.nvidia.boot.registries.service.registry.client.docker;

import static org.springframework.http.HttpStatus.CONFLICT;
import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.NOT_FOUND;
import static org.springframework.http.HttpStatus.TOO_MANY_REQUESTS;
import static org.springframework.http.HttpStatus.UNAUTHORIZED;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Expiry;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ConflictException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.registries.service.registry.auth.DockerRegistryAuthServiceStubService;
import com.nvidia.boot.registries.service.registry.auth.DockerRegistryAuthServiceStubService.DockerRegistryAuthResponse;
import jakarta.validation.constraints.NotNull;
import java.net.URI;
import java.time.Duration;
import java.util.Optional;
import java.util.regex.Pattern;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatusCode;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;
import reactor.core.publisher.Mono;

/**
 * Client for interacting with Docker Registry.
 * Handles container/helm image URL parsing and validation.
 */
@Slf4j
public class DockerRegistryClient {

    private static final String DOCKER_REGISTRY_AUTH_SERVICE_PARAMETER = "registry.docker.io";

    private static final String MESG_INVALID_DOCKER_IMAGE_URL_FORMAT_RESPONSE =
            "Invalid Docker image url format.";
    private static final String MESG_EMPTY_CONTAINER_HELM_URL_RESPONSE =
            "Container/helm image URL cannot be null or empty";
    private static final String MESG_NULL_RESPONSE =
            "Null response from getting bearer token from registry %s";
    private static final String MESG_DOCKER_5XX_RESPONSE = "Docker response with 5xx error %s";
    private static final String MESG_DOCKER_AUTH_5XX_RESPONSE = "Docker Auth response with 5xx " +
            "error %s";
    private static final String MESG_MANIFEST_VALIDATION_SUCCESSFUL =
            "Manifest validation successful for image: {}";
    private static final String MESG_DOCKER_RESPONSE_STATUS_CODE =
            "Docker Response Status Code '%d'";
    private static final Pattern CONTAINER_HELM_IMAGE_URL_PATTERN = Pattern.compile(
            "^(?:oci://)?(?<registryHost>[^/]+)/(?<namespace>[^/]+)/(?<repository>[^/:@]+)(?::" +
                    "(?<tag>[^@]+)|@(?<digest>sha256:[a-fA-F0-9]{64}))?$");
    private static final String DEFAULT_IMAGE_TAG = "latest";

    private final DockerRegistryStubService dockerRegistryStub;
    private final DockerRegistryAuthServiceStubService dockerRegistryAuthServiceStub;
    private final String groupScope;
    private final String baseUrl;
    @Getter
    private String hostname;

    private record RegistryAuthKey(String namespace, String repository, String scope, String apiKey) {
    }

    private static final int AUTH_TOKEN_CACHE_SIZE = 2048;
    private final LoadingCache<RegistryAuthKey, DockerRegistryAuthResponse> registryAuthCache =
            Caffeine.newBuilder()
                    .maximumSize(AUTH_TOKEN_CACHE_SIZE)
                    .expireAfter(new AuthTokenExpiry())
                    .scheduler(Scheduler.systemScheduler())
                    .build(this::fetchAuthToken);

    /**
     * Record to hold the components of a container/helm image URL.
     */
    public record DockerImageComponents(
            String registryHost,
            String namespace,
            String repository,
            String tag,
            String digest
    ) {
    }

    public DockerRegistryClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            String dockerHostname,
            Duration callTimeout,
            String oauth2BaseUrl,
            String oauth2GroupScope) {
        var registryUrl = dockerHostname.startsWith("http")
                ? dockerHostname : "https://" + dockerHostname;
        this.hostname = URI.create(registryUrl).getHost();
        this.baseUrl = registryUrl;
        this.groupScope = oauth2GroupScope;
        var authWebClient = webClientBuilder.clone()
                .baseUrl(oauth2BaseUrl)
                .defaultStatusHandler(HttpStatusCode::is4xxClientError,
                                      DockerRegistryClient::get4xxException)
                .defaultStatusHandler(HttpStatusCode::is5xxServerError, resp -> {
                    var errorMesg = MESG_DOCKER_AUTH_5XX_RESPONSE + resp.statusCode();
                    log.error(errorMesg);
                    return resp.bodyToMono(String.class)
                            .switchIfEmpty(Mono.error(new UpstreamException(errorMesg)))
                            .flatMap(body -> {
                                var mesg = errorMesg + " - " + body;
                                log.error(mesg);
                                return Mono.error(new UpstreamException(mesg));
                            });
                })
                .filter((request, next) ->
                                next.exchange(request).timeout(callTimeout))
                .build();
        var adapter = WebClientAdapter.create(authWebClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.dockerRegistryAuthServiceStub =
                factory.createClient(DockerRegistryAuthServiceStubService.class);
        var webClient = webClientBuilder.clone()
                .baseUrl(transformBaseUrlForDockerRegistry(baseUrl))
                .defaultStatusHandler(HttpStatusCode::is4xxClientError,
                                      DockerRegistryClient::get4xxException)
                .defaultStatusHandler(HttpStatusCode::is5xxServerError, resp -> {
                    var errorMesg = MESG_DOCKER_5XX_RESPONSE + resp.statusCode();
                    log.error(errorMesg);
                    return resp.bodyToMono(String.class)
                            .switchIfEmpty(Mono.error(new UpstreamException(errorMesg)))
                            .flatMap(body -> {
                                var mesg = errorMesg + " - " + body;
                                log.error(mesg);
                                return Mono.error(new UpstreamException(mesg));
                            });
                })
                .filter((request, next) ->
                                next.exchange(request).timeout(callTimeout))
                .build();
        adapter = WebClientAdapter.create(webClient);
        factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.dockerRegistryStub =
                factory.createClient(DockerRegistryStubService.class);
    }

    @VisibleForTesting
    public void setHostname(String hostname) {
        this.hostname = hostname;
    }

    public void resetAuthTokenCache() {
        this.registryAuthCache.invalidateAll();
    }

    // Transforms the base URL for Docker registry usage.
    // For testing: removes "-docker" suffix from localhost URLs
    // For production: transforms "docker.io" to "registry-1.docker.io"
    private static String transformBaseUrlForDockerRegistry(String baseUrl) {
        // Design for 3rd Party Registry requires hostnames to be unique. However, when
        // integration tests involving multiple registries are being executed, this
        // becomes an issue as all the registries use "localhost" as the hostname in the
        // baseUrl. To make the hostnames unique in the application-test.yaml files of
        // apps such as NVCF API and NVCT API, we use localhost-<registry-key>:<port>
        // as the baseUrl. For example, localhost-ngc:<port>, localhost-docker:<port>,
        // etc. When using the baseUrl, we remove the `-<registry-key>` part so that
        // the client can communicate with the registry-specific mock server.
        if (baseUrl.contains("localhost")) {
            return baseUrl.replace("-docker", "");
        }

        // For real world usage, we need to call registry-1.docker.io api endpoint.
        // In the application.yaml, we specify the docker host name as "docker.io". This
        // way users can refer to their docker images more naturally. But to validate
        // whether the image exists, we need to make calls to
        // "registry-1.docker.io"
        if (baseUrl.contains("docker.io")) {
            return baseUrl.replace("docker.io", "registry-1.docker.io");
        }

        return baseUrl;
    }

    /**
     * Validates a container/helm image by checking its existence and accessibility in the Docker
     * registry.
     *
     * @param imageUrl The container/helm image URL to validate
     * @param base64ApiKey      The base64 encoded API key in format "username:password" for
     *                          authentication
     * @throws BadRequestException if the image is invalid or inaccessible
     */
    public void validateImage(String imageUrl, String base64ApiKey) {
        var components = parseImageUrl(imageUrl);
        String bearerToken = authenticateWithRegistry(components, base64ApiKey);
        validateImageManifest(components, bearerToken);
    }

    /**
     * Parses a container/helm image URL into its components.
     * Expected format: docker.io/{namespace}/{repositories}:{tags}
     * or docker.io/{namespace}/{repositories}:{digest}
     * or docker.io/{namespace}/{repositories}
     *
     * @param imageUrl The full container/helm image URL to parse
     * @return ContainerImageComponents containing the parsed components
     * @throws BadRequestException if the URL format is invalid
     */
    @VisibleForTesting
    public static DockerImageComponents parseImageUrl(String imageUrl) {
        if (imageUrl == null || imageUrl.isBlank()) {
            throw new BadRequestException(MESG_EMPTY_CONTAINER_HELM_URL_RESPONSE);
        }

        var matcher = CONTAINER_HELM_IMAGE_URL_PATTERN.matcher(imageUrl);
        if (!matcher.matches()) {
            throw new BadRequestException(MESG_INVALID_DOCKER_IMAGE_URL_FORMAT_RESPONSE);
        }

        var registryHost = matcher.group("registryHost");
        var namespace = matcher.group("namespace");
        var repository = matcher.group("repository");
        var tag = matcher.group("tag");
        var digest = matcher.group("digest");
        if (tag == null && digest == null) {
            tag = DEFAULT_IMAGE_TAG;
        }

        return new DockerImageComponents(registryHost, namespace, repository, tag, digest);
    }

    public String validateCredential(String registryHost, String base64encodedSecret) {
        var authResponse = dockerRegistryAuthServiceStub
                .fetchToken("Basic " + base64encodedSecret, DOCKER_REGISTRY_AUTH_SERVICE_PARAMETER);
        return Optional.ofNullable(authResponse)
                .map(DockerRegistryAuthResponse::getToken)
                .orElseThrow(() -> {
                    var mesg = MESG_NULL_RESPONSE
                            .formatted(registryHost);
                    log.error(mesg);
                    return new ForbiddenException(mesg);
                });
    }

    private String authenticateWithRegistry(DockerImageComponents components,
                                            String base64ApiKey) {
        var authKey =
                new RegistryAuthKey(components.namespace(), components.repository(), this.groupScope, base64ApiKey);
        return registryAuthCache.get(authKey).getToken();
    }

    private void validateImageManifest(DockerImageComponents components, String bearerToken) {
        // do not care about the response body
        dockerRegistryStub
                .getManifest(components.namespace(),
                             components.repository(),
                             components.digest() != null ? components.digest() : components.tag(),
                             "Bearer " + bearerToken);
        // If we reach here, the call succeeded (2xx status code)
        log.debug(MESG_MANIFEST_VALIDATION_SUCCESSFUL,
                  components.namespace() + "/" + components.repository());
    }

    private DockerRegistryAuthResponse fetchAuthToken(RegistryAuthKey authKey) {
        var authResponse = dockerRegistryAuthServiceStub
                .proxyAuth(authKey.namespace(),
                           authKey.repository(),
                           authKey.scope(),
                           "Basic " + authKey.apiKey());
        return Optional.ofNullable(authResponse)
                .orElseThrow(() -> {
                    var mesg = MESG_NULL_RESPONSE
                            .formatted(authKey.repository());
                    log.error(mesg);
                    return new ForbiddenException(mesg);
                });
    }

    @NotNull
    private static Mono<BootResponseException> get4xxException(ClientResponse response) {
        var status = response.statusCode();
        var mesg = MESG_DOCKER_RESPONSE_STATUS_CODE.formatted(response.statusCode().value());
        log.error(mesg);

        if (status.isSameCodeAs(UNAUTHORIZED)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Unauthorized")
                    .flatMap(body -> Mono.error(new UnauthorizedException(body)));
        }

        if (status.isSameCodeAs(FORBIDDEN)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Forbidden")
                    .flatMap(body -> Mono.error(new ForbiddenException(body)));
        }

        if (status.isSameCodeAs(NOT_FOUND)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Not Found")
                    .flatMap(body -> Mono.error(new NotFoundException(body)));
        }

        if (status.isSameCodeAs(CONFLICT)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Conflict")
                    .flatMap(body -> Mono.error(new ConflictException(body)));
        }

        if (status.isSameCodeAs(TOO_MANY_REQUESTS)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Too Many Requests")
                    .flatMap(body -> Mono.error(new TooManyRequestsException(body)));
        }

        return response.bodyToMono(String.class)
                .defaultIfEmpty("Bad Request")
                .flatMap(body -> Mono.error(new BadRequestException(body)));
    }

    private static class AuthTokenExpiry
            implements Expiry<RegistryAuthKey, DockerRegistryAuthResponse> {
        private static final Duration DEFAULT_EXPIRY = Duration.ofMinutes(5);

        @Override
        public long expireAfterCreate(RegistryAuthKey key,
                                      DockerRegistryAuthResponse value,
                                      long currentTime) {
            if (value.getExpiresIn() != null) {
                // Expire at 3/4 of the token's actual expiration time
                return Duration.ofSeconds(value.getExpiresIn()).toNanos() * 3 / 4;
            }
            // Default to 5 minutes if no expiry info is provided
            return DEFAULT_EXPIRY.toNanos();
        }

        @Override
        public long expireAfterUpdate(RegistryAuthKey key,
                                      DockerRegistryAuthResponse value,
                                      long currentTime, long currentDuration) {
            return currentDuration;
        }

        @Override
        public long expireAfterRead(RegistryAuthKey key,
                                    DockerRegistryAuthResponse value,
                                    long currentTime, long currentDuration) {
            return currentDuration;
        }
    }
}
