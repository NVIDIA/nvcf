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
import static org.springframework.http.HttpStatus.UNAUTHORIZED;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Expiry;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import com.nvidia.boot.registries.service.registry.client.ngc.dto.WwwAuthenticateChallenge;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.Getter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.HttpHeaders;
import org.springframework.http.ResponseEntity;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.util.UriComponentsBuilder;
import org.springframework.web.util.UriUtils;
import reactor.core.publisher.Mono;

/**
 * Client for interacting with NGC Container Registry.
 * Handles container image URL parsing and validation.
 */
@Slf4j
public class NgcContainerRegistryClient {

    private static final String DEFAULT_IMAGE_TAG = "latest";

    private static final String BASE_ENDPOINT_PATH = "/v2/";
    private static final String SERVICE_PARAM = "service";
    private static final String SCOPE_PARAM = "scope";

    private static final String MESG_INVALID_NGC_DOCKER_IMAGE_URL_FORMAT_RESPONSE =
            "Invalid NGC docker image url format.";
    private static final String MESG_EMPTY_CONTAINER_URL_RESPONSE =
            "Container image URL cannot be null or empty";
    private static final String MESG_REGISTRY_CREDENTIALS_MISSING =
            "Registry credentials are required";
    private static final String MESG_NO_TOKEN_ISSUED =
            "Registry token endpoint %s returned no token";
    private static final String MESG_NO_CHALLENGE =
            "Registry endpoint %s did not present an authentication challenge (status %s), "
                    + "so the credential cannot be verified";
    private static final String MESG_INVALID_REALM =
            "Registry challenge advertised an unusable realm %s";

    private final NgcContainerRegistryStub ngcContainerRegistryStub;
    private final NgcChallengeProbeStub ngcChallengeProbeStub;
    private final String containerBaseUrl;

    private record RegistryAuthKey(String repository, String imageName, String apiKey) {

    }

    // Base on prod metrics, there are around 1000 request for function/deployment
    // creation or updates per hour. We doubled the number to allow extremely cases.
    private static final int AUTH_TOKEN_CACHE_SIZE = 2048;
    // Loaded through get(key, closure) rather than a LoadingCache loader: discovery probes the
    // manifest being validated, and the manifest reference is deliberately not part of the key
    // (tag and digest for one repository share a token), so it has to travel with the call.
    private final Cache<RegistryAuthKey, NgcContainerRegistryStub.NgcRegistryAuthResponse>
            registryAuthCache =
            Caffeine.newBuilder()
                    .maximumSize(AUTH_TOKEN_CACHE_SIZE)
                    .expireAfter(new AuthTokenExpiry())
                    .scheduler(Scheduler.systemScheduler())
                    .build();

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

        // A probe reads the WWW-Authenticate challenge off the 401, so the 401 must survive as
        // a response instead of becoming UnauthorizedException. The empty-Mono handler marks it
        // as a non-error; registered before the factory's standard handlers because status
        // handlers are consulted in insertion order and the first match wins. Only the probes
        // use this client - a token request answered with 401 means the credentials are bad and
        // must still throw. Cloned so the probe handler stays off the injected builder.
        var probeBuilder = webClientBuilder.clone()
                .defaultStatusHandler(status -> status.isSameCodeAs(UNAUTHORIZED),
                                      response -> Mono.empty());
        var probeWebClient = WebClientUtils.createWebClient(
                probeBuilder,
                containerBaseUrl, exchangeTimeout, connectTimeout, responseTimeout, writeTimeout,
                0);
        this.ngcChallengeProbeStub = WebClientUtils.createStubService(
                probeWebClient, NgcChallengeProbeStub.class);
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
     * Validates a container image by checking its existence and accessibility in the NGC
     * registry. With a cached token this is a single authorized manifest check; on a cache miss
     * the token is acquired through challenge discovery first. Validation never passes without
     * exercising the credential: NGC requires one for every pull.
     *
     * @param containerImageUrl The container image URL to validate
     * @param base64ApiKey      The base64 encoded API key in format "username:password" for authentication
     * @throws BadRequestException if the image is invalid or inaccessible
     */
    public void validateContainerImage(String containerImageUrl, String base64ApiKey) {
        requireCredential(base64ApiKey);
        ContainerImageComponents components = parseContainerImageUrl(containerImageUrl);
        RegistryAuthKey authKey =
                new RegistryAuthKey(components.repository(), components.imageName(), base64ApiKey);
        var authResponse = registryAuthCache.get(
                authKey, key -> fetchTokenViaChallenge(components, base64ApiKey));
        validateImageManifest(components, authResponse.getToken());
    }

    /**
     * Verifies a credential by exchanging it for a token at the realm the registry's challenge
     * advertises. No image reference is involved, so the challenge is triggered with the
     * registry base endpoint (OCI distribution spec {@code end-1}). Never cached: the point of
     * the call is to reach the registry.
     *
     * @throws BadRequestException   if the credential is blank; no request is made
     * @throws UpstreamException     if the registry does not challenge or its challenge is
     *                               unusable, leaving the credential unverified
     * @throws UnauthorizedException if the registry rejects the credential or issues no token
     */
    public String validateCredential(String registryHost, String base64EncodedSecret) {
        requireCredential(base64EncodedSecret);

        var baseEndpointUrl = URI.create(containerBaseUrl + BASE_ENDPOINT_PATH);
        var response = ngcChallengeProbeStub.probeBaseEndpoint(baseEndpointUrl);
        return fetchTokenFromProbe(baseEndpointUrl, response, base64EncodedSecret).getToken();
    }

