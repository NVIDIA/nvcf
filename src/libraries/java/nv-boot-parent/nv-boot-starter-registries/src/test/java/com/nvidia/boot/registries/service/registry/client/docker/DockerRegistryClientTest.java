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

package com.nvidia.boot.registries.service.registry.client.docker;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_CONTAINER_HASH;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_DOCKER_NAMESPACE_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_DOCKER_REPO_NAME;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VALID_DOCKER_TAG_NAME;
import static com.nvidia.boot.registries.service.registry.client.docker.DockerRegistryClient.parseImageUrl;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_CONTAINER_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_REGISTRY_OAUTH2_GROUP_SCOPE;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_REGISTRY_OAUTH2_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_REGISTRY_URL;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.docker.MockDockerRegistryAuthServer;
import com.nvidia.boot.mock.docker.MockDockerRegistryServer;
import com.nvidia.boot.registries.service.registry.client.docker.DockerRegistryClient.DockerImageComponents;
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

class DockerRegistryClientTest {
    private static DockerRegistryClient dockerRegistryClient;

    @BeforeAll
    static void beforeAll() {
        dockerRegistryClient = new DockerRegistryClient(
                WebClientUtils.builder(),
                MOCK_DOCKER_REGISTRY_URL,
                MOCK_DOCKER_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_DOCKER_REGISTRY_OAUTH2_URL,
                MOCK_DOCKER_REGISTRY_OAUTH2_GROUP_SCOPE
        );
        MockDockerRegistryServer.start(MOCK_DOCKER_REGISTRY_URL);
        MockDockerRegistryAuthServer.start(MOCK_DOCKER_REGISTRY_OAUTH2_URL);
    }

    @AfterAll
    static void cleanup() {
        MockDockerRegistryServer.stop();
        MockDockerRegistryAuthServer.stop();
    }

    @AfterEach
    void reset() {
        dockerRegistryClient.resetAuthTokenCache();
    }

    static Stream<Arguments> createValidImageUrl() {
        return Stream.of(
                Arguments.of(TEST_DOCKER_CONTAINER_IMAGE.toString(),
                             TEST_VALID_DOCKER_NAMESPACE_NAME,
                             TEST_VALID_DOCKER_REPO_NAME,
                             TEST_VALID_DOCKER_TAG_NAME,
                             null),
                Arguments.of(TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST.toString(),
                             TEST_VALID_DOCKER_NAMESPACE_NAME,
                             TEST_VALID_DOCKER_REPO_NAME,
                             null,
                             TEST_VALID_CONTAINER_HASH),
                Arguments.of("docker.io/test-docker-namespace/test-docker-repo:latest",
                             TEST_VALID_DOCKER_NAMESPACE_NAME,
                             TEST_VALID_DOCKER_REPO_NAME,
                             "latest",
                             null),

                // helm version of the url, basically just add https:// in front
                Arguments.of("oci://" + TEST_DOCKER_CONTAINER_IMAGE,
                             TEST_VALID_DOCKER_NAMESPACE_NAME,
                             TEST_VALID_DOCKER_REPO_NAME,
                             TEST_VALID_DOCKER_TAG_NAME,
                             null),
                Arguments.of("oci://" + TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST,
                             TEST_VALID_DOCKER_NAMESPACE_NAME,
                             TEST_VALID_DOCKER_REPO_NAME,
                             null,
                             TEST_VALID_CONTAINER_HASH),
                Arguments.of("oci://docker.io/test-docker-namespace/test-docker-repo:latest",
                             TEST_VALID_DOCKER_NAMESPACE_NAME,
                             TEST_VALID_DOCKER_REPO_NAME,
                             "latest",
                             null)
        );
    }

