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
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.normalizeUrl;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ACR_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.azure.MockAcrAuthServer;
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

class AzureRegistryClientTest {

    private static final String TEST_ACR_HOSTNAME = "testregistry.azurecr.io";
    private static final String TEST_ACR_REGISTRY_BASE_URL = "https://testregistry.azurecr.io";

    private static AzureRegistryClient azureRegistryClient;
    private static MockOciRegistryServer mockAcrRegistryServer;

    @BeforeAll
    static void beforeAll() {
        azureRegistryClient = new AzureRegistryClient(
                WebClientUtils.builder(),
                MOCK_AZURE_REGISTRY_URL,
                MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_AZURE_REGISTRY_AUTH_URL);

        MockAcrAuthServer.start(MOCK_AZURE_REGISTRY_AUTH_URL);
        mockAcrRegistryServer = new MockOciRegistryServer();
        mockAcrRegistryServer.start(MOCK_AZURE_REGISTRY_URL);
    }

    @AfterAll
    static void afterAll() {
        MockAcrAuthServer.stop();
        mockAcrRegistryServer.stop();
    }

    @Test
    void constructor_WithValidHostname_Success() {
        assertDoesNotThrow(() -> new AzureRegistryClient(WebClientUtils.builder(),
                                                         TEST_ACR_HOSTNAME,
                                                         MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT,
                                                         TEST_ACR_REGISTRY_BASE_URL));
    }

    @Test
    void constructor_WithHttpsHostname_Success() {
        assertDoesNotThrow(() -> new AzureRegistryClient(WebClientUtils.builder(),
                                                         TEST_ACR_REGISTRY_BASE_URL,
                                                         MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT,
                                                         TEST_ACR_REGISTRY_BASE_URL));
    }

    static Stream<Arguments> validAcrHostnames() {
        return Stream.of(
                Arguments.of("myregistry.azurecr.io", "myregistry.azurecr.io"),
                Arguments.of("https://myregistry.azurecr.io", "myregistry.azurecr.io"),
                Arguments.of("http://localhost:8080", "localhost"),
                Arguments.of("https://custom-registry.example.com", "custom-registry.example.com"),
                Arguments.of("https://registry.io:443", "registry.io"),
                Arguments.of("http://localhost-acr:9200", "localhost-acr")
                        );
    }

    @ParameterizedTest
    @MethodSource("validAcrHostnames")
    void constructor_ExtractsHostnameCorrectly(String inputHostname, String expectedHostname) {
        var client =
                new AzureRegistryClient(WebClientUtils.builder(), inputHostname, MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT,
                                        normalizeUrl(inputHostname));
        assertEquals(expectedHostname, client.getHostname());
    }

    @Test
    void getHostname_ReturnsCorrectHostname() {
        var client =
                new AzureRegistryClient(WebClientUtils.builder(), TEST_ACR_HOSTNAME, MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT,
                                        TEST_ACR_REGISTRY_BASE_URL);
        assertEquals("testregistry.azurecr.io", client.getHostname());
    }

    @Test
    void getHostname_WithHttpsUrl_ExtractsHostnameOnly() {
        var client = new AzureRegistryClient(WebClientUtils.builder(), TEST_ACR_REGISTRY_BASE_URL,
                                             MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT,
                                             TEST_ACR_REGISTRY_BASE_URL);
        assertEquals("testregistry.azurecr.io", client.getHostname());
    }

    @Test
    void getRegistryBaseUrl_ReturnsCorrectUrl() {
        var client =
                new AzureRegistryClient(WebClientUtils.builder(),
                                        TEST_ACR_HOSTNAME,
                                        MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT,
                                        TEST_ACR_REGISTRY_BASE_URL);
        String baseUrl = client.getRegistryBaseUrl("testregistry-2.azurecr.io");
        assertEquals("https://testregistry-2.azurecr.io", baseUrl);
    }

    @Test
    void azureRegistryGlobalHostname_HasCorrectValue() {
        assertEquals("azurecr.io", AzureRegistryClient.AZURE_REGISTRY_GLOBAL_HOSTNAME);
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
                azureRegistryClient.validateArtifact(invalidUrl, MOCK_ACR_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithNullImageUrl_ThrowsBadRequestException() {
        assertThrows(BadRequestException.class, () ->
                azureRegistryClient.validateArtifact(null, MOCK_ACR_CREDENTIALS));
    }

    static Stream<Arguments> validAcrImageUrls() {
        return Stream.of(
                Arguments.of(TEST_ACR_CONTAINER_IMAGE_WITH_TAG.toString()),
                Arguments.of(TEST_ACR_CONTAINER_IMAGE_WITH_DIGEST.toString()),
                Arguments.of(TEST_ACR_HELM_CHART_WITH_TAG.toString()),
                Arguments.of(TEST_ACR_HELM_CHART_WITH_DIGEST.toString())
                        );
    }

    @ParameterizedTest
    @MethodSource("validAcrImageUrls")
    void validateArtifact_WithValidUrls_Success(String imageUrl) {
        assertDoesNotThrow(
                () -> azureRegistryClient.validateArtifact(imageUrl, MOCK_ACR_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithPermissionDeniedImage_ThrowsForbiddenException() {
        var imageUrl = TEST_ACR_CONTAINER_IMAGE_PERMISSION_DENIED.toString();
        assertThrows(ForbiddenException.class, () ->
                azureRegistryClient.validateArtifact(
                        imageUrl,
                        MOCK_ACR_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithNonExistentImage_ThrowsNotFoundException() {
        var imageUrl = TEST_ACR_CONTAINER_IMAGE_NOT_EXISTS.toString();
        assertThrows(NotFoundException.class, () ->
                azureRegistryClient.validateArtifact(
                        imageUrl,
                        MOCK_ACR_CREDENTIALS));
    }
}