    private NgcContainerRegistryStub.NgcRegistryAuthResponse fetchTokenFromChallenge(
            WwwAuthenticateChallenge challenge, String base64EncodedSecret) {
        var tokenUrl = buildTokenUrl(challenge);
        var response = ngcContainerRegistryStub.fetchToken(tokenUrl,
                                                           "Basic " + base64EncodedSecret);
        if (response == null || StringUtils.isBlank(response.getToken())) {
            var mesg = MESG_NO_TOKEN_ISSUED.formatted(challenge.realm());
            log.error(mesg);
            throw new UnauthorizedException(mesg);
        }
        return response;
    }

    /**
     * Builds the token URL from the challenge, replaying exactly the non-empty parameters it
     * advertised. {@code build(true)} keeps a pre-encoded realm query intact, so the appended
     * parameters are encoded here; without it the realm would be double-encoded and the
     * parameters not at all.
     */
    private static URI buildTokenUrl(WwwAuthenticateChallenge challenge) {
        // A realm that cannot form a URL is the registry speaking broken protocol - the same
        // family as not challenging at all - so it maps to UpstreamException rather than
        // escaping as the raw IllegalArgumentException (bad characters or encoding) or
        // IllegalStateException ({braces} parse as URI-template variables and fail expansion).
        try {
            var builder = UriComponentsBuilder.fromUriString(challenge.realm());
            if (challenge.service() != null) {
                builder.queryParam(SERVICE_PARAM,
                                   UriUtils.encodeQueryParam(challenge.service(),
                                                             StandardCharsets.UTF_8));
            }
            if (challenge.scope() != null) {
                builder.queryParam(SCOPE_PARAM,
                                   UriUtils.encodeQueryParam(challenge.scope(),
                                                             StandardCharsets.UTF_8));
            }

            return builder.build(true).toUri();
        } catch (IllegalArgumentException | IllegalStateException e) {
            var mesg = MESG_INVALID_REALM.formatted(challenge.realm());
            log.error(mesg);
            throw new UpstreamException(mesg);
        }
    }

    private static WwwAuthenticateChallenge parseChallenge(ResponseEntity<Void> response) {
        return AuthenticateChallengeUtils.parse(
                response.getHeaders().get(HttpHeaders.WWW_AUTHENTICATE));
    }

    private static void requireCredential(String base64EncodedSecret) {
        if (StringUtils.isBlank(base64EncodedSecret)) {
            log.error(MESG_REGISTRY_CREDENTIALS_MISSING);
            throw new BadRequestException(MESG_REGISTRY_CREDENTIALS_MISSING);
        }
    }

    /**
     * Loads a token for the repository by probing this very manifest for its challenge and
     * redeeming it at the advertised realm, so the realm and scope always come from the
     * registry's own answer for the right repository.
     */
    private NgcContainerRegistryStub.NgcRegistryAuthResponse fetchTokenViaChallenge(
            ContainerImageComponents components, String base64ApiKey) {
        var manifestUrl = manifestUri(components);
        var response = ngcChallengeProbeStub.probeManifest(manifestUrl, IMAGE_MEDIA_TYPES);
        return fetchTokenFromProbe(manifestUrl, response, base64ApiKey);
    }

    /**
     * Redeems a discovery probe's challenge for a token. NGC requires a credential for every
     * request, so a probe answer other than the 401 challenge (the probe client already
     * converts other 4xx/5xx to exceptions) leaves the credential unverified and raises
     * {@link UpstreamException} - validation never passes without exercising the credential.
     */
    private NgcContainerRegistryStub.NgcRegistryAuthResponse fetchTokenFromProbe(
            URI probedUrl, ResponseEntity<Void> probeResponse, String base64EncodedSecret) {
        if (!probeResponse.getStatusCode().isSameCodeAs(UNAUTHORIZED)) {
            var mesg = MESG_NO_CHALLENGE.formatted(probedUrl, probeResponse.getStatusCode());
            log.error(mesg);
            throw new UpstreamException(mesg);
        }
        return fetchTokenFromChallenge(parseChallenge(probeResponse), base64EncodedSecret);
    }

    private void validateImageManifest(ContainerImageComponents components, String bearerToken) {
        ngcContainerRegistryStub.validateManifest(
                manifestUri(components), "Bearer " + bearerToken, IMAGE_MEDIA_TYPES);
    }

    private URI manifestUri(ContainerImageComponents components) {
        String reference = components.digest() != null ? components.digest() : components.tag();
        String imagePath = components.repository() + "/" + components.imageName();
        return URI.create(containerBaseUrl + "/v2/" + imagePath + "/manifests/" + reference);
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
