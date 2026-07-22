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

import static com.nvidia.boot.registries.service.registry.client.ngc.NgcRegistryUtils.convertArtifactFileUrlToSizeUrl;
import static com.nvidia.boot.registries.service.registry.client.ngc.NgcRegistryUtils.removeArtifactHostName;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Expiry;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.google.common.collect.Streams;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.registries.service.registry.auth.AuthServiceStub;
import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import com.nvidia.boot.registries.service.registry.dto.ArtifactFile;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import jakarta.validation.constraints.PositiveOrZero;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Base64;
import java.util.List;
import java.util.Optional;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.NotImplementedException;
import org.springframework.util.LinkedMultiValueMap;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

@Slf4j
public class NgcArtifactRegistryClient {

    private static final String MESG_UNKNOWN_ARTIFACT_URL_TYPE =
            "Unknown Artifact URL type '%s'";
    private static final String MESG_NULL_RESPONSE =
            "Null response from Artifact Registry for artifact path '%s'";
    private static final String MESG_INVALID_OAUTH_SERVER_RESPONSE =
            "Invalid response from Artifact Registry's OAuth Server";
    private static final String MESG_INVALID_NGC_HELM_CHART_URL_FORMAT_RESPONSE =
            "Invalid NGC helm chart url format";
    private static final String MESG_NULL_TOKEN_RESPONSE =
            "Null response from getting token from registry %s";
    private static final int PAGE_SIZE = 100;
    private static final long OAUTH_TTL_IN_SECONDS = 900;  // 15 minutes
    // The Regex is inspired from NGC helm service implementation
    // https://gitlab-master.nvidia.com/ngc/cloud/helmchart-registry/-/blob/main/internal/proxy/helm_registry_proxy.go?ref_type=heads#L75
    private static final String NGC_HELM_CHART_VERSION_URL_REGEX =
            "^(?:/api)?/(?<namespace>[^/]+(?:/[^/]+)?)/charts/(?<chartName>[a-zA-Z0-9-_]+)-(?<version>v?\\d+(\\.\\d+){0,2}([-+][\\w.+-]+)?)\\.tgz(?:\\.prov)?$";
    private static final Pattern HELM_CHART_VERSION_URL_REGEX_PATTERN =
            Pattern.compile(NGC_HELM_CHART_VERSION_URL_REGEX);

    private final LoadingCache<String, AuthServiceStub.Oauth2Token> authCache =
            Caffeine.newBuilder()
                    .expireAfter(new Oauth2TokenExpiry())
                    .scheduler(Scheduler.systemScheduler())
                    .build(this::fetchAuthToken);

    private final NgcArtifactRegistryStub ngcArtifactRegistryStub;
    private final AuthServiceStub authServiceStub;
    private final String groupScope;
    private final String artifactBaseUrl;
    private String hostname;

