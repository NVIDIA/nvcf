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
package com.nvidia.icms.outbound.apikeys;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.outbound.exception.ApiKeysException;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationRequest;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationResponse;
import com.nvidia.icms.outbound.apikeys.model.ApiKeyValidationResult;
import com.nvidia.icms.util.OAuth2ClientUtils;
import com.nvidia.icms.util.OAuth2ClientUtils.ManagedHttpResources;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Component;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientResponseException;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

@Slf4j
@Component
@RefreshScope
public class ApiKeysRequestHandler {

    private static final String CLIENT_REGISTRATION_ID = "api-keys";

    private final ApiKeysStubService apiKeysStubService;
    private final ObjectMapper objectMapper;
    private final String namespace;

    // Package-private constructor for unit testing with a mock service
    ApiKeysRequestHandler(
            ApiKeysStubService apiKeysStubService,
            ObjectMapper objectMapper,
            String namespace) {
        this.apiKeysStubService = apiKeysStubService;
        this.objectMapper = objectMapper;
        this.namespace = namespace;
    }

    @org.springframework.beans.factory.annotation.Autowired
    public ApiKeysRequestHandler(
            @Value("${icms.api-keys.url}") String apiKeysBaseUrl,
            @Value("${icms.api-keys.namespace}") String namespace,
            @Value("${spring.security.oauth2.client.registration.api-keys.client-id}") String clientId,
            @Value("${spring.security.oauth2.client.registration.api-keys.client-secret}") String clientSecret,
            @Value("${spring.security.oauth2.client.registration.api-keys.scope}") String scope,
            @Value("${spring.security.oauth2.client.provider.api-keys.token-uri}") String tokenUri,
            ObjectMapper objectMapper,
            @Qualifier("apiKeysHttpResources") ManagedHttpResources httpResources,
            WebClient.Builder webClientBuilder) { // Prototype-scoped - Safe to mutate.

        this.objectMapper = objectMapper;
        this.namespace = namespace;

        var webClient = webClientBuilder
                .baseUrl(apiKeysBaseUrl)
                .clientConnector(httpResources.connector())
                .filter(OAuth2ClientUtils.getOauth2ExchangeFilter(
                        CLIENT_REGISTRATION_ID, tokenUri, clientId, clientSecret, scope))
                .filter(OAuth2ClientUtils.getRetryableFilter(CLIENT_REGISTRATION_ID))
                .filter(OAuth2ClientUtils.getResponseFilterProcessor(CLIENT_REGISTRATION_ID))
                .build();

        var adapter = WebClientAdapter.create(webClient);
        var factory = HttpServiceProxyFactory.builderFor(adapter).build();
        this.apiKeysStubService = factory.createClient(ApiKeysStubService.class);
    }

    public ApiKeyValidationResult getUserAccessDetails(String policyName, ApiKeyValidationRequest request) {
        try {
            ApiKeyValidationResponse
                    response = apiKeysStubService.evaluatePolicy(namespace, policyName, request);
            return parseResponse(response);

        } catch (WebClientResponseException e) {
            log.error("Failed to fetch details, HTTP {}", e.getStatusCode(), e);
            throw new ApiKeysException("ApiKeys service error: " + e.getStatusCode(), e);

        } catch (ApiKeysException e) {
            throw e;
        } catch (Exception e) {
            log.error("Unknown error while getUserAccessDetails, exception - ", e);
            throw new ApiKeysException("Unknown error from ApiKeys service.", e);
        }
    }

    private ApiKeyValidationResult parseResponse(ApiKeyValidationResponse response) {
        if (response == null || response.getResult() == null) {
            throw new ApiKeysException("Unexpected response from ApiKeys service. No body returned.");
        }
        try {
            return objectMapper.convertValue(response.getResult(), ApiKeyValidationResult.class);
        } catch (Exception exception) {
            throw new ApiKeysException("Failed to parse user details.", exception);
        }
    }
}
