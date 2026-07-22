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

package com.nvidia.boot.registries.service.registry.client.ngc;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlEqualTo;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_HASH;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_ORG_NAME;
import static com.nvidia.boot.registries.service.registry.client.ngc.NgcContainerRegistryClient.parseContainerImageUrl;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_CONTAINER_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_CONTAINER_REGISTRY_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_2;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_UNKNOWN_ORG;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_IMAGE_WITH_INVALID_TAG;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.web.reactive.function.client.WebClient;

class NgcContainerRegistryClientTest {
    private static NgcContainerRegistryClient ngcContainerRegistryClient;

    @BeforeAll
    static void beforeAll() {
        ngcContainerRegistryClient = new NgcContainerRegistryClient(
                WebClientUtils.builder(),
                MOCK_NGC_CONTAINER_REGISTRY_URL,
                MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT
        );
        MockNgcContainerRegistryServer.start(MOCK_NGC_CONTAINER_REGISTRY_URL);
    }

    @AfterAll
    static void cleanup() {
        MockNgcContainerRegistryServer.stop();
    }

    @AfterEach
    void reset() {
        ngcContainerRegistryClient.resetAuthTokenCache();
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().resetRequests();
    }


    static Stream<Arguments> createValidImageUrl() {
        return Stream.of(
                Arguments.of(TEST_NGC_CONTAINER_IMAGE.toString(),
                             TEST_NGC_CONTAINER_REGISTRY, TEST_VALID_ORG_NAME,
                             TEST_VALID_CONTAINER_NAME, TEST_VALID_CONTAINER_TAG, null),
                Arguments.of(TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST.toString(),
                             TEST_NGC_CONTAINER_REGISTRY, TEST_VALID_ORG_NAME,
                             TEST_VALID_CONTAINER_NAME, null,
                             TEST_VALID_CONTAINER_HASH),
                Arguments.of("docker.io/test-container-image:latest",
                             "docker.io", "", TEST_VALID_CONTAINER_NAME, TEST_VALID_CONTAINER_TAG,
                             null));
    }

    @ParameterizedTest
    @MethodSource("createValidImageUrl")
    void parseContainerImageUrl_WithValidFormat_Success(String imageUrl,
                                                        String registryHost,
                                                        String repository,
                                                        String imageName,
                                                        String tag,
                                                        String digest) {
        // When
        NgcContainerRegistryClient.ContainerImageComponents components =
                parseContainerImageUrl(imageUrl);

        // Then
        assertNotNull(components);
        assertEquals(registryHost, components.registryHost());
        assertEquals(repository, components.repository());
        assertEquals(imageName, components.imageName());
        assertEquals(tag, components.tag());
        assertEquals(digest, components.digest());
    }

    @MethodSource("createValidImageUrl")
    @ParameterizedTest
    void validateContainerImage_Success(String containerUrl) {
        if (containerUrl.startsWith("stg.nvcr.io")) {
            ngcContainerRegistryClient.validateContainerImage(containerUrl,
                                                              MOCK_NGC_CONTAINER_REGISTRY_CRED);
        }
    }

