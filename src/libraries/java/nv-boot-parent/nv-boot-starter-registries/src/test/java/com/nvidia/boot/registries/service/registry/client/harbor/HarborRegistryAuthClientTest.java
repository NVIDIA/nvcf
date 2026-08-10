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

package com.nvidia.boot.registries.service.registry.client.harbor;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;

import com.nvidia.boot.mock.harbor.MockHarborAuthServer;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciArtifactComponents;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

class HarborRegistryAuthClientTest {

    private static final String TEST_HARBOR_AUTH_BASE_URL = "https://demo.goharbor.io";

    private static HarborRegistryAuthClient harborRegistryAuthClient;

    @BeforeAll
    static void beforeAll() {
        harborRegistryAuthClient = new HarborRegistryAuthClient(WebClientUtils.builder(),
                                                                MOCK_HARBOR_REGISTRY_AUTH_URL,
                                                                MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT);

        MockHarborAuthServer.start(MOCK_HARBOR_REGISTRY_AUTH_URL);
    }

    @AfterAll
    static void afterAll() {
        MockHarborAuthServer.stop();
    }

    @Test
    void constructor_WithValidHostname_Success() {
        assertDoesNotThrow(() -> new HarborRegistryAuthClient(WebClientUtils.builder(),
                                                              MOCK_HARBOR_REGISTRY_AUTH_URL,
                                                              MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT));
    }

    @Test
    void getAuthBaseUrl_ReturnsCorrectValue() {
        var authClient = new HarborRegistryAuthClient(WebClientUtils.builder(),
                                                      TEST_HARBOR_AUTH_BASE_URL,
                                                      MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT);
        assertEquals(TEST_HARBOR_AUTH_BASE_URL, authClient.getAuthBaseUrl());
    }

    @Test
    void getCanonicalAuthTokenUrl_ReturnsCorrectUrl() {
        var authClient = new HarborRegistryAuthClient(WebClientUtils.builder(),
                                                      "https://demo.goharbor.io",
                                                      MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl("demo.goharbor.io", "myrepo/myimage");
        assertEquals(
                "https://demo.goharbor.io/service/token?service=harbor-registry&scope=repository:myrepo/myimage:pull",
                tokenUrl);
    }

    @Test
    void getCanonicalAuthTokenUrl_WithEmptyName_ReturnsUrlWithoutScope() {
        var authClient = new HarborRegistryAuthClient(WebClientUtils.builder(),
                                                      "https://demo.goharbor.io",
                                                      MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl("demo.goharbor.io", "");
        assertEquals(
                "https://demo.goharbor.io/service/token?service=harbor-registry",
                tokenUrl);
    }

    @Test
    void getCanonicalAuthTokenUrl_WithNullName_ReturnsUrlWithoutScope() {
        var authClient = new HarborRegistryAuthClient(WebClientUtils.builder(),
                                                      "https://demo.goharbor.io",
                                                      MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl("demo.goharbor.io", null);
        assertEquals(
                "https://demo.goharbor.io/service/token?service=harbor-registry",
                tokenUrl);
    }

    // ===== INTEGRATION TESTS WITH MOCK SERVER =====

    @Test
    void fetchToken_WithValidInputs_Success() {
        var components = new OciArtifactComponents(
                "localhost-harbor", "test/image", "latest");
        var result = harborRegistryAuthClient.getToken(components, MOCK_HARBOR_CREDENTIALS);

        assertNotNull(result);
        assertNotNull(result.token());
        assertNotNull(result.expiresIn());
    }

    @Test
    void fetchToken_DifferentRepositories() {
        var component1 = new OciArtifactComponents(
                "localhost-harbor", "repo1/image", "v1.0.0");
        var component2 = new OciArtifactComponents(
                "localhost-harbor", "repo2/image", "v2.0.0");

        assertDoesNotThrow(() -> {
            var token1 = harborRegistryAuthClient.getToken(component1, MOCK_HARBOR_CREDENTIALS);
            var token2 = harborRegistryAuthClient.getToken(component2, MOCK_HARBOR_CREDENTIALS);

            assertNotNull(token1);
            assertNotNull(token2);
            assertNotNull(token1.token());
            assertNotNull(token2.token());
        });
    }

    @Test
    void validateCredential_WithMockServer_Success() {
        // Test validateCredential using the mock server
        var credentials = MOCK_HARBOR_CREDENTIALS;

        var token = harborRegistryAuthClient.validateCredential("localhost-harbor", credentials);
        assertNotNull(token);
        assertNotNull(token.token());
        assertNotNull(token.expiresIn());
    }
}
