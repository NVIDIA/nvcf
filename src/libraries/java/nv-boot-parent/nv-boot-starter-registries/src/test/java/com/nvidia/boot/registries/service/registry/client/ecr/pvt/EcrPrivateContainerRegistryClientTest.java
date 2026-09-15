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

package com.nvidia.boot.registries.service.registry.client.ecr.pvt;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.parsePrivateArtifactUrl;
import static com.nvidia.boot.registries.service.registry.client.ecr.pvt.EcrPrivateContainerRegistryClient.ECR_CONTAINER_IMAGE_URL_PATTERN;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_REGISTRY_API_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_INVALID_ECR_REGISTRY_CRED;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.mock.ecr.MockEcrPrivateRegistryServer;
import com.nvidia.boot.registries.service.registry.client.ecr.dto.EcrArtifactComponents;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.web.reactive.function.client.WebClient;

class EcrPrivateContainerRegistryClientTest {

    private static EcrPrivateContainerRegistryClient ecrPrivateContainerRegistryClient;

    // Test ECR URLs for integration testing
    private static final String TEST_ECR_CONTAINER_IMAGE_WITH_TAG_1 =
            "123456789012.dkr.ecr.us-west-2.amazonaws.com/test-repo:v1.0.0";
    private static final String TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST_1 =
            "123456789012.dkr.ecr.us-west-2.amazonaws.com/test-repo@sha256:abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab";
    private static final String TEST_ECR_CONTAINER_IMAGE_WITHOUT_TAG_1 =
            "123456789012.dkr.ecr.us-west-2.amazonaws.com/test-repo";
    private static final String TEST_ECR_CONTAINER_IMAGE_MULTI_NAMESPACE_1 =
            "123456789012.dkr.ecr.us-east-1.amazonaws.com/namespace/sub-namespace/test-repo:latest";

    @BeforeAll
    static void beforeAll() {
        ecrPrivateContainerRegistryClient = new EcrPrivateContainerRegistryClient(
                WebClientUtils.builder(),
                MOCK_ECR_REGISTRY_API_URL,
                MOCK_ECR_REGISTRY_CLIENT_CALL_TIMEOUT);
        MockEcrPrivateRegistryServer.start(MOCK_ECR_REGISTRY_API_URL);
    }

    @AfterAll
    static void cleanup() {
        MockEcrPrivateRegistryServer.stop();
    }