    public NgcArtifactRegistryClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            String hostname,
            Duration exchangeTimeout,
            Duration responseTimeout,
            Duration writeTimeout,
            Duration connectTimeout,
            String oauth2BaseUrl,
            String oauth2GroupScope) {
        var baseUrl = hostname.startsWith("http") ? hostname : "https://" + hostname;
        this.hostname = URI.create(baseUrl).getHost();
        // Design for 3rd Party Registry requires hostnames to be unique. However, when
        // integration tests involving multiple registries are being executed, this
        // becomes an issue as all the registries use "localhost" as the hostname in the
        // baseUrl. To make the hostnames unique in the application-test.yaml files of
        // apps such as NVCF API and NVCT API, we use localhost-<registry-key>:<port>
        // as the baseUrl. For example, localhost-ngc:<port>, localhost-docker:<port>,
        // etc. When using the baseUrl, we remove the `-<registry-key>` part so that
        // the client can communicate with the registry-specific mock server.
        this.artifactBaseUrl = baseUrl.replace("-ngc", "");

        var authWebClient = WebClientUtils.createWebClient(
                webClientBuilder.clone(),
                oauth2BaseUrl, Duration.ofSeconds(5));
        this.authServiceStub = WebClientUtils.createStubService(
                authWebClient, AuthServiceStub.class);

        log.info("NgcArtifactRegistryClient init for hostname {} with exchangeTimeout: {},"
                         + " connectTimeout: {}, responseTimeout: {}, writeTimeout: {}",
                 hostname, exchangeTimeout, connectTimeout, responseTimeout, writeTimeout);
        var artifactWebClient = WebClientUtils.createWebClient(
                webClientBuilder.clone(),
                artifactBaseUrl, exchangeTimeout, connectTimeout, responseTimeout, writeTimeout, 3);
        this.ngcArtifactRegistryStub = WebClientUtils.createStubService(
                artifactWebClient, NgcArtifactRegistryStub.class);
        this.groupScope = oauth2GroupScope;
    }

    public String getHostname() {
        return this.hostname;
    }

    @VisibleForTesting
    public void setHostname(String hostname) {
        this.hostname = hostname;
    }

    public void resetAuthTokenCache() {
        authCache.invalidateAll();
    }

    public long fetchModelSize(String artifactUrl, String apiKey) {
        var artifactSizeUrl = convertArtifactFileUrlToSizeUrl(artifactUrl);
        var artifactPath = removeArtifactHostName(artifactSizeUrl);
        if (artifactPath == null) {
            return 0L;
        }
        var authorization = "Bearer " + authCache.get(apiKey).getAccessToken();
        var response = ngcArtifactRegistryStub.getModelMetadata(
                URI.create(artifactBaseUrl + artifactPath), authorization);
        if (response.getModelVersion() == null) {
            var mesg = MESG_NULL_RESPONSE.formatted(artifactPath);
            throw new UpstreamException(mesg);
        }
        return response.getModelVersion().getTotalSizeInBytes();
    }

    public long fetchResourceSize(String artifactUrl, String apiKey) {
        var artifactSizeUrl = convertArtifactFileUrlToSizeUrl(artifactUrl);
        var artifactPath = removeArtifactHostName(artifactSizeUrl);
        if (artifactPath == null) {
            return 0L;
        }
        var authorization = "Bearer " + authCache.get(apiKey).getAccessToken();
        var response = ngcArtifactRegistryStub.getResourceMetadata(
                URI.create(artifactBaseUrl + artifactPath), authorization);
        if (response == null || response.getRecipeVersion() == null) {
            var mesg = MESG_NULL_RESPONSE.formatted(artifactPath);
            throw new UpstreamException(mesg);
        }
        return response.getRecipeVersion().getTotalSizeInBytes();
    }

    public List<ArtifactFile> getPreSignedArtifactURLs(String artifactUrl, String apiKey) {
        if (artifactUrl.endsWith("/files")) {
            return getPreSignedArtifactFilesURLs(artifactUrl, apiKey);
        }
        var mesg = MESG_UNKNOWN_ARTIFACT_URL_TYPE.formatted(artifactUrl);
        log.error(mesg);
        throw new NotImplementedException(mesg);
    }

    public void validateArtifact(String artifactUrl, String apiKey, ArtifactTypeEnum artifactType) {
        NgcArtifactUriValidator.validate(artifactUrl, artifactType);
        var artifactSizeUrl = convertArtifactFileUrlToSizeUrl(artifactUrl);
        var artifactPath = removeArtifactHostName(artifactSizeUrl);
        if (artifactPath == null) {
            throw new BadRequestException("Artifact path is empty");
        }
        var authorization = "Bearer " + authCache.get(apiKey).getAccessToken();
        ngcArtifactRegistryStub.validateArtifact(
                URI.create(artifactBaseUrl + artifactPath), authorization);
    }

    // NGC store helm chart as artifact in their backend, so we translate the helm chart url to
    // artifact url and leverage same way to validate it.
    public void validateHelmChart(String helmChartUrl, String apiKey) {
        var helmChartPath = removeArtifactHostName(helmChartUrl);
        if (helmChartPath == null) {
            throw new BadRequestException("Helm Chart path is empty");
        }
        var artifactPath = translateHelmPathToArtifactPath(helmChartPath);
        var authorization = "Bearer " + authCache.get(apiKey).getAccessToken();
        ngcArtifactRegistryStub.validateArtifact(
                URI.create(artifactBaseUrl + artifactPath), authorization);
    }

    public String validateCredential(String registryHost, String apiKey) {
        var oauth2Token = fetchAuthToken(apiKey);
        return Optional.ofNullable(oauth2Token)
                .map(AuthServiceStub.Oauth2Token::getAccessToken)
                .orElseThrow(() -> {
                    var mesg = MESG_NULL_TOKEN_RESPONSE.formatted(registryHost);
                    log.error(mesg);
                    return new ForbiddenException(mesg);
                });
    }

    @VisibleForTesting
    public static String translateHelmPathToArtifactPath(String helmChartPath) {
        Matcher matcher = HELM_CHART_VERSION_URL_REGEX_PATTERN.matcher(helmChartPath);
        if (!matcher.matches()) {
            throw new BadRequestException(MESG_INVALID_NGC_HELM_CHART_URL_FORMAT_RESPONSE);
        }
        String namespace = matcher.group("namespace");
        String chartName = matcher.group("chartName");
        String version = matcher.group("version");

        String[] helmPathSegments = namespace.split("/");
        String helmPath = helmPathSegments.length > 1 ?
                "org/%s/team/%s".formatted(helmPathSegments[0], helmPathSegments[1]) :
                "org/%s".formatted(helmPathSegments[0]);

        // We expect the path will be in the format
        // /v2/org/0539907589386975/team/mega-dev/helm-charts/mega-simulation-app/versions/0.6.0+mr.626af78b/files
        return "/v2/%s/helm-charts/%s/versions/%s"
                .formatted(helmPath, chartName, version);
    }

    private List<ArtifactFile> getPreSignedArtifactFilesURLs(String artifactUrl, String apiKey) {
        var artifactPath = removeArtifactHostName(artifactUrl);
        var authorization = "Bearer " + authCache.get(apiKey).getAccessToken();

        return ngcArtifactRegistryStub.getArtifactFiles(
                        buildArtifactFilesUri(artifactPath, null, PAGE_SIZE), authorization)
                .switchIfEmpty(Mono.error(() ->
                                                  new UpstreamException(
                                                          MESG_NULL_RESPONSE.formatted(
                                                                  artifactPath))))
                .flatMap(firstPage -> {
                    var allPages = Flux.just(firstPage);
                    if (firstPage.getPaginationInfo() != null
                            && firstPage.getPaginationInfo().getTotalPages() > 1) {
                        // flatMapSequential mirrors the original RestClientUtils behavior:
                        // all page requests are fired concurrently, but results are emitted
                        // in page order (same as firing all CompletableFutures then joining in order).
                        var remainingPages = Flux.range(1,
                                                        firstPage.getPaginationInfo()
                                                                .getTotalPages() - 1)
                                .flatMapSequential(i -> ngcArtifactRegistryStub.getArtifactFiles(
                                        buildArtifactFilesUri(artifactPath, i, PAGE_SIZE),
                                        authorization));
                        allPages = allPages.concatWith(remainingPages);
                    }
                    return allPages
                            .flatMapIterable(page -> Streams.zip(
                                    page.getFilepath().stream(),
                                    page.getUrls().stream(),
                                    ArtifactFile::new).toList())
                            .collectList();
                })
                .block();
    }

    private URI buildArtifactFilesUri(String path, Integer pageNumber, int pageSize) {
        var sb = new StringBuilder(artifactBaseUrl).append(path)
                .append("?page-size=").append(pageSize);
        if (pageNumber != null) {
            sb.append("&page-number=").append(pageNumber);
        }
        return URI.create(sb.toString());
    }

    private AuthServiceStub.Oauth2Token fetchAuthToken(String key) {
        if (key.startsWith("nvapi-")) {
            // Special handling for account credentials that can be SAKs till all the legacy AuthN
            // ApiKeys are migrated to SAKs.
            var oauth2Token = new AuthServiceStub.Oauth2Token();
            oauth2Token.setAccessToken(key);
            oauth2Token.setExpiresIn(OAUTH_TTL_IN_SECONDS);
            return oauth2Token;
        }

        var formData = new LinkedMultiValueMap<String, String>();
        formData.add("grant_type", "client_credentials");
        formData.add("scope", "ngc:group/" + groupScope);
        var basicAuth = "Basic " + Base64.getEncoder().encodeToString(
                ("$oauthtoken:" + key).getBytes(StandardCharsets.UTF_8));
        AuthServiceStub.Oauth2Token body = authServiceStub.fetchToken(basicAuth, formData);
        if (body == null || body.getAccessToken() == null
                || body.getExpiresIn() == null) {
            log.error(MESG_INVALID_OAUTH_SERVER_RESPONSE);
            throw new UpstreamException(MESG_INVALID_OAUTH_SERVER_RESPONSE);
        }
        return body;
    }

    private static class Oauth2TokenExpiry implements Expiry<String, AuthServiceStub.Oauth2Token> {

        @Override
        public long expireAfterCreate(String key, AuthServiceStub.Oauth2Token value,
                                      long currentTime) {
            return Duration.ofSeconds(value.getExpiresIn()).toNanos() * 3 / 4;
        }

        @Override
        public long expireAfterUpdate(
                String key, AuthServiceStub.Oauth2Token value, long currentTime,
                @PositiveOrZero long currentDuration) {
            return currentDuration;
        }

        @Override
        public long expireAfterRead(
                String key, AuthServiceStub.Oauth2Token value, long currentTime,
                @PositiveOrZero long currentDuration) {
            return currentDuration;
        }
    }
}
