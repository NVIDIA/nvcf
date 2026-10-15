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

import static com.nvidia.boot.registries.service.registry.client.oci.OciRegistryClient.getOciRegistryWebClient;

import com.github.benmanes.caffeine.cache.Caffeine;
import com.github.benmanes.caffeine.cache.Expiry;
import com.github.benmanes.caffeine.cache.LoadingCache;
import com.github.benmanes.caffeine.cache.Scheduler;
import com.google.common.annotations.VisibleForTesting;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.client.oci.OciAuthStubService.OciAuthTokenResponse;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciArtifactComponents;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciAuthKey;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciAuthToken;
import java.net.URI;
import java.time.Duration;
import java.time.Instant;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.logging.log4j.util.Strings;
import org.springframework.web.reactive.function.client.WebClient;

@Slf4j
public abstract class OciRegistryAuthClient {

    // Based on prod metrics, there are around ~ 1000 request for function creation
    // and function deployment creation/update per hour.
    // Double the number to allow extreme cases.
    private static final int AUTH_TOKEN_CACHE_SIZE = 2048;

    private static final String MESG_REGISTRY_CREDENTIALS_MISSING =
            "Registry credentials are required";
    private static final String MESG_OCI_AUTH_RESPONSE_TOKEN_MISSING =
            "Oci auth response must contain either access_token or token field";

    private final LoadingCache<OciAuthKey, OciAuthToken> registryAuthCache;
    private final OciAuthStubService ociAuthStubService;

    protected OciRegistryAuthClient(
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            Duration callTimeout) {
        var timeout = callTimeout != null ? callTimeout : Duration.ofSeconds(30);
        this.ociAuthStubService = getOciRegistryWebClient(webClientBuilder,
                                                          timeout,
                                                          OciAuthStubService.class);
        this.registryAuthCache = Caffeine.newBuilder()
                .maximumSize(AUTH_TOKEN_CACHE_SIZE)
                .expireAfter(new AuthTokenExpiry())
                .scheduler(Scheduler.systemScheduler())
                .build(this::fetchToken);
    }

    protected abstract String getCanonicalAuthTokenUrl(String registryHost, String name);

    public OciAuthToken getToken(OciArtifactComponents components,
                                 String base64EncodedSecret) {
        if (StringUtils.isBlank(base64EncodedSecret)) {
            log.error(MESG_REGISTRY_CREDENTIALS_MISSING);
            throw new BadRequestException(MESG_REGISTRY_CREDENTIALS_MISSING);
        }
        var authKey = new OciAuthKey(components.registryHost(),
                                     components.name(),
                                     base64EncodedSecret);

        return registryAuthCache.get(authKey);
    }

    public OciAuthToken validateCredential(String registryHost,
                                            String base64EncodedSecret) {
        var url = getCanonicalAuthTokenUrl(registryHost, Strings.EMPTY);
        var tokenResponse = ociAuthStubService.fetchToken(
                URI.create(url),
                "Basic " + base64EncodedSecret);

        return extractAuthToken(tokenResponse);
    }

    @VisibleForTesting
    OciAuthToken fetchToken(OciAuthKey authKey) {
        var url = getCanonicalAuthTokenUrl(authKey.registryHost(), authKey.name());
        var tokenResponse = ociAuthStubService.fetchToken(
                URI.create(url),
                "Basic " + authKey.base64Secret());

        return extractAuthToken(tokenResponse);
    }

    protected OciAuthToken extractAuthToken(OciAuthTokenResponse response) {
        String tokenValue = null;
        if (StringUtils.isNotBlank(response.getAccessToken())) {
            tokenValue = response.getAccessToken();
        } else if (StringUtils.isNotBlank(response.getToken())) {
            tokenValue = response.getToken();
        }

        if (StringUtils.isBlank(tokenValue)) {
            log.error(MESG_OCI_AUTH_RESPONSE_TOKEN_MISSING);
            throw new IllegalArgumentException(MESG_OCI_AUTH_RESPONSE_TOKEN_MISSING);
        }

        Duration expiresIn;
        if (response.getExpiresIn() != null && response.getExpiresIn() > 0) {
            expiresIn = Duration.ofSeconds(response.getExpiresIn());
        } else {
            expiresIn = getExpClaimFromJwt(tokenValue)
                    .orElse(Duration.ofSeconds(60));
        }

        return new OciAuthToken(tokenValue, expiresIn);
    }

    public void invalidateCache(OciAuthKey authKey) {
        registryAuthCache.invalidate(authKey);
    }

    public void invalidateCache() {
        registryAuthCache.invalidateAll();
    }

    @VisibleForTesting
    long getCacheSize() {
        return registryAuthCache.estimatedSize();
    }

    @VisibleForTesting
    static Optional<Duration> getExpClaimFromJwt(String accessToken) {
        if (StringUtils.isEmpty(accessToken)) {
            return Optional.empty();
        }

        try {
            var signedJWT = SignedJWT.parse(accessToken);
            var claimsSet = signedJWT.getJWTClaimsSet();

            var expirationDate = claimsSet.getExpirationTime();
            if (expirationDate == null) {
                log.warn("No 'exp' claim found in JWT");
                return Optional.empty();
            }

            var expirationTime = expirationDate.toInstant();
            var currentTime = Instant.now();

            if (expirationTime.isAfter(currentTime)) {
                return Optional.of(Duration.between(currentTime, expirationTime));
            } else {
                log.warn("JWT token has already expired at {}", expirationTime);
                return Optional.of(Duration.ZERO);
            }

        } catch (Exception e) {
            log.warn("Failed to parse JWT: {}", e.getMessage());
            return Optional.empty();
        }
    }

    private static class AuthTokenExpiry implements
            Expiry<OciAuthKey, OciAuthToken> {

        @Override
        public long expireAfterCreate(OciAuthKey key,
                                      OciAuthToken value,
                                      long currentTime) {
            return value.expiresIn().toNanos() * 3 / 4;
        }

        @Override
        public long expireAfterUpdate(OciAuthKey key,
                                      OciAuthToken value,
                                      long currentTime,
                                      long currentDuration) {
            return currentDuration;
        }

        @Override
        public long expireAfterRead(OciAuthKey key,
                                    OciAuthToken value,
                                    long currentTime,
                                    long currentDuration) {
            return currentDuration;
        }
    }
}
