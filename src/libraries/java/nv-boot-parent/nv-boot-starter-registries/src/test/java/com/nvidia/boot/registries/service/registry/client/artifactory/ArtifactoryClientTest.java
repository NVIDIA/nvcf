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
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.normalizeUrl;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_REGISTRY_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.artifactory.MockArtifactoryAuthServer;
import com.nvidia.boot.mock.oci.MockOciRegistryServer;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.web.reactive.function.client.WebClient;

class ArtifactoryClientTest {

    private static final String TEST_ARTIFACTORY_HOSTNAME = "testregistry.jfrog.io";
    private static final String TEST_ARTIFACTORY_REGISTRY_BASE_URL =
            "https://testregistry.jfrog.io";

    private static ArtifactoryClient artifactoryClient;
    private static MockOciRegistryServer mockArtifactoryRegistryServer;

    @BeforeAll
    static void beforeAll() {
        artifactoryClient = new ArtifactoryClient(
                WebClientUtils.builder(),
                MOCK_ARTIFACTORY_REGISTRY_URL,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_ARTIFACTORY_REGISTRY_AUTH_URL);

        MockArtifactoryAuthServer.start(MOCK_ARTIFACTORY_REGISTRY_AUTH_URL);
        mockArtifactoryRegistryServer = new MockOciRegistryServer();
        mockArtifactoryRegistryServer.start(MOCK_ARTIFACTORY_REGISTRY_URL);
    }

    @AfterAll
    static void afterAll() {
        MockArtifactoryAuthServer.stop();
        mockArtifactoryRegistryServer.stop();
    }

    @Test
    void constructor_WithValidHostname_Success() {
        assertDoesNotThrow(() -> new ArtifactoryClient(
                WebClientUtils.builder(),
                TEST_ARTIFACTORY_HOSTNAME,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT,
                TEST_ARTIFACTORY_REGISTRY_BASE_URL));
    }

    @Test
    void constructor_WithHttpsHostname_Success() {
        assertDoesNotThrow(() -> new ArtifactoryClient(
                WebClientUtils.builder(),
                TEST_ARTIFACTORY_REGISTRY_BASE_URL,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT,
                TEST_ARTIFACTORY_REGISTRY_BASE_URL));
    }

    static Stream<Arguments> validArtifactoryHostnames() {
        return Stream.of(
                Arguments.of("myregistry.jfrog.io", "myregistry.jfrog.io"),
                Arguments.of("https://myregistry.jfrog.io", "myregistry.jfrog.io"),
                Arguments.of("http://localhost:8080", "localhost"),
                Arguments.of("https://custom-registry.example.com", "custom-registry.example.com"),
                Arguments.of("https://registry.io:443", "registry.io"),
                Arguments.of("http://localhost-jfrog:9200", "localhost-jfrog")
        );
    }

    @ParameterizedTest
    @MethodSource("validArtifactoryHostnames")
    void constructor_ExtractsHostnameCorrectly(String inputHostname, String expectedHostname) {
        var client = new ArtifactoryClient(
                WebClientUtils.builder(),
                inputHostname,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT,
                normalizeUrl(inputHostname));
        assertEquals(expectedHostname, client.getHostname());
    }

    @Test
    void getHostname_ReturnsCorrectHostname() {
        var client = new ArtifactoryClient(
                WebClientUtils.builder(),
                TEST_ARTIFACTORY_HOSTNAME,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT,
                TEST_ARTIFACTORY_REGISTRY_BASE_URL);
        assertEquals("testregistry.jfrog.io", client.getHostname());
    }

    @Test
    void getHostname_WithHttpsUrl_ExtractsHostnameOnly() {
        var client = new ArtifactoryClient(
                WebClientUtils.builder(),
                TEST_ARTIFACTORY_REGISTRY_BASE_URL,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT,
                TEST_ARTIFACTORY_REGISTRY_BASE_URL);
        assertEquals("testregistry.jfrog.io", client.getHostname());
    }

    @Test
    void getRegistryBaseUrl_ReturnsCorrectUrl() {
        var client = new ArtifactoryClient(
                WebClientUtils.builder(),
                TEST_ARTIFACTORY_HOSTNAME,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT,
                TEST_ARTIFACTORY_REGISTRY_BASE_URL);
        String baseUrl = client.getRegistryBaseUrl("testregistry-2.jfrog.io");
        assertEquals("https://testregistry-2.jfrog.io", baseUrl);
    }

    // ===== INTEGRATION TESTS WITH MOCK SERVER =====

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty string
            "invalid",  // no slashes
            "localhost:9110",  // only registry
            "localhost:9110/",  // trailing slash
            "https://localhost:9110/namespace/repo:tag", // wrong protocol prefix
    })
    void validateArtifact_WithInvalidImageUrls_ThrowsBadRequestException(String invalidUrl) {
        assertThrows(BadRequestException.class, () ->
                artifactoryClient.validateArtifact(invalidUrl, MOCK_ARTIFACTORY_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithNullImageUrl_ThrowsBadRequestException() {
        assertThrows(BadRequestException.class, () ->
                artifactoryClient.validateArtifact(null, MOCK_ARTIFACTORY_CREDENTIALS));
    }

    static Stream<Arguments> validArtifactoryImageUrls() {
        return Stream.of(
                Arguments.of(TEST_ARTIFACTORY_CONTAINER_IMAGE_WITH_TAG.toString()),
                Arguments.of(TEST_ARTIFACTORY_CONTAINER_IMAGE_WITH_DIGEST.toString()),
                Arguments.of(TEST_ARTIFACTORY_HELM_CHART_WITH_TAG.toString()),
                Arguments.of(TEST_ARTIFACTORY_HELM_CHART_WITH_DIGEST.toString())
        );
    }

    @ParameterizedTest
    @MethodSource("validArtifactoryImageUrls")
    void validateArtifact_WithValidUrls_Success(String imageUrl) {
        assertDoesNotThrow(
                () -> artifactoryClient.validateArtifact(imageUrl,
                                                         MOCK_ARTIFACTORY_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithPermissionDeniedImage_ThrowsForbiddenException() {
        var imageUrl = TEST_ARTIFACTORY_CONTAINER_IMAGE_PERMISSION_DENIED.toString();
        assertThrows(ForbiddenException.class, () ->
                artifactoryClient.validateArtifact(
                        imageUrl,
                        MOCK_ARTIFACTORY_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithNonExistentImage_ThrowsNotFoundException() {
        var imageUrl = TEST_ARTIFACTORY_CONTAINER_IMAGE_NOT_EXISTS.toString();
        assertThrows(NotFoundException.class, () ->
                artifactoryClient.validateArtifact(
                        imageUrl,
                        MOCK_ARTIFACTORY_CREDENTIALS));
    }
}
