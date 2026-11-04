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
package com.nvidia.nvct.service.apikeys;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.nvct.configuration.staticclientauth.FixedBearerExchangeFilterFunction;
import com.nvidia.nvct.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientApiKeysProperties;
import com.nvidia.nvct.service.apikeys.dto.ApiKeyValidationRequest;
import com.nvidia.nvct.service.apikeys.dto.ApiKeyValidationResponse;
import java.time.Duration;
import java.util.Arrays;
import java.util.List;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.MediaType;
import org.springframework.security.oauth2.client.AuthorizedClientServiceReactiveOAuth2AuthorizedClientManager;
import org.springframework.security.oauth2.client.InMemoryReactiveOAuth2AuthorizedClientService;
import org.springframework.security.oauth2.client.registration.ClientRegistration;
import org.springframework.security.oauth2.client.registration.InMemoryReactiveClientRegistrationRepository;
import org.springframework.security.oauth2.client.web.reactive.function.client.ServerOAuth2AuthorizedClientExchangeFilterFunction;
import org.springframework.security.oauth2.core.AuthorizationGrantType;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.ExchangeFilterFunction;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;
import reactor.util.retry.Retry;
import reactor.util.retry.RetryBackoffSpec;

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
            @Value("${nvct.api-keys.base-url}") String baseUrl,
            @Value("${nvct.api-keys.evaluation-uri:/v1/namespaces/nvct/evaluations/apikey.allow}")
            String evaluationUri,
            @Value("${nvct.api-keys.request-property-name:apiKey}")
            String requestPropertyName,
            @Value("${spring.security.oauth2.client.registration.api-keys.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.api-keys.client-secret}")
            String clientSecret,
            @Value("${spring.security.oauth2.client.registration.api-keys.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.api-keys.token-uri}") String tokenUri,
            Optional<StaticClientApiKeysProperties> staticClientApiKeysProperties,
            WebClient.Builder webClientBuilder,   // Prototype-scoped - Safe to mutate.
            JsonMapper jsonMapper) {
        this.evaluationUri = evaluationUri;
        this.requestPropertyName = requestPropertyName;
        this.jsonMapper = jsonMapper;
        var authFilter = staticClientApiKeysProperties.map(properties -> (ExchangeFilterFunction)
                        new FixedBearerExchangeFilterFunction(properties::getToken))
                .orElseGet(() -> oauthFilter(clientId, clientSecret, scope, tokenUri));
        this.webClient = webClientBuilder
                .baseUrl(baseUrl)
                .filter(authFilter)
                .build();
    }

    private static ServerOAuth2AuthorizedClientExchangeFilterFunction oauthFilter(
            String clientId, String clientSecret, String scope, String tokenUri) {
        var scopes = StringUtils.isBlank(scope) ? List.<String>of() :
                Arrays.stream(scope.split(",")).map(String::trim).toList();
        var clientRegistration = ClientRegistration.withRegistrationId(CLIENT_REGISTRATION_ID)
                .authorizationGrantType(AuthorizationGrantType.CLIENT_CREDENTIALS)
                .clientId(clientId)
                .clientSecret(clientSecret)
                .scope(scopes)
                .tokenUri(tokenUri)
                .build();
        var clientRegistrationRepository =
                new InMemoryReactiveClientRegistrationRepository(clientRegistration);
        var authorizedClientService =
                new InMemoryReactiveOAuth2AuthorizedClientService(clientRegistrationRepository);
        var authorizedClientManager =
                new AuthorizedClientServiceReactiveOAuth2AuthorizedClientManager(
                        clientRegistrationRepository, authorizedClientService);
        var filter =
                new ServerOAuth2AuthorizedClientExchangeFilterFunction(authorizedClientManager);
        filter.setDefaultClientRegistrationId(CLIENT_REGISTRATION_ID);
        return filter;
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
                    log.error("4xx error from ApiKeys Svc: {}", response.statusCode());
                    return response.createException();
                })
                .onStatus(HttpStatusCode::is5xxServerError, response -> {
                    log.error("Error response code from ApiKeys Svc: {}", response.statusCode());
                    return Mono.error(new UpstreamException("ApiKeys Service returned 5xx error"));
                })
                .bodyToMono(ApiKeyValidationResponse.class)
                .retryWhen(RETRY_SPEC)
                .switchIfEmpty(Mono.error(() -> new UpstreamException("No response from ApiKeys Svc")))
                .map(apiKeysResponse -> jsonMapper.convertValue(apiKeysResponse.getResult(),
                                                                ApiKeyValidationResult.class))
                .filter(ApiKeyValidationResult::valid)
                .switchIfEmpty(Mono.error(() -> new ForbiddenException("Authorization failed")))
                .block();
    }
}
