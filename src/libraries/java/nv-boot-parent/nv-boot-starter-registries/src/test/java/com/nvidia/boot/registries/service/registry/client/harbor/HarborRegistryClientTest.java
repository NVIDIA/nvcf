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
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_REGISTRY;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.normalizeUrl;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_REGISTRY_URL;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.harbor.MockHarborAuthServer;
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

class HarborRegistryClientTest {

    private static HarborRegistryClient harborRegistryClient;
    private static MockOciRegistryServer mockHarborRegistryServer;
    private static final String TEST_HARBOR_REGISTRY_BASE_URL =
            "https://" + TEST_HARBOR_REGISTRY;

    @BeforeAll
    static void beforeAll() {
        mockHarborRegistryServer = new MockOciRegistryServer();
        MockHarborAuthServer.start(MOCK_HARBOR_REGISTRY_AUTH_URL);
        mockHarborRegistryServer.start(MOCK_HARBOR_REGISTRY_URL);
        harborRegistryClient = new HarborRegistryClient(WebClientUtils.builder(),
                                                        MOCK_HARBOR_REGISTRY_URL,
                                                        MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT,
                                                        MOCK_HARBOR_REGISTRY_AUTH_URL);
    }

    @AfterAll
    static void afterAll() {
        MockHarborAuthServer.stop();
        mockHarborRegistryServer.stop();
    }

    @Test
    void constructor_WithValidHostname_Success() {
        assertDoesNotThrow(() -> new HarborRegistryClient(WebClientUtils.builder(),
                                                          TEST_HARBOR_REGISTRY,
                                                          MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT,
                                                          TEST_HARBOR_REGISTRY_BASE_URL));
    }

    @Test
    void constructor_WithHttpsHostname_Success() {
        assertDoesNotThrow(() -> new HarborRegistryClient(WebClientUtils.builder(),
                                                          TEST_HARBOR_REGISTRY_BASE_URL,
                                                          MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT,
                                                          TEST_HARBOR_REGISTRY_BASE_URL));
    }

    static Stream<Arguments> validHarborHostnames() {
        return Stream.of(
                Arguments.of("https://harbor.demo.com", "harbor.demo.com"),
                Arguments.of("https://harbor.demo.com:443", "harbor.demo.com"),
                Arguments.of("http://localhost:8080", "localhost"));
    }

    @ParameterizedTest
    @MethodSource("validHarborHostnames")
    void constructor_ExtractsHostnameCorrectly(String inputHostname, String expectedHostname) {
        var client = new HarborRegistryClient(WebClientUtils.builder(),
                                              inputHostname,
                                              MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT,
                                              normalizeUrl(inputHostname));
        assertEquals(expectedHostname, client.getHostname());
    }

    @Test
    void getHostname_ReturnsCorrectHostname() {
        var client = new HarborRegistryClient(WebClientUtils.builder(),
                                              TEST_HARBOR_REGISTRY,
                                              MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT,
                                              TEST_HARBOR_REGISTRY_BASE_URL);
        assertEquals("demo.goharbor.io", client.getHostname());
    }

    @Test
    void getHostname_WithHttpsUrl_ExtractsHostnameOnly() {
        var client = new HarborRegistryClient(WebClientUtils.builder(),
                                              TEST_HARBOR_REGISTRY_BASE_URL,
                                              MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT,
                                              TEST_HARBOR_REGISTRY_BASE_URL);
        assertEquals("demo.goharbor.io", client.getHostname());
    }

    @Test
    void getRegistryBaseUrl_ReturnsCorrectUrl() {
        var client = new HarborRegistryClient(WebClientUtils.builder(),
                                              TEST_HARBOR_REGISTRY,
                                              MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT,
                                              TEST_HARBOR_REGISTRY_BASE_URL);

        String baseUrl = client.getRegistryBaseUrl("demo.goharbor.io");
        assertEquals("https://demo.goharbor.io", baseUrl);
    }

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
                harborRegistryClient.validateArtifact(invalidUrl, MOCK_HARBOR_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithNullImageUrl_ThrowsBadRequestException() {
        assertThrows(BadRequestException.class, () ->
                harborRegistryClient.validateArtifact(null, MOCK_HARBOR_CREDENTIALS));
    }

    static Stream<Arguments> validHarborImageUrls() {
        return Stream.of(
                Arguments.of(TEST_HARBOR_CONTAINER_IMAGE_WITH_TAG.toString()),
                Arguments.of(TEST_HARBOR_CONTAINER_IMAGE_WITH_DIGEST.toString()),
                Arguments.of(TEST_HARBOR_HELM_CHART_WITH_TAG.toString()),
                Arguments.of(TEST_HARBOR_HELM_CHART_WITH_DIGEST.toString()));
    }

    @ParameterizedTest
    @MethodSource("validHarborImageUrls")
    void validateArtifact_WithParameterizedValidUrls_Success(String imageUrl) {
        assertDoesNotThrow(() -> harborRegistryClient.validateArtifact(imageUrl,
                                                                       MOCK_HARBOR_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithPermissionDeniedImage_ThrowsForbiddenException() {
        assertThrows(ForbiddenException.class, () ->
                harborRegistryClient.validateArtifact(
                        TEST_HARBOR_CONTAINER_IMAGE_PERMISSION_DENIED.toString(),
                        MOCK_HARBOR_CREDENTIALS));
    }

    @Test
    void validateArtifact_WithNonExistentImage_ThrowsNotFoundException() {
        assertThrows(NotFoundException.class, () ->
                harborRegistryClient.validateArtifact(
                        TEST_HARBOR_CONTAINER_IMAGE_NOT_EXISTS.toString(),
                        MOCK_HARBOR_CREDENTIALS));
    }
}
