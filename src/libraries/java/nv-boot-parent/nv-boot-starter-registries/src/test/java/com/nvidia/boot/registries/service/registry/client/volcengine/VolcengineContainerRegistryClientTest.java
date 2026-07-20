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

package com.nvidia.boot.registries.service.registry.client.volcengine;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_CONTAINER_IMAGE_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineContainerRegistryClient.CONTAINER_IMAGE_URL_PATTERN;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.parseArtifactUrl;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_INVALID_VOLCENGINE_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_VOLCENGINE_REGISTRY_API_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_VOLCENGINE_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_VOLCENGINE_REGISTRY_CRED;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.volcengine.MockVolcengineRegistryServer;
import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineArtifactComponents;
import com.nvidia.boot.registries.service.registry.client.volcengine.dto.VolcengineArtifactType;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.web.reactive.function.client.WebClient;

class VolcengineContainerRegistryClientTest {

    private static VolcengineContainerRegistryClient volcengineContainerRegistryClient;

    // Test Volcengine URLs for integration testing
    private static final String TEST_VOLCENGINE_CONTAINER_IMAGE_WITH_TAG_1 =
            "test-registry-cn-beijing.cr.volces.com/test-namespace/test-repo:v1.0.0";
    private static final String TEST_VOLCENGINE_CONTAINER_IMAGE_WITHOUT_TAG_1 =
            "test-registry-cn-beijing.cr.volces.com/test-namespace/test-repo";
    private static final String TEST_VOLCENGINE_CONTAINER_IMAGE_MULTI_REPO_1 =
            "test-registry-ap-singapore.cr.volces.com/test-namespace/test-repo/sub-repo:latest";

    @BeforeAll
    static void beforeAll() {
        volcengineContainerRegistryClient = new VolcengineContainerRegistryClient(
                WebClientUtils.builder(),
                MOCK_VOLCENGINE_REGISTRY_API_URL,
                MOCK_VOLCENGINE_REGISTRY_CLIENT_CALL_TIMEOUT);
        MockVolcengineRegistryServer.start(MOCK_VOLCENGINE_REGISTRY_API_URL);
    }

    @AfterAll
    static void cleanup() {
        MockVolcengineRegistryServer.stop();
    }

    static Stream<Arguments> createValidImageUrls() {
        return Stream.of(
                Arguments.of(TEST_VOLCENGINE_CONTAINER_IMAGE_WITH_TAG_1,
                             "test-registry", "cn-beijing", "test-namespace", "test-repo",
                             "v1.0.0"),
                Arguments.of(TEST_VOLCENGINE_CONTAINER_IMAGE_WITHOUT_TAG_1,
                             "test-registry", "cn-beijing", "test-namespace", "test-repo",
                             "latest"),
                Arguments.of(TEST_VOLCENGINE_CONTAINER_IMAGE_MULTI_REPO_1,
                             "test-registry", "ap-singapore", "test-namespace",
                             "test-repo/sub-repo", "latest")
        );
    }

