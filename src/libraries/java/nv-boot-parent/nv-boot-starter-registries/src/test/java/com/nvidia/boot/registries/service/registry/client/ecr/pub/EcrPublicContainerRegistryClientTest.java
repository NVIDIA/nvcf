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

package com.nvidia.boot.registries.service.registry.client.ecr.pub;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.parsePublicArtifactUrl;
import static com.nvidia.boot.registries.service.registry.client.ecr.pub.EcrPublicContainerRegistryClient.ECR_PUBLIC_CONTAINER_IMAGE_URL_PATTERN;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_PUBLIC_REGISTRY_API_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_PUBLIC_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_PUBLIC_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_INVALID_ECR_PUBLIC_REGISTRY_CRED;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.mock.ecr.MockEcrPublicRegistryServer;
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

class EcrPublicContainerRegistryClientTest {

    private static EcrPublicContainerRegistryClient ecrPublicContainerRegistryClient;

    // Test ECR Public URLs for integration testing
    private static final String TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG_1 =
            "public.ecr.aws/test-alias/test-repo:v1.0.0";
    private static final String TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST_1 =
            "public.ecr.aws/test-alias/test-repo@sha256:abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab";
    private static final String TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITHOUT_TAG_1 =
            "public.ecr.aws/test-alias/test-repo";
    private static final String TEST_ECR_PUBLIC_CONTAINER_IMAGE_MULTI_NAMESPACE_1 =
            "public.ecr.aws/test-alias/namespace/sub-namespace/test-repo:latest";

    @BeforeAll
    static void beforeAll() {
        ecrPublicContainerRegistryClient = new EcrPublicContainerRegistryClient(
                WebClientUtils.builder(),
                MOCK_ECR_PUBLIC_REGISTRY_API_URL,
                MOCK_ECR_PUBLIC_REGISTRY_CLIENT_CALL_TIMEOUT);
        MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_API_URL);
    }

    @AfterAll
    static void cleanup() {
        MockEcrPublicRegistryServer.stop();
    }

    static Stream<Arguments> createValidImageUrls() {
        return Stream.of(
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG_1,
                             "test-alias", "test-repo", "v1.0.0", null),
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST_1,
                             "test-alias", "test-repo", null,
                             "sha256:abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab"),
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITHOUT_TAG_1,
                             "test-alias", "test-repo", "latest", null),
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_MULTI_NAMESPACE_1,
                             "test-alias", "namespace/sub-namespace/test-repo", "latest", null)
        );
    }

    @ParameterizedTest
    @MethodSource("createValidImageUrls")
    void parseContainerImageUrl_WithValidFormat_Success(String imageUrl,
                                                        String expectedRegistryAlias,
                                                        String expectedRepository,
                                                        String expectedTag,
                                                        String expectedDigest) {

        EcrArtifactComponents components = parsePublicArtifactUrl(imageUrl,
                                                                  ECR_PUBLIC_CONTAINER_IMAGE_URL_PATTERN);

        assertNotNull(components);
        assertEquals(expectedRepository, components.repositoryName());
        assertEquals(expectedTag, components.tag());
        assertEquals(expectedDigest, components.digest());
        assertThat(components.registryId()).isNull();
        assertThat(components.region()).isNull();
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty string
            "   ",  // whitespace only
            "invalid-url",  // not ECR Public format
            "public.ecr.com/alias/repo:tag",  // wrong domain
            "public.ecr.aws//repo:tag",  // empty registry alias
            "public.ecr.aws/alias/:tag",  // empty repository
            "public.ecr.aws/alias/repo@invalid-digest",  // invalid digest format
            "123456789012.dkr.ecr.us-west-2.amazonaws.com/repo:tag",  // ECR private format
            "docker.io/library/nginx:latest",  // Docker Hub format
            "public.ecr.aws/alias-with_invalid_chars!/repo:tag",  // invalid registry alias
            "public.ecr.aws/alias/repo:tag:extra",  // multiple colons
    })
    void parseContainerImageUrl_WithInvalidFormats_Fail(String invalidUrl) {
        assertThrows(BadRequestException.class,
                     () -> parsePublicArtifactUrl(invalidUrl,
                                                  ECR_PUBLIC_CONTAINER_IMAGE_URL_PATTERN));
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
        String imageUrl = String.format("public.ecr.aws/test-alias/%s:latest", repositoryName);
        assertDoesNotThrow(
                () -> parsePublicArtifactUrl(imageUrl, ECR_PUBLIC_CONTAINER_IMAGE_URL_PATTERN));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "test-alias",
            "simple",
            "alias-with-dashes",
            "alias_with_underscores",
            "alias.with.dots",
            "123numeric-alias",
            "a",  // minimum length
            "very-long-alias-name-that-is-still-valid"
    })
    void parseContainerImageUrl_VariousRegistryAliases_Success(String registryAlias) {
        String imageUrl = String.format("public.ecr.aws/%s/test-repo:latest", registryAlias);
        assertDoesNotThrow(
                () -> parsePublicArtifactUrl(imageUrl, ECR_PUBLIC_CONTAINER_IMAGE_URL_PATTERN));
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
        String imageUrl = String.format("public.ecr.aws/test-alias/test-repo:%s", tag);
        assertDoesNotThrow(
                () -> parsePublicArtifactUrl(imageUrl, ECR_PUBLIC_CONTAINER_IMAGE_URL_PATTERN));
    }

    static Stream<Arguments> createValidContainerImages() {
        return Stream.of(
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG.toString()),
                Arguments.of(TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST.toString())
        );
    }

    @ParameterizedTest
    @MethodSource("createValidContainerImages")
    void validateContainerImage_Success(String containerImageUrl) {
        assertDoesNotThrow(
                () -> ecrPublicContainerRegistryClient
                        .validateContainerImage(containerImageUrl,
                                                MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateContainerImage_PermissionDenied_Fail() {
        assertThrows(ForbiddenException.class, () ->
                ecrPublicContainerRegistryClient.validateContainerImage(
                        TEST_ECR_PUBLIC_CONTAINER_IMAGE_PERMISSION_DENIED.toString(),
                        MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateContainerImage_NotFound_Fail() {
        assertThrows(BadRequestException.class, () ->
                ecrPublicContainerRegistryClient.validateContainerImage(
                        TEST_ECR_PUBLIC_CONTAINER_IMAGE_TAG_NOT_FOUND.toString(),
                        MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateContainerImage_DigestNotFound_Fail() {
        assertThrows(BadRequestException.class, () ->
                ecrPublicContainerRegistryClient.validateContainerImage(
                        TEST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST_NOT_FOUND.toString(),
                        MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateCredential_ValidHostnameAndCredentials_Success() {
        String hostname = "public.ecr.aws";
        assertDoesNotThrow(() ->
                ecrPublicContainerRegistryClient.validateCredential(hostname,
                                                                    MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateCredential_InvalidCredentials_Fail() {
        String hostname = "public.ecr.aws";
        assertThrows(BadRequestException.class, () ->
                ecrPublicContainerRegistryClient.validateCredential(hostname, 
                                                                    MOCK_INVALID_ECR_PUBLIC_REGISTRY_CRED));
    }
}