    static Stream<Arguments> createValidImageUrls() {
        return Stream.of(
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_WITH_TAG_1,
                             "123456789012", "us-west-2", "test-repo", "v1.0.0", null),
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST_1,
                             "123456789012", "us-west-2", "test-repo", null,
                             "sha256:abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab"),
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_WITHOUT_TAG_1,
                             "123456789012", "us-west-2", "test-repo", "latest", null),
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_MULTI_NAMESPACE_1,
                             "123456789012", "us-east-1", "namespace/sub-namespace/test-repo",
                             "latest", null)
        );
    }

    @ParameterizedTest
    @MethodSource("createValidImageUrls")
    void parseContainerImageUrl_WithValidFormat_Success(String imageUrl,
                                                        String expectedRegistryId,
                                                        String expectedRegion,
                                                        String expectedRepository,
                                                        String expectedTag,
                                                        String expectedDigest) {

        EcrArtifactComponents components = parsePrivateArtifactUrl(imageUrl,
                                                                   ECR_CONTAINER_IMAGE_URL_PATTERN);

        assertNotNull(components);
        assertEquals(expectedRegistryId, components.registryId());
        assertEquals(expectedRegion, components.region());
        assertEquals(expectedRepository, components.repositoryName());
        assertEquals(expectedTag, components.tag());
        assertEquals(expectedDigest, components.digest());
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty string
            "   ",  // whitespace only
            "invalid-url",  // not ECR format
            "123.dkr.ecr.us-west-2.amazonaws.com/repo:tag",  // invalid registry ID (too short)
            "1234567890123.dkr.ecr.us-west-2.amazonaws.com/repo:tag",
            // invalid registry ID (too long)
            "123456789012.ecr.us-west-2.amazonaws.com/repo:tag",  // missing 'dkr'
            "123456789012.dkr.ecr..amazonaws.com/repo:tag",  // empty region
            "123456789012.dkr.ecr.us-west-2.amazonaws.com/",  // empty repository
            "123456789012.dkr.ecr.us-west-2.amazonaws.com/:tag",  // empty repository with tag
            "docker.io/library/nginx:latest",  // not ECR URL
            "123456789012.dkr.ecr.us-west-2.amazonaws.com/repo@invalid-digest"
            // invalid digest format
    })
    void parseContainerImageUrl_WithInvalidFormats_Fail(String invalidUrl) {
        assertThrows(BadRequestException.class,
                     () -> parsePrivateArtifactUrl(invalidUrl, ECR_CONTAINER_IMAGE_URL_PATTERN));
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
        String imageUrl = String.format("123456789012.dkr.ecr.us-west-2.amazonaws.com/%s:latest",
                                        repositoryName);
        assertDoesNotThrow(
                () -> parsePrivateArtifactUrl(imageUrl, ECR_CONTAINER_IMAGE_URL_PATTERN));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "us-east-1",
            "us-west-2",
            "eu-west-1",
            "eu-central-1",
            "ap-southeast-1",
            "ap-northeast-1",
            "ca-central-1",
            "sa-east-1"
    })
    void parseContainerImageUrl_VariousRegions_Success(String region) {
        // Given
        String imageUrl =
                String.format("123456789012.dkr.ecr.%s.amazonaws.com/test-repo:latest", region);

        assertDoesNotThrow(
                () -> parsePrivateArtifactUrl(imageUrl, ECR_CONTAINER_IMAGE_URL_PATTERN));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "latest",
            "v1.0.0",
            "1.2.3",
            "dev-branch",
            "feature_branch",
            "release-2023-12-01",
            "SHA-abc123def"
    })
    void parseContainerImageUrl_VariousTagFormats_Success(String tag) {
        // Given
        String imageUrl =
                String.format("123456789012.dkr.ecr.us-west-2.amazonaws.com/test-repo:%s", tag);

        assertDoesNotThrow(
                () -> parsePrivateArtifactUrl(imageUrl, ECR_CONTAINER_IMAGE_URL_PATTERN));
    }

    static Stream<Arguments> createValidContainerImages() {
        return Stream.of(
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_WITH_TAG.toString()),
                Arguments.of(TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST.toString())
        );
    }

    @ParameterizedTest
    @MethodSource("createValidContainerImages")
    void validateContainerImage_Success(String containerImageUrl) {
        assertDoesNotThrow(
                () -> ecrPrivateContainerRegistryClient.validateContainerImage(containerImageUrl,
                                                                               MOCK_ECR_REGISTRY_CRED));
    }

    @Test
    void validateContainerImage_PermissionDenied_Fail() {
        assertThrows(ForbiddenException.class, () ->
                ecrPrivateContainerRegistryClient.validateContainerImage(
                        TEST_ECR_CONTAINER_IMAGE_PERMISSION_DENIED.toString(),
                        MOCK_ECR_REGISTRY_CRED));
    }

    @Test
    void validateContainerImage_NotExist_Fail() {
        assertThrows(BadRequestException.class, () ->
                ecrPrivateContainerRegistryClient.validateContainerImage(
                        TEST_ECR_CONTAINER_IMAGE_TAG_NOT_FOUND.toString(),
                        MOCK_ECR_REGISTRY_CRED));
    }

    @Test
    void validateContainerImage_DigestNotFound_Fail() {
        assertThrows(BadRequestException.class, () ->
                ecrPrivateContainerRegistryClient.validateContainerImage(
                        TEST_ECR_CONTAINER_IMAGE_DIGEST_NOT_FOUND.toString(),
                        MOCK_ECR_REGISTRY_CRED));
    }

    @ParameterizedTest(name = "Should reject invalid credentials: {0}")
    @ValueSource(strings = {
            "",  // empty credentials
            "invalid-base64",  // invalid base64
            "dGVzdA==",  // valid base64 but no colon (decodes to "test")
    })
    void validateContainerImage_InvalidCredentials_Fail(String invalidCredentials) {
        // When & Then
        assertThrows(BadRequestException.class, () ->
                ecrPrivateContainerRegistryClient.validateContainerImage(
                        TEST_ECR_CONTAINER_IMAGE_WITH_TAG.toString(),
                        invalidCredentials));
    }

    @Test
    void validateCredential_ValidHostnameAndCredentials_Success() {
        String hostname = "123456789012.dkr.ecr.us-west-2.amazonaws.com";
        assertDoesNotThrow(() ->
                ecrPrivateContainerRegistryClient.validateCredential(hostname,
                                                                     MOCK_ECR_REGISTRY_CRED));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "123456789012.dkr.ecr.us-east-1.amazonaws.com",
            "123456789012.dkr.ecr.eu-west-1.amazonaws.com",
            "987654321098.dkr.ecr.ap-southeast-1.amazonaws.com"
    })
    void validateCredential_VariousValidHostnames_Success(String hostname) {
        assertDoesNotThrow(() ->
                ecrPrivateContainerRegistryClient.validateCredential(hostname,
                                                                     MOCK_ECR_REGISTRY_CRED));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "invalid-hostname",
            "123.dkr.ecr.us-west-2.amazonaws.com",  // invalid account ID
            "123456789012.ecr.us-west-2.amazonaws.com",  // missing 'dkr'
            "public.ecr.aws",  // ECR public hostname
            ""  // empty hostname
    })
    void validateCredential_InvalidHostname_Fail(String hostname) {
        assertThrows(BadRequestException.class, () ->
                ecrPrivateContainerRegistryClient.validateCredential(hostname,
                                                                     MOCK_ECR_REGISTRY_CRED));
    }

    @Test
    void validateCredential_InvalidCredentials_Fail() {
        String hostname = "123456789012.dkr.ecr.us-west-2.amazonaws.com";
        assertThrows(BadRequestException.class, () ->
                ecrPrivateContainerRegistryClient.validateCredential(hostname, 
                                                                     MOCK_INVALID_ECR_REGISTRY_CRED));
    }
}
