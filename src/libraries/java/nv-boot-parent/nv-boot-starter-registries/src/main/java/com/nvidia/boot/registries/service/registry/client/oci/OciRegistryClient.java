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

package com.nvidia.boot.registries.service.registry.client.oci;

import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.NOT_FOUND;
import static org.springframework.http.HttpStatus.TOO_MANY_REQUESTS;
import static org.springframework.http.HttpStatus.UNAUTHORIZED;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciArtifactComponents;
import java.net.URI;
import java.time.Duration;
import java.util.regex.Pattern;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.HttpStatusCode;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;
import reactor.core.publisher.Mono;

@Slf4j
public abstract class OciRegistryClient {

    // Ref:
    // 1. https://github.com/distribution/distribution/blob/main/docs/content/spec/manifest-v2-2.md
    // 2. https://github.com/opencontainers/image-spec/blob/v1.0.0/media-types.md#compatibility-matrix
    public static final String IMAGE_MEDIA_TYPES =
            // Most common format, which may build from Docker 1.10 or later versions.
            "application/vnd.docker.distribution.manifest.v2+json,"
                    // Use to describe a list of image manifests for different platforms.
                    + "application/vnd.docker.distribution.manifest.list.v2+json,"
                    // Equivalent to Docker's manifest list, used for multi-platform images
                    // in the OCI ecosystem
                    + "application/vnd.oci.image.index.v1+json,"
                    // OCI image specification (v1.0 and v1.1), which may build by podman.
                    + "application/vnd.oci.image.manifest.v1+json";

    private static final String DEFAULT_TAG = "latest";

    private static final String MESG_EMPTY_ARTIFACT_URL =
            "OCI artifact URL cannot be null or empty";
    private static final String MESG_INVALID_OCI_ARTIFACT_URL_FORMAT =
            "Invalid OCI artifact URL format: %s";
    private static final String MESG_OCI_4XX_RESPONSE = "OCI registry response with 4xx error %s";
    private static final String MESG_OCI_5XX_RESPONSE = "OCI registry response with 5xx error %s";
    private static final String MESG_MANIFEST_VALIDATION_SUCCESSFUL =
            "OCI manifest validation successful for artifact: %s";

    /**
     * Regex pattern for OCI artifact URLs supporting the following formats:
     * - {registryHost}/{name}:{tag} or {registryHost}/{name}@{digest}
     * - oci://{registryHost}/{name}:{tag} or oci://{registryHost}/{name}@{digest}
     */
    private static final Pattern OCI_ARTIFACT_URL_PATTERN = Pattern.compile(
            "^(?:oci://)?(?<registryHost>[^/]+(?:\\.[^/]+)*)/(?<name>(?:[^/:@]+/)*[^/:@]+)(?::(?<tag>[^@]+)|@(?<digest>sha256:[a-f0-9]{64}))?$"
    );

    private final OciRegistryStubService ociRegistryStubService;
    @Getter
    private final OciRegistryAuthClient ociRegistryAuthClient;

