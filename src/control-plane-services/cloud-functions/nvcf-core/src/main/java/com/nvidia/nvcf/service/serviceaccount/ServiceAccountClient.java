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
package com.nvidia.nvcf.service.serviceaccount;

import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvcf.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientServiceAccountProperties;
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

// Separate from ApiKeysClient: that name/shape is specific to the API-key path (introspects
// a raw key); this is called with an already-resolved ncaId, so it's just a keyed lookup.
@Service
@RefreshScope
@Slf4j
public class ServiceAccountClient {

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

    private static final String CLIENT_REGISTRATION_ID = "service-account";

    private final WebClient webClient;
    private final JsonMapper jsonMapper;
    private final String evaluationUri;
    private final String requestPropertyName;

    // This evaluation is expected to be served by the same gateway as apikey.allow, so every
    // property here defaults to that client's config; override the
    // nvcf.service-account.*/spring.security.oauth2.client.registration.service-account.*
    // properties if that stops being true.
    public ServiceAccountClient(
            @Value("${nvcf.service-account.base-url:${nvcf.api-keys.base-url}}") String baseUrl,
            @Value("${nvcf.service-account.evaluation-uri:/v1/namespaces/nvcf/evaluations/ssa.allow}")
            String evaluationUri,
            // matches the input.<name>_key naming this evaluation expects (placeholder
            // pending the real data source registration)
            @Value("${nvcf.service-account.request-property-name:tiered_rate_key}")
            String requestPropertyName,
            @Value("${spring.security.oauth2.client.registration.service-account.client-id:"
                    + "${spring.security.oauth2.client.registration.api-keys.client-id}}")
            String clientId,
            @Value("${spring.security.oauth2.client.registration.service-account.client-secret:"
                    + "${spring.security.oauth2.client.registration.api-keys.client-secret}}")
            String clientSecret,
            @Value("${spring.security.oauth2.client.registration.service-account.scope:"
                    + "${spring.security.oauth2.client.registration.api-keys.scope}}")
            String scope,
            @Value("${spring.security.oauth2.client.provider.service-account.token-uri:"
                    + "${spring.security.oauth2.client.provider.api-keys.token-uri}}")
            String tokenUri,
            Optional<StaticClientServiceAccountProperties> staticClientServiceAccountProperties,
            WebClient.Builder webClientBuilder,
            JsonMapper jsonMapper) {
        this.evaluationUri = evaluationUri;
        this.requestPropertyName = requestPropertyName;
        this.jsonMapper = jsonMapper;
        var authFilter = oauthFilter(staticClientServiceAccountProperties, webClientBuilder,
                                     clientId, clientSecret, scope, tokenUri);
        this.webClient = webClientBuilder
                .baseUrl(baseUrl)
                .filter(authFilter)
                .build();
    }

    private static ExchangeFilterFunction oauthFilter(
            Optional<StaticClientServiceAccountProperties> staticClientServiceAccountProperties,
            WebClient.Builder webClientBuilder,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri) {
        return staticClientServiceAccountProperties
                .map(p -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvcfOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    // Unlike apikey.allow, an empty/absent result means no tier configured, not forbidden.
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
                    log.error("4xx error from NAK: {}", response.statusCode());
                    return response.createException();
                })
                .onStatus(HttpStatusCode::is5xxServerError, response -> {
                    log.error("Error response code from NAK: {}", response.statusCode());
                    return Mono.error(new UpstreamException("NAK returned 5xx error"));
                })
                .bodyToMono(ApiKeyValidationResponse.class)
                .retryWhen(RETRY_SPEC)
                .switchIfEmpty(Mono.error(() -> new UpstreamException("No response from NAK")))
                .map(response -> jsonMapper.convertValue(response.getResult(),
                                                          ApiKeyValidationResult.RateLimitAttributes.class))
                .block();
    }
}
