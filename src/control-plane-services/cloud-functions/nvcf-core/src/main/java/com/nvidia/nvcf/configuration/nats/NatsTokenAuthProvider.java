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
package com.nvidia.nvcf.configuration.nats;

import static com.nvidia.nvcf.service.nats.AuthCalloutService.API_USER_ACCOUNT;

import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.service.nats.AuthCalloutService.AuthCalloutPluginRequest;
import java.time.Duration;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.authentication.AnonymousAuthenticationToken;
import org.springframework.security.core.authority.AuthorityUtils;
import org.springframework.security.oauth2.client.AuthorizedClientServiceOAuth2AuthorizedClientManager;
import org.springframework.security.oauth2.client.ClientCredentialsOAuth2AuthorizedClientProvider;
import org.springframework.security.oauth2.client.InMemoryOAuth2AuthorizedClientService;
import org.springframework.security.oauth2.client.OAuth2AuthorizeRequest;
import org.springframework.security.oauth2.client.registration.ClientRegistration;
import org.springframework.security.oauth2.client.registration.InMemoryClientRegistrationRepository;
import org.springframework.security.oauth2.core.AuthorizationGrantType;
import tools.jackson.databind.json.JsonMapper;

// @RefreshScope causes this bean to be destroyed and recreated whenever Spring Cloud Config
// triggers a refresh event. The AuthorizedClientServiceOAuth2AuthorizedClientManager is
// re-instantiated with fresh values from NatsProperties on each refresh, picking up any
// rotated credentials or changed token URIs without requiring a full application restart.
@Configuration
@ConditionalOnProperty(value = "nvcf.nats.oauth2-properties.enabled", havingValue = "true")
@RefreshScope
public class NatsTokenAuthProvider {

    private static final String CLIENT_REGISTRATION_ID = "nats-auth";

    // Mirrors the 10-second wiggle room from the previous JwtProvider implementation:
    // re-fetch the token this many seconds before its reported expiry to avoid using
    // a token that expires in flight.
    private static final Duration CLOCK_SKEW = Duration.ofSeconds(10);

    private final JsonMapper jsonMapper;
    private final AuthorizedClientServiceOAuth2AuthorizedClientManager authorizedClientManager;
    private final String authCalloutPluginName;

    public NatsTokenAuthProvider(NatsProperties natsProperties, JsonMapper jsonMapper) {
        this.jsonMapper = jsonMapper;
        this.authCalloutPluginName = natsProperties.getAuthCalloutPluginName();
        var oauth2Properties = natsProperties.getOauth2Properties();
        if (oauth2Properties == null) {
            throw new IllegalArgumentException("OAuth2 properties are required");
        }
        if (StringUtils.isBlank(oauth2Properties.baseUrl())
                || StringUtils.isBlank(oauth2Properties.clientId())
                || StringUtils.isBlank(oauth2Properties.secretId())) {
            throw new IllegalArgumentException("OAuth2 properties are required");
        }

        if (StringUtils.isBlank(authCalloutPluginName)) {
            throw new IllegalArgumentException("authCalloutPluginName cannot be blank");
        }

        var clientRegistration = ClientRegistration
                .withRegistrationId(CLIENT_REGISTRATION_ID)
                .authorizationGrantType(AuthorizationGrantType.CLIENT_CREDENTIALS)
                .clientId(oauth2Properties.clientId())
                .clientSecret(oauth2Properties.secretId())
                .scope("admin:nats:" + API_USER_ACCOUNT)
                .tokenUri(oauth2Properties.baseUrl() + "/token")
                .build();

        var registrationRepository = new InMemoryClientRegistrationRepository(clientRegistration);
        var clientService = new InMemoryOAuth2AuthorizedClientService(registrationRepository);

        var clientCredentialsProvider = new ClientCredentialsOAuth2AuthorizedClientProvider();
        clientCredentialsProvider.setClockSkew(CLOCK_SKEW);

        this.authorizedClientManager = new AuthorizedClientServiceOAuth2AuthorizedClientManager(
                registrationRepository, clientService);
        this.authorizedClientManager.setAuthorizedClientProvider(clientCredentialsProvider);
    }

    public char[] getToken() {
        // OAuth2AuthorizeRequest requires a non-null Principal. In a server-to-server
        // client_credentials flow there is no authenticated user or active HTTP request,
        // so we supply a static AnonymousAuthenticationToken as the principal. This is
        // the standard pattern for headless/background OAuth2 client usage outside of a
        // servlet request context.
        var principal = new AnonymousAuthenticationToken(
                CLIENT_REGISTRATION_ID, CLIENT_REGISTRATION_ID,
                AuthorityUtils.createAuthorityList("ROLE_ANONYMOUS"));

        var authorizeRequest = OAuth2AuthorizeRequest
                .withClientRegistrationId(CLIENT_REGISTRATION_ID)
                .principal(principal)
                .build();

        var authorizedClient = authorizedClientManager.authorize(authorizeRequest);
        if (authorizedClient == null || authorizedClient.getAccessToken() == null) {
            throw new IllegalStateException("Failed to obtain OAuth2 access token for NATS");
        }

        return new AuthCalloutPluginRequest(API_USER_ACCOUNT, authCalloutPluginName,
                                            authorizedClient.getAccessToken().getTokenValue())
                .toToken(jsonMapper);
    }
}