    protected OciRegistryClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            Duration callTimeout,
            OciRegistryAuthClient ociRegistryAuthClient) {
        var timeout = callTimeout != null ? callTimeout : Duration.ofSeconds(30);
        this.ociRegistryStubService =
                getOciRegistryWebClient(webClientBuilder, timeout, OciRegistryStubService.class);
        this.ociRegistryAuthClient = ociRegistryAuthClient;
    }

    protected abstract String getRegistryBaseUrl(String registryHost);

    protected String getRegistryAuthScheme() {
        return "Bearer";
    }

    public void validateArtifact(
            String artifactUrl,
            String base64EncodedSecret) {
        validateArtifact(artifactUrl, base64EncodedSecret, IMAGE_MEDIA_TYPES);
    }

    // Media types are not strictly required for HEAD requests, but are recommended.
    // If not specified for the registry, IMAGE_MEDIA_TYPES is used by default to cover common cases.
    // Please verify requirements with the specific registry.
    public void validateArtifact(
            String artifactUrl,
            String base64EncodedSecret,
            String mediaTypes) {
        var components = parseArtifactUrl(artifactUrl);
        var authToken = ociRegistryAuthClient.getToken(components, base64EncodedSecret);
        validateArtifactManifest(getRegistryBaseUrl(components.registryHost()),
                                 components,
                                 getRegistryAuthScheme(),
                                 authToken.token(),
                                 StringUtils.defaultIfBlank(mediaTypes, IMAGE_MEDIA_TYPES));
    }

    private void validateArtifactManifest(
            String baseUrl,
            OciArtifactComponents components,
            String scheme,
            String token,
            String manifestMediaTypes) {
        var authorization = scheme + " " + token;
        var path = String.format("/v2/%s/manifests/%s", components.name(), components.reference());

        ociRegistryStubService.doesManifestExist(
                URI.create(baseUrl + path), authorization, manifestMediaTypes);
        var artifactPath = components.name() + ":" + components.reference();
        var mesg = MESG_MANIFEST_VALIDATION_SUCCESSFUL.formatted(artifactPath);
        log.debug(mesg);
    }

    public static <S> S getOciRegistryWebClient(WebClient.Builder webClientBuilder,
                                                Duration timeout,
                                                Class<S> serviceType) {
        var webClient = getOciRegistryWebClientBuilder(webClientBuilder, timeout).build();
        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        return factory.createClient(serviceType);
    }

    /**
     * Creates a WebClient.Builder for OCI registry operations using default Spring WebFlux Jackson codecs.
     * Supports content types: application/json, application/*+json, text/json
     * This covers OCI-specific types like application/vnd.oci.image.manifest.v1+json,
     * application/vnd.oci.image.index.v1+json, application/vnd.oci.image.config.v1+json.
     */
    private static WebClient.Builder getOciRegistryWebClientBuilder(WebClient.Builder webClientBuilder,
                                                                    Duration timeout) {
        return webClientBuilder
                .defaultStatusHandler(HttpStatusCode::is4xxClientError,
                                      OciRegistryClient::get4xxException)
                .defaultStatusHandler(HttpStatusCode::is5xxServerError,
                                      OciRegistryClient::get5xxException)
                .filter((request, next)
                                -> next.exchange(request).timeout(timeout));
    }

    @VisibleForTesting
    static OciArtifactComponents parseArtifactUrl(String artifactUrl) {
        if (StringUtils.isEmpty(artifactUrl)) {
            log.error(MESG_EMPTY_ARTIFACT_URL);
            throw new BadRequestException(MESG_EMPTY_ARTIFACT_URL);
        }

        var matcher = OCI_ARTIFACT_URL_PATTERN.matcher(artifactUrl);
        if (!matcher.matches()) {
            var mesg = MESG_INVALID_OCI_ARTIFACT_URL_FORMAT.formatted(artifactUrl);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var registryHost = matcher.group("registryHost");
        var name = matcher.group("name");
        var tag = matcher.group("tag");
        var digest = matcher.group("digest");

        if (StringUtils.isEmpty(registryHost)) {
            var mesg = MESG_INVALID_OCI_ARTIFACT_URL_FORMAT.formatted(
                    artifactUrl + " (missing registryHost)");
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if (StringUtils.isEmpty(name)) {
            var mesg = MESG_INVALID_OCI_ARTIFACT_URL_FORMAT.formatted(
                    artifactUrl + " (missing imageName)");
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var reference = StringUtils.firstNonBlank(tag, digest, DEFAULT_TAG);

        return new OciArtifactComponents(registryHost, name, reference);
    }

    private static Mono<BootResponseException> get4xxException(ClientResponse response) {
        var status = response.statusCode();
        var msg = MESG_OCI_4XX_RESPONSE.formatted(response.statusCode().value());
        log.error(msg);

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

        if (status.isSameCodeAs(TOO_MANY_REQUESTS)) {
            return response.bodyToMono(String.class)
                    .defaultIfEmpty("Too Many Requests")
                    .flatMap(body -> Mono.error(new TooManyRequestsException(body)));
        }

        return response.bodyToMono(String.class)
                .defaultIfEmpty("Bad Request")
                .flatMap(body -> Mono.error(new BadRequestException(body)));
    }

    private static Mono<BootResponseException> get5xxException(ClientResponse response) {
        var errorMsg = MESG_OCI_5XX_RESPONSE.formatted(response.statusCode());
        log.error(errorMsg);

        return response.bodyToMono(String.class)
                .switchIfEmpty(Mono.error(new UpstreamException(errorMsg)))
                .flatMap(body -> {
                    var mesg = errorMsg + " - " + body;
                    log.error(mesg);
                    return Mono.error(new UpstreamException(mesg));
                });
    }
}
