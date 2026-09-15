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
package com.nvidia.nvcf.service.apikeys;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvcf.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientApiKeysProperties;
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

@Service
@RefreshScope
@Slf4j
public class ApiKeysClient {

    private static final String CLIENT_REGISTRATION_ID = "api-keys";

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

    private final WebClient webClient;
    private final JsonMapper jsonMapper;
    private final String evaluationUri;
    private final String requestPropertyName;

    // We could have used OAuth2AuthorizedClientManager and relied on Spring Security to
    // pick up the configuration properties using ClientRegistrationRepository directly. However,
    // the client-secret value held in the ClientRegistrationRepository does not get refreshed when
    // client-secret is rotated. Addressing these issues requires introducing a refreshable
    // ClientRegistrationRepository that wasn't clean. Instead, we will keep it simple and use
    // the tried and tested approach of using @Value and @RefreshScope annotations and wire
    // things up ourselves.
    public ApiKeysClient(
            @Value("${nvcf.api-keys.base-url}") String baseUrl,
            @Value("${nvcf.api-keys.evaluation-uri:/v1/namespaces/nvcf/evaluations/apikey.allow}")
            String evaluationUri,
            @Value("${nvcf.api-keys.request-property-name:apiKey}")
            String requestPropertyName,
            @Value("${spring.security.oauth2.client.registration.api-keys.client-id}")
            String clientId,
            @Value("${spring.security.oauth2.client.registration.api-keys.client-secret}")
            String clientSecret,
            @Value("${spring.security.oauth2.client.registration.api-keys.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.api-keys.token-uri}") String tokenUri,
            Optional<StaticClientApiKeysProperties> staticClientApiKeysProperties,
            WebClient.Builder webClientBuilder,
            JsonMapper jsonMapper) {
        this.evaluationUri = evaluationUri;
        this.requestPropertyName = requestPropertyName;
        this.jsonMapper = jsonMapper;
        var authFilter = oauthFilter(staticClientApiKeysProperties, webClientBuilder,
                                     clientId, clientSecret, scope, tokenUri);
        this.webClient = webClientBuilder
                .baseUrl(baseUrl)
                .filter(authFilter)
                .build();
    }

    private static ExchangeFilterFunction oauthFilter(
            Optional<StaticClientApiKeysProperties> staticClientApiKeysProperties,
            WebClient.Builder webClientBuilder,
            String clientId,
            String clientSecret,
            String scope,
            String tokenUri) {
        return staticClientApiKeysProperties
                .map(p -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(p::getToken))
                .orElseGet(() -> NvcfOAuth2ClientUtils
                        .getOAuth2ExchangeFilter(webClientBuilder, CLIENT_REGISTRATION_ID,
                                                 tokenUri, clientId, clientSecret, scope));
    }

    public ApiKeyValidationResult fetchApiKeyValidationResult(String apiKey) {
        return webClient
                .post()
                .uri(evaluationUri)
                .accept(MediaType.APPLICATION_JSON)
                .bodyValue(ApiKeyValidationRequest.builder()
                                   .jsonField(requestPropertyName, apiKey)
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
                .switchIfEmpty(
                        Mono.error(() -> new UpstreamException("No response from NAK")))
                .map(apiKeysResponse -> jsonMapper.convertValue(apiKeysResponse.getResult(),
                                                                ApiKeyValidationResult.class))
                .filter(ApiKeyValidationResult::valid)
                .switchIfEmpty(Mono.error(() -> new ForbiddenException("Authorization failed")))
                .block();
    }

}
