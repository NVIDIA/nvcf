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

package com.nvidia.boot.registries.service.registry.client.acr;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ACR_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;

import com.nvidia.boot.mock.azure.MockAcrAuthServer;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciArtifactComponents;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

class AzureRegistryAuthClientTest {

    private static final String TEST_ACR_AUTH_BASE_URL = "https://testregistry.azurecr.io";

    private static AzureRegistryAuthClient azureRegistryAuthClient;

    @BeforeAll
    static void beforeAll() {
        azureRegistryAuthClient = new AzureRegistryAuthClient(WebClientUtils.builder(),
                                                              MOCK_AZURE_REGISTRY_AUTH_URL,
                                                              MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT);

        MockAcrAuthServer.start(MOCK_AZURE_REGISTRY_AUTH_URL);
    }

    @AfterAll
    static void afterAll() {
        MockAcrAuthServer.stop();
    }

    @Test
    void constructor_WithValidHostname_Success() {
        assertDoesNotThrow(() -> new AzureRegistryAuthClient(WebClientUtils.builder(),
                                                             TEST_ACR_AUTH_BASE_URL,
                                                             MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT));
    }

    @Test
    void getAuthBaseUrl_ReturnsCorrectValue() {
        var authClient = new AzureRegistryAuthClient(WebClientUtils.builder(),
                                                     TEST_ACR_AUTH_BASE_URL,
                                                     MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT);
        assertEquals(TEST_ACR_AUTH_BASE_URL, authClient.getAuthBaseUrl());
    }

    @Test
    void getCanonicalAuthTokenUrl_ReturnsCorrectUrl() {
        var authClient = new AzureRegistryAuthClient(WebClientUtils.builder(),
                                                     "https://testregistry-1.azurecr.io",
                                                     MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl =
                authClient.getCanonicalAuthTokenUrl("testregistry.azurecr.io", "myrepo/myimage");

        assertEquals(
                "https://testregistry.azurecr.io/oauth2/token?service=testregistry.azurecr.io&scope=repository:myrepo/myimage:pull",
                tokenUrl);
    }

    @Test
    void getCanonicalAuthTokenUrl_WithEmptyName_ReturnsUrlWithoutScope() {
        var authClient = new AzureRegistryAuthClient(WebClientUtils.builder(),
                                                     "https://testregistry-1.azurecr.io",
                                                     MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl("testregistry.azurecr.io", "");

        assertEquals(
                "https://testregistry.azurecr.io/oauth2/token?service=testregistry.azurecr.io",
                tokenUrl);
    }

    @Test
    void getCanonicalAuthTokenUrl_WithNullName_ReturnsUrlWithoutScope() {
        var authClient = new AzureRegistryAuthClient(WebClientUtils.builder(),
                                                     "https://testregistry-1.azurecr.io",
                                                     MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl("testregistry.azurecr.io", null);

        assertEquals(
                "https://testregistry.azurecr.io/oauth2/token?service=testregistry.azurecr.io",
                tokenUrl);
    }

    // ===== INTEGRATION TESTS WITH MOCK SERVER =====

    @Test
    void fetchToken_WithValidInputs_Success() {
        var components = new OciArtifactComponents(
                "testregistry.azurecr.io",
                "myrepo/myimage",
                "1.0.0"
        );

        var result = azureRegistryAuthClient
                .getToken(components,
                          "dGVzdC11c2VyOnRlc3QtcGFzcw=="); // base64 for "test-user:test-pass"

        assertNotNull(result);
        assertNotNull(result.token());
        assertNotNull(result.expiresIn());
    }

    @Test
    void fetchToken_DifferentRepositories() {
        var credentials = MOCK_ACR_CREDENTIALS;

        var components1 = new OciArtifactComponents(
                "localhost-acr", "repo1/image", "v1.0.0");
        var components2 = new OciArtifactComponents(
                "localhost-acr", "repo2/image", "v2.0.0");

        assertDoesNotThrow(() -> {
            var token1 = azureRegistryAuthClient.getToken(components1, credentials);
            var token2 = azureRegistryAuthClient.getToken(components2, credentials);

            assertNotNull(token1);
            assertNotNull(token2);
            assertNotNull(token1.token());
            assertNotNull(token2.token());
        });
    }

    @Test
    void validateCredential_WithMockServer_Success() {
        // Test validateCredential using the mock server
        var credentials = MOCK_ACR_CREDENTIALS;

        var token = azureRegistryAuthClient.validateCredential("localhost-acr", credentials);
        assertNotNull(token);
        assertNotNull(token.token());
        assertNotNull(token.expiresIn());
    }
}
