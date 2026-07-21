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

package com.nvidia.boot.registries.service.registry.client.artifactory;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.mock.artifactory.MockArtifactoryAuthServer;
import com.nvidia.boot.registries.service.registry.client.oci.dto.OciArtifactComponents;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;

class ArtifactoryAuthClientTest {

    private static final String TEST_ARTIFACTORY_AUTH_BASE_URL =
            "https://artifactoryRegistryTest.jfrog.io";

    private static ArtifactoryAuthClient artifactoryAuthClient;

    @BeforeAll
    static void beforeAll() {
        artifactoryAuthClient = new ArtifactoryAuthClient(
                WebClientUtils.builder(),
                MOCK_ARTIFACTORY_REGISTRY_AUTH_URL,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT);

        MockArtifactoryAuthServer.start(MOCK_ARTIFACTORY_REGISTRY_AUTH_URL);
    }

    @AfterAll
    static void afterAll() {
        MockArtifactoryAuthServer.stop();
    }

    @Test
    void constructor_WithValidHostname_Success() {
        assertDoesNotThrow(() -> new ArtifactoryAuthClient(
                WebClientUtils.builder(),
                TEST_ARTIFACTORY_AUTH_BASE_URL,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT));
    }

    @Test
    void getAuthBaseUrl_ReturnsCorrectValue() {
        var authClient = new ArtifactoryAuthClient(
                WebClientUtils.builder(),
                TEST_ARTIFACTORY_AUTH_BASE_URL,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT);
        assertEquals(TEST_ARTIFACTORY_AUTH_BASE_URL, authClient.getAuthBaseUrl());
    }

    @Test
    void getCanonicalAuthTokenUrl_ReturnsCorrectUrl() {
        var authClient = new ArtifactoryAuthClient(
                WebClientUtils.builder(),
                "https://testregistry-1.jfrog.io",
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl(
                "testregistry.jfrog.io",
                "nvcf-test-oci/test-image-1");

        assertEquals(
                "https://testregistry.jfrog.io/v2/token?service=testregistry.jfrog.io&scope=repository:test-image-1:pull",
                tokenUrl);
    }

    @Test
    void getCanonicalAuthTokenUrl_WithComplexImageName_ReturnsCorrectUrl() {
        var authClient = new ArtifactoryAuthClient(
                WebClientUtils.builder(),
                "https://testregistry.jfrog.io",
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl(
                "testregistry.jfrog.io",
                "myrepo/path/to/image");

        assertEquals(
                "https://testregistry.jfrog.io/v2/token?service=testregistry.jfrog.io&scope=repository:path/to/image:pull",
                tokenUrl);
    }

    @Test
    void getCanonicalAuthTokenUrl_WithInvalidName_ThrowsBadRequestException() {
        var authClient = new ArtifactoryAuthClient(
                WebClientUtils.builder(),
                "https://testregistry.jfrog.io",
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT);

        assertThrows(BadRequestException.class, () ->
                authClient.getCanonicalAuthTokenUrl(
                        "testregistry.jfrog.io",
                        "myrepo"));
    }

    @Test
    void getCanonicalAuthTokenUrl_WithEmptyName_ReturnsUrlWithoutScope() {
        var authClient = new ArtifactoryAuthClient(
                WebClientUtils.builder(),
                "https://testregistry.jfrog.io",
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl(
                "testregistry.jfrog.io",
                "");

        assertEquals(
                "https://testregistry.jfrog.io/v2/token?service=testregistry.jfrog.io",
                tokenUrl);
    }

    @Test
    void getCanonicalAuthTokenUrl_WithNullName_ReturnsUrlWithoutScope() {
        var authClient = new ArtifactoryAuthClient(
                WebClientUtils.builder(),
                "https://testregistry.jfrog.io",
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT);

        var tokenUrl = authClient.getCanonicalAuthTokenUrl(
                "testregistry.jfrog.io",
                null);

        assertEquals(
                "https://testregistry.jfrog.io/v2/token?service=testregistry.jfrog.io",
                tokenUrl);
    }

    // ===== INTEGRATION TESTS WITH MOCK SERVER =====

    @Test
    void fetchToken_WithValidInputs_Success() {
        var components = new OciArtifactComponents(
                "testregistry.jfrog.io",
                "nvcf-test-oci/myimage",
                "1.0.0"
        );

        var result = artifactoryAuthClient
                .getToken(components,
                          "dGVzdC11c2VyOnRlc3QtcGFzcw=="); // base64 for "test-user:test-pass"

        assertNotNull(result);
        assertNotNull(result.token());
        assertNotNull(result.expiresIn());
    }

    @Test
    void fetchToken_DifferentRepositories() {
        var credentials = MOCK_ARTIFACTORY_CREDENTIALS;

        var components1 = new OciArtifactComponents(
                "localhost-jfrog", "repo1/image", "v1.0.0");
        var components2 = new OciArtifactComponents(
                "localhost-jfrog", "repo2/image", "v2.0.0");

        assertDoesNotThrow(() -> {
            var token1 = artifactoryAuthClient.getToken(components1, credentials);
            var token2 = artifactoryAuthClient.getToken(components2, credentials);

            assertNotNull(token1);
            assertNotNull(token2);
            assertNotNull(token1.token());
            assertNotNull(token2.token());
        });
    }

    @Test
    void validateCredential_WithMockServer_Success() {
        // Test validateCredential using the mock server
        var credentials = MOCK_ARTIFACTORY_CREDENTIALS;

        var token = artifactoryAuthClient.validateCredential("localhost-jfrog", credentials);
        assertNotNull(token);
        assertNotNull(token.token());
        assertNotNull(token.expiresIn());
    }
}