    @Test
    void validateContainerImage_PermissionDenied_Fail() {
        assertThrows(ForbiddenException.class, () -> {
            ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE_PERMISSION_DENIED.toString(),
                    MOCK_NGC_CONTAINER_REGISTRY_CRED);
        });
    }

    @Test
    void validateContainerImage_NotExist_Fail() {
        assertThrows(NotFoundException.class, () -> {
            ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE_NOT_EXISTS.toString(),
                    MOCK_NGC_CONTAINER_REGISTRY_CRED);
        });
    }

    @Test
    void validateContainerImage_InvalidTag_Fail() {
        assertThrows(BadRequestException.class, () -> {
            ngcContainerRegistryClient.validateContainerImage(
                    TEST_NGC_CONTAINER_IMAGE_WITH_INVALID_TAG.toString(),
                    MOCK_NGC_CONTAINER_REGISTRY_CRED);
        });
    }

    @Test
    void validateContainerImage_CachesBearerToken_Success() {
        String containerUrl = TEST_NGC_CONTAINER_IMAGE.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;
        String targetUrl =
                "/proxy_auth?account=%24oauthtoken&scope=repository%3Awhw3rcpsilnj%2Ftest-container-image%3Apull";
        ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey);
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl)));

        ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey);
        // Should use cached authentication token without additional API call
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl)));
    }

    @Test
    void validateContainerImage_DifferentApiKeys_SeparateCacheEntries() {
        String containerUrl = TEST_NGC_CONTAINER_IMAGE.toString();
        String apiKey1 = MOCK_NGC_CONTAINER_REGISTRY_CRED;
        String apiKey2 = "different-" + MOCK_NGC_CONTAINER_REGISTRY_CRED;
        String targetUrl =
                "/proxy_auth?account=%24oauthtoken&scope=repository%3Awhw3rcpsilnj%2Ftest-container-image%3Apull";
        ngcContainerRegistryClient.validateContainerImage(containerUrl, apiKey1);
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl)));
        ngcContainerRegistryClient.validateContainerImage(containerUrl,
                                                          apiKey2);
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(2, getRequestedFor(urlEqualTo(targetUrl)));

    }

    @Test
    void validateContainerImage_DifferentContainerImageTag_CachesBearerToken_Success() {
        String containerUrl1 = TEST_NGC_CONTAINER_IMAGE.toString();
        String containerUrl2 = TEST_NGC_CONTAINER_IMAGE_WITH_DIGEST.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;
        String targetUrl =
                "/proxy_auth?account=%24oauthtoken&scope=repository%3Awhw3rcpsilnj%2Ftest-container-image%3Apull";

        ngcContainerRegistryClient.validateContainerImage(containerUrl1, apiKey);
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl)));

        ngcContainerRegistryClient.validateContainerImage(containerUrl2, apiKey);
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl)));
    }

    @Test
    void validateContainerImage_DifferentContainerImageName_SeparateCacheEntries() {
        String containerUrl1 = TEST_NGC_CONTAINER_IMAGE.toString();
        String containerUrl2 = TEST_NGC_CONTAINER_IMAGE_2.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;
        String targetUrl1 =
                "/proxy_auth?account=%24oauthtoken&scope=repository%3Awhw3rcpsilnj%2Ftest-container-image%3Apull";
        ngcContainerRegistryClient.validateContainerImage(containerUrl1, apiKey);
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl1)));

        String targetUrl2 =
                "/proxy_auth?account=%24oauthtoken&scope=repository%3Awhw3rcpsilnj%2Ftest-container-image-2%3Apull";
        assertThrows(NotFoundException.class, () ->
                ngcContainerRegistryClient.validateContainerImage(containerUrl2, apiKey));
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl1)));
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl2)));
    }

    @Test
    void validateContainerImage_DifferentOrg_SeparateCacheEntries() {
        String containerUrl1 = TEST_NGC_CONTAINER_IMAGE.toString();
        String containerUrl2 = TEST_NGC_CONTAINER_IMAGE_UNKNOWN_ORG.toString();
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;

        String targetUrl1 =
                "/proxy_auth?account=%24oauthtoken&scope=repository%3Awhw3rcpsilnj%2Ftest-container-image%3Apull";
        ngcContainerRegistryClient.validateContainerImage(containerUrl1, apiKey);
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl1)));

        String targetUrl2 =
                "/proxy_auth?account=%24oauthtoken&scope=repository%3Asomeone-org%2Ftest-container-image%3Apull";
        assertThrows(NotFoundException.class, () ->
                ngcContainerRegistryClient.validateContainerImage(containerUrl2, apiKey));
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl1)));
        MockNgcContainerRegistryServer.getNgcContainerRegistryMockServer().
                verify(1, getRequestedFor(urlEqualTo(targetUrl2)));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty string
            "invalid",  // no slashes
            "nvcr.io",  // only registry
            "nvcr.io/",  // trailing slash
            "nvcr.io/example:",  // empty tag
            "nvcr.io/example@",  // empty digest
            "nvcr.io/example:tag:extra",  // multiple colons
            "nvcr.io/example@digest@extra"  // multiple @ symbols
    })
    void parseContainerImageUrl_WithInvalidFormats_Fail(String invalidUrl) {
        // When/Then
        assertThrows(BadRequestException.class, () -> parseContainerImageUrl(invalidUrl));
    }

    @Test
    void validateCredential_Success() {
        String registryHost = TEST_NGC_CONTAINER_REGISTRY;
        String apiKey = MOCK_NGC_CONTAINER_REGISTRY_CRED;

        String token = ngcContainerRegistryClient.validateCredential(registryHost, apiKey);
        assertNotNull(token);
        assertEquals("mockBearerToken", token);
    }
}
