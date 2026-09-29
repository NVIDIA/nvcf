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
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.service.nats.AuthCalloutService.AuthCalloutPluginRequest;
import org.junit.jupiter.api.Test;
import tools.jackson.databind.json.JsonMapper;

class NatsTokenAuthProviderTest {

    private static final JsonMapper OBJECT_MAPPER = new JsonMapper();
    // The issuer URL is a compile-time String constant, so the compiler inlines it and never
    // loads IntegrationTestConfiguration. Referencing MOCK_OAUTH2_TOKEN_SERVER forces the class
    // to load, triggering the static block that starts MockOAuth2TokenServer once for the suite.
    private static final String OAUTH_BASE_URL = IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER.getBaseUrl();
    private static final String CLIENT_ID = "test-client-id";
    private static final String SECRET_ID = "test-secret-id";

    // --- constructor validation ---

    @Test
    void constructorThrowsWhenOauth2PropertiesIsNull() {
        var natsProperties = new NatsProperties();
        natsProperties.setOauth2Properties(null);

        assertThatThrownBy(() -> new NatsTokenAuthProvider(natsProperties, OBJECT_MAPPER))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessage("OAuth2 properties are required");
    }

    @Test
    void constructorThrowsWhenBaseUrlIsBlank() {
        assertThatThrownBy(
                () -> new NatsTokenAuthProvider(
                        buildNatsProperties("", CLIENT_ID, SECRET_ID), OBJECT_MAPPER))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessage("OAuth2 properties are required");
    }

    @Test
    void constructorThrowsWhenClientIdIsBlank() {
        assertThatThrownBy(
                () -> new NatsTokenAuthProvider(
                        buildNatsProperties(OAUTH_BASE_URL, "", SECRET_ID), OBJECT_MAPPER))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessage("OAuth2 properties are required");
    }

    @Test
    void constructorThrowsWhenSecretIdIsBlank() {
        assertThatThrownBy(
                () -> new NatsTokenAuthProvider(
                        buildNatsProperties(OAUTH_BASE_URL, CLIENT_ID, ""), OBJECT_MAPPER))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessage("OAuth2 properties are required");
    }

    // --- getToken ---

    @Test
    void getTokenReturnsBase64EncodedPluginRequestWithAccessToken() throws Exception {
        var provider = new NatsTokenAuthProvider(
                buildNatsProperties(OAUTH_BASE_URL, CLIENT_ID, SECRET_ID), OBJECT_MAPPER);

        var token = provider.getToken();

        var request = AuthCalloutPluginRequest.fromToken(new String(token), OBJECT_MAPPER);
        assertThat(request.account()).isEqualTo(API_USER_ACCOUNT);
        assertThat(request.pluginName()).isEqualTo("oidc");
        assertThat(request.payload()).isNotBlank();
    }

    @Test
    void getTokenCachesTokenBetweenCallsUntilExpiry() throws Exception {
        var provider = new NatsTokenAuthProvider(
                buildNatsProperties(OAUTH_BASE_URL, CLIENT_ID, SECRET_ID), OBJECT_MAPPER);

        // Both calls should return the same encoded token; the second call must hit the cache,
        // not the token endpoint again.
        var firstToken = new String(provider.getToken());
        var secondToken = new String(provider.getToken());

        assertThat(firstToken).isEqualTo(secondToken);
    }

    // --- helpers ---

    private NatsProperties buildNatsProperties(String baseUrl, String clientId, String secretId) {
        var natsProperties = new NatsProperties();
        natsProperties.setOauth2Properties(
                new NatsProperties.OAuth2Properties(true, baseUrl, clientId, secretId));
        return natsProperties;
    }
}
