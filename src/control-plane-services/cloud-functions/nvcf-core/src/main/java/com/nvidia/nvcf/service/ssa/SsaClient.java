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
package com.nvidia.nvcf.service.ssa;

import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvcf.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientSsaProperties;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult;
import com.nvidia.nvcf.service.apikeys.dto.ApiKeyValidationRequest;
import com.nvidia.nvcf.service.apikeys.dto.ApiKeyValidationResponse;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import java.time.Duration;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.MediaType;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;
import reactor.util.retry.Retry;
import reactor.util.retry.RetryBackoffSpec;
import tools.jackson.databind.json.JsonMapper;

/**
 * Calls UAM's SSA-JWT policy evaluation to resolve the per-account tiered token rate limit,
 * by ncaId. Kept separate from {@link com.nvidia.nvcf.service.apikeys.ApiKeysClient}: that
 * client's name and shape are SAK/apikey specific (it introspects a raw key), while this one
 * is called with an ncaId the caller already resolved from an already-authenticated SSA JWT -
 * there is no identity to introspect here, just a keyed PIP lookup.
 */
@Service
@RefreshScope
@Slf4j
public class SsaClient {

    private static final RetryBackoffSpec RETRY_SPEC = Retry.backoff(2, Duration.ofMillis(200))
            .jitter(0.75)
            .doBeforeRetry(retrySignal -> log.info("before retrying call"))
            .doAfterRetry(retrySignal -> log.info("after retrying call"))
            // retry only on 500 upstream
            .filter(UpstreamException.class::isInstance)
            .onRetryExhaustedThrow((retryBackoffSpec, retrySignal) -> {
                log.error("External Service failed to process after max retries");
                return new UpstreamException(
                        "Failed to get response from external system after retries.");
            });

    private static final String CLIENT_REGISTRATION_ID = "ssa";

    private final WebClient webClient;
    private final JsonMapper jsonMapper;
    private final String evaluationUri;
    private final String requestPropertyName;

    public SsaClient(
            @Value("${nvcf.ssa.base-url}") String baseUrl,
            @Value("${nvcf.ssa.evaluation-uri:/v1/namespaces/nvcf/evaluations/ssa.allow}")
            String evaluationUri,
            // matches the input.<name>_key naming the ssa.allow policy expects - see the
            // comment in nvcf-uam-policies' policy/ssa/ssa.rego (placeholder pending the
            // real PIP registration)
            @Value("${nvcf.ssa.request-property-name:tiered_rate_key}")
            String requestPropertyName,
            @Value("${spring.security.oauth2.client.registration.ssa.client-id}")
            String clientId,
            @Value("${spring.security.oauth2.client.registration.ssa.client-secret}")
            String clientSecret,
            @Value("${spring.security.oauth2.client.registration.ssa.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.ssa.token-uri}") String tokenUri,
            Optional<StaticClientSsaProperties> staticClientSsaProperties,
            WebClient.Builder webClientBuilder,
            JsonMapper jsonMapper) {
        this.evaluationUri = evaluationUri;
        this.requestPropertyName = requestPropertyName;
        this.jsonMapper = jsonMapper;
        var authFilter = oauthFilter(staticClientSsaProperties, webClientBuilder,
                                     clientId, clientSecret, scope, tokenUri);
        this.webClient = webClientBuilder
                .baseUrl(baseUrl)
                .filter(authFilter)
                .build();
    }

    private static ExchangeFilterFunction oauthFilter(
            Optional<StaticClientSsaProperties> staticClientSsaProperties,
            WebClient.Builder webClientBuilder,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri) {
        return staticClientSsaProperties
                .map(p -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvcfOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    /**
     * Resolves the tiered token rate limit for an ncaId already established by SSA-JWT
     * authentication. Unlike {@code apikey.allow}, this is a data lookup, not an
     * authorization gate: an empty/absent result just means no tier is configured for that
     * account, not that the caller is forbidden.
     */
    public ApiKeyValidationResult.RateLimitAttributes fetchTieredRateLimit(String ncaId) {
        return webClient
                .post()
                .uri(evaluationUri)
                .accept(MediaType.APPLICATION_JSON)
                .bodyValue(ApiKeyValidationRequest.builder()
                                   .jsonField(requestPropertyName, ncaId)
                                   .build())
                .retrieve()
                .onStatus(HttpStatusCode::is4xxClientError, response -> {
                    log.error("4xx error from UAM: {}", response.statusCode());
                    return response.createException();
                })
                .onStatus(HttpStatusCode::is5xxServerError, response -> {
                    log.error("Error response code from UAM: {}", response.statusCode());
                    return Mono.error(new UpstreamException("UAM returned 5xx error"));
                })
                .bodyToMono(ApiKeyValidationResponse.class)
                .retryWhen(RETRY_SPEC)
                .switchIfEmpty(Mono.error(() -> new UpstreamException("No response from UAM")))
                .map(response -> jsonMapper.convertValue(response.getResult(),
                                                          ApiKeyValidationResult.RateLimitAttributes.class))
                .block();
    }
}