    @ParameterizedTest
    @MethodSource("createValidImageUrl")
    void parseContainerImageUrl_WithValidFormat_Success(String imageUrl,
                                                        String namespace,
                                                        String repository,
                                                        String tag,
                                                        String digest) {
        // When
        DockerImageComponents components = parseImageUrl(imageUrl);

        // Then
        assertNotNull(components);
        assertEquals(namespace, components.namespace());
        assertEquals(repository, components.repository());
        assertEquals(tag, components.tag());
        assertEquals(digest, components.digest());
    }

    @MethodSource("createValidImageUrl")
    @ParameterizedTest
    void validateContainerImage_Success(String containerUrl) {
        dockerRegistryClient
                .validateImage(containerUrl,
                               MOCK_DOCKER_CONTAINER_REGISTRY_CRED);
    }

    @Test
    void validateContainerImage_PermissionDenied_Fail() {
        assertThrows(ForbiddenException.class, () -> {
            dockerRegistryClient.validateImage(
                    TEST_DOCKER_CONTAINER_IMAGE_PERMISSION_DENIED.toString(),
                    MOCK_DOCKER_CONTAINER_REGISTRY_CRED);
        });
    }

    @Test
    void validateContainerImage_NotExist_Fail() {
        assertThrows(NotFoundException.class, () -> {
            dockerRegistryClient.validateImage(
                    TEST_DOCKER_CONTAINER_IMAGE_NOT_EXISTS.toString(),
                    MOCK_DOCKER_CONTAINER_REGISTRY_CRED);
        });
    }

    @Test
    void validateCredential_WithValidCredentials_Success() {
        // This test validates credentials without checking any specific image
        dockerRegistryClient.validateCredential("docker.io", 
                                                MOCK_DOCKER_CONTAINER_REGISTRY_CRED);
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty string
            "invalid",  // no slashes
            "docker.io",  // only registry
            "docker.io/",  // trailing slash
            "docker.io/mynamespace/myrepo/tag",  // wrong format, slash instead of colon
            "docker.io/myrepo:tag",  // no namespace
            "docker.io/mynamespace:tag",  // no repo
            "https://docker.io/mynamespace/myrepo:tag", // wrong helm prefix
            "oci:/docker.io/mynamespace/myrepo:tag", // wrong helm host
    })
    void parseContainerImageUrl_WithInvalidFormats_Fail(String invalidUrl) {
        // When/Then
        assertThrows(BadRequestException.class, () -> parseImageUrl(invalidUrl));
    }

    @Test
    void validateImage_RepeatedCalls_Success() {
        String imageUrl = TEST_DOCKER_CONTAINER_IMAGE.toString();
        String apiKey = MOCK_DOCKER_CONTAINER_REGISTRY_CRED;

        // First call - should fetch token and cache it
        dockerRegistryClient.validateImage(imageUrl, apiKey);

        // Second call - should use cached token
        dockerRegistryClient.validateImage(imageUrl, apiKey);

        // Third call - should still work with cached token
        dockerRegistryClient.validateImage(imageUrl, apiKey);
    }

    @Test
    void validateImage_DifferentImageWithSameCredentials_Success() {
        String imageUrl1 = TEST_DOCKER_CONTAINER_IMAGE.toString();
        String imageUrl2 = TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST.toString();
        String apiKey = MOCK_DOCKER_CONTAINER_REGISTRY_CRED;

        // Both images use same namespace/repo, so should share cache entry
        dockerRegistryClient.validateImage(imageUrl1, apiKey);
        dockerRegistryClient.validateImage(imageUrl2, apiKey);
    }

    @Test
    void resetAuthTokenCache_ClearsCacheSuccessfully() {
        String imageUrl = TEST_DOCKER_CONTAINER_IMAGE.toString();
        String apiKey = MOCK_DOCKER_CONTAINER_REGISTRY_CRED;

        // Populate cache
        dockerRegistryClient.validateImage(imageUrl, apiKey);

        // Reset cache
        dockerRegistryClient.resetAuthTokenCache();

        // Should still work after cache reset (will fetch new token)
        dockerRegistryClient.validateImage(imageUrl, apiKey);
    }
}
