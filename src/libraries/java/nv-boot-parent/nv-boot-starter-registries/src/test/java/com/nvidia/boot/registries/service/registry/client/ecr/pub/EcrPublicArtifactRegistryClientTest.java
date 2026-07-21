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
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.parsePublicArtifactUrl;
import static com.nvidia.boot.registries.service.registry.client.ecr.pub.EcrPublicArtifactRegistryClient.ECR_PUBLIC_HELM_CHART_URL_PATTERN;
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
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.web.reactive.function.client.WebClient;

class EcrPublicArtifactRegistryClientTest {

    private static EcrPublicArtifactRegistryClient ecrPublicArtifactRegistryClient;

    // Test ECR Public Helm chart URLs for integration testing
    private static final String TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG_1 =
            "oci://public.ecr.aws/test-alias/helm-charts/my-chart:v1.0.0";
    private static final String TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST_1 =
            "oci://public.ecr.aws/test-alias/helm-charts/my-chart@sha256:abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab";
    private static final String TEST_ECR_PUBLIC_HELM_CHART_WITHOUT_TAG_1 =
            "oci://public.ecr.aws/test-alias/helm-charts/my-chart";
    private static final String TEST_ECR_PUBLIC_HELM_CHART_MULTI_NAMESPACE_1 =
            "oci://public.ecr.aws/test-alias/namespace/sub-namespace/helm-charts/my-chart:latest";

    @BeforeAll
    static void beforeAll() {
        ecrPublicArtifactRegistryClient = new EcrPublicArtifactRegistryClient(
                WebClientUtils.builder(),
                MOCK_ECR_PUBLIC_REGISTRY_API_URL,
                MOCK_ECR_PUBLIC_REGISTRY_CLIENT_CALL_TIMEOUT);
        MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_API_URL);
    }

    @AfterAll
    static void cleanup() {
        MockEcrPublicRegistryServer.stop();
    }

    static Stream<Arguments> createValidHelmChartUrls() {
        return Stream.of(
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG_1,
                             "helm-charts/my-chart", "v1.0.0", null),
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST_1,
                             "helm-charts/my-chart", null,
                             "sha256:abcd1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab"),
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITHOUT_TAG_1,
                             "helm-charts/my-chart", "latest", null),
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_MULTI_NAMESPACE_1,
                             "namespace/sub-namespace/helm-charts/my-chart", "latest",
                             null)
        );
    }

    @ParameterizedTest
    @MethodSource("createValidHelmChartUrls")
    void parseHelmChartUrl_WithValidFormat_Success(String helmChartUrl,
                                                   String expectedRepository,
                                                   String expectedTag,
                                                   String expectedDigest) {

        var components = parsePublicArtifactUrl(helmChartUrl, ECR_PUBLIC_HELM_CHART_URL_PATTERN);

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
            "invalid-url",  // not ECR Public OCI format
            "oci://public.ecr.com/alias/chart:tag",  // wrong domain
            "oci://public.ecr.aws//chart:tag",  // empty registry alias
            "oci://public.ecr.aws/alias/:tag",  // empty repository
            "oci://public.ecr.aws/alias/chart@invalid-digest",  // invalid digest format
            "https://helm.example.com/charts/my-chart:latest",  // not ECR Public URL
            "public.ecr.aws/alias/chart:tag",  // missing 'oci://' prefix
            "oci://123456789012.dkr.ecr.us-west-2.amazonaws.com/chart:tag",  // ECR private format
            "oci://public.ecr.aws/alias-with_invalid_chars!/chart:tag",  // invalid registry alias
            "oci://public.ecr.aws/alias/chart:tag:extra",  // multiple colons
    })
    void parseHelmChartUrl_WithInvalidFormats_Fail(String invalidUrl) {
        assertThrows(BadRequestException.class,
                     () -> parsePublicArtifactUrl(invalidUrl, ECR_PUBLIC_HELM_CHART_URL_PATTERN));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "simple-chart",
            "namespace/chart",
            "deep/namespace/structure/chart",
            "chart-with-dashes",
            "chart_with_underscores",
            "chart.with.dots",
            "123numeric-chart",
            "helm-charts/my-chart",
            "charts/stable/nginx"
    })
    void parseHelmChartUrl_VariousRepositoryFormats_Success(String repositoryName) {
        var helmChartUrl =
                String.format("oci://public.ecr.aws/test-alias/%s:latest", repositoryName);
        assertDoesNotThrow(
                () -> parsePublicArtifactUrl(helmChartUrl, ECR_PUBLIC_HELM_CHART_URL_PATTERN));
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
    void parseHelmChartUrl_VariousRegistryAliases_Success(String registryAlias) {
        var helmChartUrl =
                String.format("oci://public.ecr.aws/%s/helm-charts/my-chart:latest", registryAlias);
        assertDoesNotThrow(
                () -> parsePublicArtifactUrl(helmChartUrl, ECR_PUBLIC_HELM_CHART_URL_PATTERN));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "latest",
            "v1.0.0",
            "1.2.3",
            "0.1.0-alpha",
            "2.0.0-beta.1",
            "1.0.0-rc.1",
            "dev-branch",
            "feature_branch",
            "release-2023-12-01",
            "chart-v1.2.3"
    })
    void parseHelmChartUrl_VariousVersionFormats_Success(String version) {
        var helmChartUrl =
                String.format("oci://public.ecr.aws/test-alias/helm-charts/my-chart:%s", version);
        assertDoesNotThrow(
                () -> parsePublicArtifactUrl(helmChartUrl, ECR_PUBLIC_HELM_CHART_URL_PATTERN));
    }

    static Stream<Arguments> createValidHelmCharts() {
        return Stream.of(
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG.toString()),
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST.toString())
        );
    }

    @ParameterizedTest
    @MethodSource("createValidHelmCharts")
    void validateHelmChart_Success(String helmChartUrl) {
        assertDoesNotThrow(() -> ecrPublicArtifactRegistryClient
                .validateHelmChart(helmChartUrl,
                                   MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateHelmChart_PermissionDenied_Fail() {
        assertThrows(ForbiddenException.class, () ->
                ecrPublicArtifactRegistryClient.validateHelmChart(
                        TEST_ECR_PUBLIC_HELM_CHART_PERMISSION_DENIED.toString(),
                        MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateHelmChart_NotFound_Fail() {
        assertThrows(BadRequestException.class, () ->
                ecrPublicArtifactRegistryClient.validateHelmChart(
                        TEST_ECR_PUBLIC_HELM_CHART_TAG_NOT_FOUND.toString(),
                        MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateHelmChart_DigestNotFound_Fail() {
        assertThrows(BadRequestException.class, () ->
                ecrPublicArtifactRegistryClient.validateHelmChart(
                        TEST_ECR_PUBLIC_HELM_CHART_DIGEST_NOT_FOUND.toString(),
                        MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateCredential_ValidHostnameAndCredentials_Success() {
        String hostname = "public.ecr.aws";
        assertDoesNotThrow(() ->
                ecrPublicArtifactRegistryClient.validateCredential(hostname,
                                                                   MOCK_ECR_PUBLIC_REGISTRY_CRED));
    }

    @Test
    void validateCredential_InvalidCredentials_Fail() {
        String hostname = "public.ecr.aws";
        assertThrows(BadRequestException.class, () ->
                ecrPublicArtifactRegistryClient.validateCredential(hostname, 
                                                                   MOCK_INVALID_ECR_PUBLIC_REGISTRY_CRED));
    }
}
