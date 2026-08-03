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
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_HELM_CHART_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineArtifactRegistryClient.HELM_CHART_URL_PATTERN;
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

class VolcengineArtifactRegistryClientTest {

    private static VolcengineArtifactRegistryClient volcengineArtifactRegistryClient;

    // Test Volcengine Helm chart URLs for integration testing
    private static final String TEST_VOLCENGINE_HELM_CHART_WITH_TAG_1 =
            "oci://test-registry-cn-beijing.cr.volces.com/test-namespace/helm-charts/my-chart:v1.0.0";
    private static final String TEST_VOLCENGINE_HELM_CHART_WITHOUT_TAG_1 =
            "oci://test-registry-cn-beijing.cr.volces.com/test-namespace/helm-charts/my-chart";

    @BeforeAll
    static void beforeAll() {
        volcengineArtifactRegistryClient = new VolcengineArtifactRegistryClient(
                WebClientUtils.builder(),
                MOCK_VOLCENGINE_REGISTRY_API_URL,
                MOCK_VOLCENGINE_REGISTRY_CLIENT_CALL_TIMEOUT);
        MockVolcengineRegistryServer.start(MOCK_VOLCENGINE_REGISTRY_API_URL);
    }

    @AfterAll
    static void cleanup() {
        MockVolcengineRegistryServer.stop();
    }

    static Stream<Arguments> createValidHelmChartUrls() {
        return Stream.of(
                Arguments.of(TEST_VOLCENGINE_HELM_CHART_WITH_TAG_1,
                             "test-registry", "cn-beijing", "test-namespace",
                             "helm-charts/my-chart", "v1.0.0"),
                Arguments.of(TEST_VOLCENGINE_HELM_CHART_WITHOUT_TAG_1,
                             "test-registry", "cn-beijing", "test-namespace",
                             "helm-charts/my-chart", "latest")
        );
    }

    @ParameterizedTest
    @MethodSource("createValidHelmChartUrls")
    void parseHelmChartUrl_WithValidFormat_Success(String helmChartUrl,
                                                   String expectedRegistry,
                                                   String expectedRegion,
                                                   String expectedNamespace,
                                                   String expectedRepository,
                                                   String expectedTag) {

        var components =
                parseArtifactUrl(helmChartUrl,
                                 HELM_CHART_URL_PATTERN,
                                 VolcengineArtifactType.CHART);

        assertNotNull(components);
        assertEquals(expectedRegistry, components.registry());
        assertEquals(expectedRegion, components.region());
        assertEquals(expectedNamespace, components.namespace());
        assertEquals(expectedRepository, components.repository());
        assertEquals(expectedTag, components.tag());
        assertEquals(VolcengineArtifactType.CHART, components.type());
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty string
            "   ",  // whitespace only
            "invalid-url",  // not Volcengine OCI format
            "oci://test.cr.volces.com/namespace/chart:tag",  // invalid registry format
            "oci://test-registry.cr.volces.com/namespace/chart:tag",  // missing region
            "oci://test-registry-invalid-region.cr.volces.com/namespace/chart:tag",
            // invalid region
            "oci://test-registry-cn-beijing.volces.com/namespace/chart:tag",  // missing 'cr'
            "oci://test-registry-cn-beijing.cr.volces.com//chart:tag",  // empty namespace
            "oci://test-registry-cn-beijing.cr.volces.com/namespace/:tag",  // empty repository
            "oci://test-registry-cn-beijing.cr.volces.com/namespace/chart@invalid-digest",
            // invalid digest format
            "https://helm.example.com/charts/my-chart:latest",  // not Volcengine URL
            "test-registry-cn-beijing.cr.volces.com/namespace/chart:tag",
            // missing 'oci://' prefix
    })
    void parseHelmChartUrl_WithInvalidFormats_Fail(String invalidUrl) {
        assertThrows(BadRequestException.class,
                     () -> parseArtifactUrl(invalidUrl,
                                            HELM_CHART_URL_PATTERN,
                                            VolcengineArtifactType.CHART));
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
                String.format(
                        "oci://test-registry-cn-beijing.cr.volces.com/test-namespace/%s:latest",
                        repositoryName);

        assertDoesNotThrow(() -> parseArtifactUrl(helmChartUrl,
                                                  HELM_CHART_URL_PATTERN,
                                                  VolcengineArtifactType.CHART));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "cn-beijing",
            "cn-shanghai",
            "ap-singapore",
            "ap-mumbai",
            "cn-north-1",
            "ap-southeast-1"
    })
    void parseHelmChartUrl_VariousRegions_Success(String region) {

        var helmChartUrl = String.format(
                "oci://test-registry-%s.cr.volces.com/test-namespace/helm-charts/my-chart:latest",
                region);

        assertDoesNotThrow(() -> parseArtifactUrl(helmChartUrl,
                                                  HELM_CHART_URL_PATTERN,
                                                  VolcengineArtifactType.CHART));
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

        var helmChartUrl = String.format(
                "oci://test-registry-cn-beijing.cr.volces.com/test-namespace/helm-charts/my-chart:%s",
                version);

        assertDoesNotThrow(() -> parseArtifactUrl(helmChartUrl,
                                                  HELM_CHART_URL_PATTERN,
                                                  VolcengineArtifactType.CHART));
    }

    static Stream<Arguments> createValidHelmCharts() {
        return Stream.of(
                Arguments.of(TEST_VOLCENGINE_HELM_CHART_WITH_TAG.toString())
        );
    }

    @ParameterizedTest
    @MethodSource("createValidHelmCharts")
    void validateHelmChart_Success(String helmChartUrl) {
        assertDoesNotThrow(() ->
                                   volcengineArtifactRegistryClient.validateHelmChart(helmChartUrl,
                                                                                      MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @Test
    void validateHelmChart_PermissionDenied_Fail() {
        assertThrows(ForbiddenException.class, () ->
                volcengineArtifactRegistryClient.validateHelmChart(
                        TEST_VOLCENGINE_HELM_CHART_PERMISSION_DENIED.toString(),
                        MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @Test
    void validateHelmChart_NotExist_Fail() {
        assertThrows(NotFoundException.class, () ->
                volcengineArtifactRegistryClient.validateHelmChart(
                        TEST_VOLCENGINE_HELM_CHART_TAG_NOT_FOUND.toString(),
                        MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "",  // empty credentials
            "invalid-base64",  // invalid base64
            "dGVzdA==",  // valid base64 but no colon (decodes to "test")
    })
    void validateHelmChart_InvalidCredentials_Fail(String invalidCredentials) {
        assertThrows(BadRequestException.class, () ->
                volcengineArtifactRegistryClient.validateHelmChart(
                        TEST_VOLCENGINE_HELM_CHART_WITH_TAG_1,
                        invalidCredentials));
    }

    @Test
    void validateCredential_ValidHostnameAndCredentials_Success() {
        String hostname = "test-volcengine-registry-cn-beijing.cr.volces.com";
        assertDoesNotThrow(() ->
                volcengineArtifactRegistryClient.validateCredential(hostname,
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
                volcengineArtifactRegistryClient.validateCredential(hostname,
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
                volcengineArtifactRegistryClient.validateCredential(hostname,
                                                                    MOCK_VOLCENGINE_REGISTRY_CRED));
    }

    @Test
    void validateCredential_InvalidCredentials_Fail() {
        String hostname = "test-volcengine-registry-cn-beijing.cr.volces.com";
        assertThrows(BadRequestException.class, () ->
                volcengineArtifactRegistryClient.validateCredential(hostname,
                                                                    MOCK_INVALID_VOLCENGINE_REGISTRY_CRED));
    }
}