    @ParameterizedTest
    @MethodSource("createValidImageUrls")
    void parseContainerImageUrl_WithValidFormat_Success(String imageUrl,
                                                        String expectedRegistry,
                                                        String expectedRegion,
                                                        String expectedNamespace,
                                                        String expectedRepository,
                                                        String expectedTag) {

        VolcengineArtifactComponents components =
                parseArtifactUrl(imageUrl, CONTAINER_IMAGE_URL_PATTERN,
                                 VolcengineArtifactType.IMAGE);

        assertNotNull(components);
        assertEquals(expectedRegistry, components.registry());
        assertEquals(expectedRegion, components.region());
        assertEquals(expectedNamespace, components.namespace());
        assertEquals(expectedRepository, components.repository());
        assertEquals(expectedTag, components.tag());
        assertEquals(VolcengineArtifactType.IMAGE, components.type());
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty string
            "   ",  // whitespace only
            "invalid-url",  // not Volcengine format
            "test.cr.volces.com/namespace/repo:tag",  // invalid registry format
            "test-registry.cr.volces.com/namespace/repo:tag",  // missing region
            "test-registry-invalid-region.cr.volces.com/namespace/repo:tag",  // invalid region
            "test-registry-cn-beijing.volces.com/namespace/repo:tag",  // missing 'cr'
            "test-registry-cn-beijing.cr.volces.com//repo:tag",  // empty namespace
            "test-registry-cn-beijing.cr.volces.com/namespace/:tag",  // empty repository
            "docker.io/library/nginx:latest",  // not Volcengine URL
    })
    void parseContainerImageUrl_WithInvalidFormats_Fail(String invalidUrl) {
        assertThrows(BadRequestException.class,
                     () -> parseArtifactUrl(invalidUrl,
                                            CONTAINER_IMAGE_URL_PATTERN,
                                            VolcengineArtifactType.IMAGE));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "simple-repo",
            "namespace/repo",
            "deep/namespace/structure/repo",
            "repo-with-dashes",
            "repo_with_underscores",
            "repo.with.dots",
            "123numeric-repo"
    })
    void parseContainerImageUrl_VariousRepositoryFormats_Success(String repositoryName) {
        String imageUrl =
                String.format("test-registry-cn-beijing.cr.volces.com/test-namespace/%s:latest",
                              repositoryName);
        assertDoesNotThrow(
                () -> parseArtifactUrl(imageUrl,
                                       CONTAINER_IMAGE_URL_PATTERN,
                                       VolcengineArtifactType.IMAGE));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "cn-beijing",
            "cn-shanghai",
            "ap-singapore",
            "ap-mumbai",
            "ap-southeast-1"
    })
    void parseContainerImageUrl_VariousRegions_Success(String region) {
        String imageUrl =
                String.format("test-registry-%s.cr.volces.com/test-namespace/test-repo:latest",
                              region);

        assertDoesNotThrow(
                () -> parseArtifactUrl(imageUrl,
                                       CONTAINER_IMAGE_URL_PATTERN,
                                       VolcengineArtifactType.IMAGE));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "latest",
            "v1.0.0",
            "1.2.3",
            "dev-branch",
            "feature_branch",
            "release-2023-12-01"
    })
    void parseContainerImageUrl_VariousTagFormats_Success(String tag) {
        String imageUrl =
                String.format("test-registry-cn-beijing.cr.volces.com/test-namespace/test-repo:%s",
                              tag);

        assertDoesNotThrow(
                () -> parseArtifactUrl(imageUrl,
                                       CONTAINER_IMAGE_URL_PATTERN,
                                       VolcengineArtifactType.IMAGE));
    }

    static Stream<Arguments> createValidContainerImages() {
        return Stream.of(
                Arguments.of(TEST_VOLCENGINE_CONTAINER_IMAGE_WITH_TAG.toString())
        );
    }

    @ParameterizedTest
    @MethodSource("createValidContainerImages")
    void validateContainerImage_Success(String containerImageUrl) {
        assertDoesNotThrow(
                () -> volcengineContainerRegistryClient.validateContainerImage(containerImageUrl,
                                                                               MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @Test
    void validateContainerImage_PermissionDenied_Fail() {
        assertThrows(ForbiddenException.class, () ->
                volcengineContainerRegistryClient.validateContainerImage(
                        TEST_VOLCENGINE_CONTAINER_IMAGE_PERMISSION_DENIED.toString(),
                        MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @Test
    void validateContainerImage_NotExist_Fail() {
        assertThrows(NotFoundException.class, () ->
                volcengineContainerRegistryClient.validateContainerImage(
                        TEST_VOLCENGINE_CONTAINER_IMAGE_TAG_NOT_FOUND.toString(),
                        MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty credentials
            "invalid-base64",  // invalid base64
            "dGVzdA==",  // valid base64 but no colon (decodes to "test")
    })
    void validateContainerImage_InvalidCredentials_Fail(String invalidCredentials) {
        assertThrows(BadRequestException.class, () ->
                volcengineContainerRegistryClient.validateContainerImage(
                        TEST_VOLCENGINE_CONTAINER_IMAGE_WITH_TAG.toString(),
                        invalidCredentials));
    }

    @Test
    void validateCredential_ValidHostnameAndCredentials_Success() {
        String hostname = "test-volcengine-registry-cn-beijing.cr.volces.com";
        assertDoesNotThrow(() ->
                volcengineContainerRegistryClient.validateCredential(hostname,
                                                                     MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "test-volcengine-registry-cn-beijing.cr.volces.com",
            "test-volcengine-registry-cn-shanghai.cr.volces.com",
            "my-registry-ap-southeast-1.cr.volces.com"
    })
    void validateCredential_VariousValidHostnames_Success(String hostname) {
        assertDoesNotThrow(() ->
                volcengineContainerRegistryClient.validateCredential(hostname,
                                                                     MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "invalid-hostname",
            "test.cr.volces.com",  // missing region
            "test-registry.cr.volces.com",  // missing region
            "test-registry-invalid-region.cr.volces.com",  // invalid region
            "public.ecr.aws",  // ECR public hostname
            ""  // empty hostname
    })
    void validateCredential_InvalidHostname_Fail(String hostname) {
        assertThrows(BadRequestException.class, () ->
                volcengineContainerRegistryClient.validateCredential(hostname,
                                                                     MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @Test
    void validateCredential_InvalidCredentials_Fail() {
        String hostname = "test-volcengine-registry-cn-beijing.cr.volces.com";
        assertThrows(BadRequestException.class, () ->
                volcengineContainerRegistryClient.validateCredential(hostname,
                                                                     MOCK_INVALID_VOLCENGINE_REGISTRY_CRED));
    }
}
