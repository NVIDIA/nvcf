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

package com.nvidia.boot.telemetry.client;

import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.Map;
import java.util.concurrent.atomic.AtomicReference;
import org.apache.commons.lang3.StringUtils;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.reactive.function.client.ClientRequest;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.ExchangeFunction;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;

/**
 * ExchangeFilterFunction that adds OAuth2 Bearer token to requests.
 * Obtains tokens via client credentials from TelemetryProperties.
 */
public final class OAuth2BearerFilter implements ExchangeFilterFunction {

    private final TelemetryProperties.OAuth2Properties oauth2;
    private final OAuth2AuthMethod authMethod;
    private final WebClient tokenWebClient;
    private final AtomicReference<TokenHolder> cachedToken = new AtomicReference<>();

    public OAuth2BearerFilter(TelemetryProperties properties, WebClient tokenWebClient) {
        this.oauth2 = properties.getOauth2();
        this.authMethod = oauth2.getAuthMethod() != null
                ? oauth2.getAuthMethod()
                : OAuth2AuthMethod.CLIENT_SECRET_POST;
        this.tokenWebClient = tokenWebClient;
    }

    @Override
    public Mono<ClientResponse> filter(ClientRequest request, ExchangeFunction next) {
        return Mono.fromCallable(this::getAccessToken)
                .flatMap(token -> next.exchange(ClientRequest.from(request)
                        .header(HttpHeaders.AUTHORIZATION, "Bearer " + token)
                        .build()));
    }

    private String getAccessToken() {
        var holder = cachedToken.get();
        if (holder != null && !holder.isExpired()) {
            return holder.getToken();
        }

        var requestSpec = tokenWebClient.post()
                .uri(oauth2.getTokenUri())
                .header(HttpHeaders.CONTENT_TYPE, MediaType.APPLICATION_FORM_URLENCODED_VALUE);

        if (authMethod == OAuth2AuthMethod.CLIENT_SECRET_BASIC) {
            var credentials = oauth2.getClientId() + ":" + oauth2.getClientSecret();
            var encoded = Base64.getEncoder().encodeToString(
                    credentials.getBytes(StandardCharsets.UTF_8));
            requestSpec = requestSpec.header(HttpHeaders.AUTHORIZATION, "Basic " + encoded);
        }

        var tokenResponse = requestSpec
                .bodyValue(buildTokenRequestBody())
                .retrieve()
                .bodyToMono(new ParameterizedTypeReference<Map<String, Object>>() {})
                .blockOptional()
                .orElseThrow(() -> new IllegalStateException(
                        "Failed to obtain OAuth2 access token for telemetry"));

        var token = (String) tokenResponse.get("access_token");
        if (token == null) {
            throw new IllegalStateException("OAuth2 token response missing access_token");
        }
        var expiresIn = (Number) tokenResponse.get("expires_in");
        var expiresInSeconds = expiresIn != null ? expiresIn.longValue() : 3600L;
        cachedToken.set(new TokenHolder(token, expiresInSeconds));
        return token;
    }

    private String buildTokenRequestBody() {
        var params = new StringBuilder();
        params.append("grant_type=client_credentials");
        if (authMethod == OAuth2AuthMethod.CLIENT_SECRET_POST) {
            params.append("&client_id=").append(oauth2.getClientId());
            params.append("&client_secret=").append(oauth2.getClientSecret());
        }
        if (StringUtils.isNotBlank(oauth2.getScope())) {
            params.append("&scope=").append(oauth2.getScope());
        }
        return params.toString();
    }

    private static final class TokenHolder {
        private final String token;
        private final long expiresAtSeconds;

        TokenHolder(String token, long expiresInSeconds) {
            this.token = token;
            var now = System.currentTimeMillis() / 1000;
            var buffer = Math.max(0, expiresInSeconds - 60);
            this.expiresAtSeconds = now + buffer;
        }

        String getToken() {
            return token;
        }

        boolean isExpired() {
            return System.currentTimeMillis() / 1000 >= expiresAtSeconds;
        }
    }
}
