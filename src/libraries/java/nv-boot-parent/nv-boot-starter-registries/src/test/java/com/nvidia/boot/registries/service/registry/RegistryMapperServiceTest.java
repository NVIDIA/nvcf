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

package com.nvidia.boot.registries.service.registry;

import static com.nvidia.boot.registries.service.registry.RegistryMapperService.ARTIFACT_REGISTRY_CANARY_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.CONTAINER_REGISTRY_CANARY_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.RegistryMapperService.HELM_REGISTRY_CANARY_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_ARTIFACT_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY_PROD;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_REGISTRY_PROD;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.ValueSource;

class RegistryMapperServiceTest {

    private static RegistryMapperService registryMapperService;

    @BeforeAll
    static void beforeAll() {
        registryMapperService = new RegistryMapperService(TEST_NGC_CONTAINER_REGISTRY_PROD,
                                                          TEST_NGC_ARTIFACT_REGISTRY_PROD,
                                                          TEST_NGC_HELM_REGISTRY_PROD);
    }

    @ParameterizedTest
    @ValueSource(strings = {
            CONTAINER_REGISTRY_CANARY_HOSTNAME,
            HELM_REGISTRY_CANARY_HOSTNAME,
            ARTIFACT_REGISTRY_CANARY_HOSTNAME
    })
    void isCanaryHostname_WithCanaryHostnames_ReturnsTrue(String canaryHostname) {
        assertThat(RegistryMapperService.isCanaryHostname(canaryHostname)).isTrue();
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "nvcr.io",
            "helm.ngc.nvidia.com",
            "api.ngc.nvidia.com",
            "docker.io",
            "registry.k8s.io",
            "unknown.registry.com",
            ""
    })
    void isCanaryHostname_WithNonCanaryHostnames_ReturnsFalse(String hostname) {
        assertThat(RegistryMapperService.isCanaryHostname(hostname)).isFalse();
    }

    @ParameterizedTest
    @MethodSource("provideCanaryToNormalizedHostnameMappings")
    void toNormalizedHostname_WithCanaryHostnames_ReturnsProductionHostnames(String canaryHostname,
                                                                             String expectedNormalizedHostname) {
        assertThat(registryMapperService.toNormalizedHostname(canaryHostname)).isEqualTo(
                expectedNormalizedHostname);
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "nvcr.io",
            "helm.ngc.nvidia.com",
            "api.ngc.nvidia.com",
            "docker.io",
            "registry.k8s.io",
            "unknown.registry.com"
    })
    void toNormalizedHostname_WithNonCanaryHostnames_ReturnsSameHostname(String hostname) {
        assertThat(registryMapperService.toNormalizedHostname(hostname)).isEqualTo(hostname);
    }

    @Test
    void toNormalizedHostname_WithEmptyString_ReturnsEmptyString() {
        assertThat(registryMapperService.toNormalizedHostname("")).isEqualTo("");
    }

    @ParameterizedTest
    @MethodSource("provideNormalizedToCanaryHostnameMappings")
    void toCanaryHostname_WithProductionHostnames_ReturnsCanaryHostnames(String productionHostname,
                                                                         String expectedCanaryHostname) {
        assertThat(registryMapperService.toCanaryHostname(productionHostname)).isEqualTo(
                expectedCanaryHostname);
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "docker.io",
            "registry.k8s.io",
            "unknown.registry.com",
            "custom.registry.io"
    })
    void toCanaryHostname_WithUnknownHostnames_ReturnsSameHostname(String hostname) {
        assertThat(registryMapperService.toCanaryHostname(hostname)).isEqualTo(hostname);
    }

    @Test
    void toCanaryHostname_WithEmptyString_ReturnsEmptyString() {
        assertThat(registryMapperService.toCanaryHostname("")).isEmpty();
    }

    @Test
    void isCanaryHostname_WithNull_ReturnsFalse() {
        assertThat(RegistryMapperService.isCanaryHostname(null)).isFalse();
    }

    @Test
    void toNormalizedHostname_WithNull_ReturnsNull() {
        assertThat(registryMapperService.toNormalizedHostname(null)).isNull();
    }

    @Test
    void toCanaryHostname_WithNull_ReturnsNull() {
        assertThat(registryMapperService.toCanaryHostname(null)).isNull();
    }

    @ParameterizedTest
    @MethodSource("provideEcrPrivateHostnames")
    void toNormalizedRecognizedRegistryHostname_WithEcrPrivateHostnames_ReturnsGlobalHostname(String ecrHostname) {
        String result = registryMapperService.toNormalizedRecognizedRegistryHostname(ecrHostname);
        assertThat(result).isEqualTo(ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME);
    }

    private static Stream<String> provideEcrPrivateHostnames() {
        return Stream.of(
                // Standard AWS regions
                "123456789012.dkr.ecr.us-east-1.amazonaws.com",
                "123456789012.dkr.ecr.us-west-2.amazonaws.com",
                "123456789012.dkr.ecr.eu-west-1.amazonaws.com",
                "123456789012.dkr.ecr.eu-central-1.amazonaws.com",
                "123456789012.dkr.ecr.ap-southeast-1.amazonaws.com",
                "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com",
                "123456789012.dkr.ecr.ca-central-1.amazonaws.com",
                "123456789012.dkr.ecr.sa-east-1.amazonaws.com",
                "987654321098.dkr.ecr.ap-south-1.amazonaws.com",
                // AWS GovCloud regions
                "123456789012.dkr.ecr.us-gov-east-1.amazonaws.com",
                "123456789012.dkr.ecr.us-gov-west-1.amazonaws.com"
        );
    }

    @ParameterizedTest
    @MethodSource("provideVolcengineHostnames")
    void toNormalizedRecognizedRegistryHostname_WithVolcengineHostnames_ReturnsGlobalHostname(String volcengineHostname) {
        String result = registryMapperService.toNormalizedRecognizedRegistryHostname(volcengineHostname);
        assertThat(result).isEqualTo(VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME);
    }

    private static Stream<String> provideVolcengineHostnames() {
        return Stream.of(
                // Valid VolcEngine regions
                "test-registry-cn-beijing.cr.volces.com",
                "test-registry-cn-shanghai.cr.volces.com",
                "test-registry-cn-guangzhou.cr.volces.com",
                "test-registry-cn-hangzhou.cr.volces.com",
                "test-registry-cn-hongkong.cr.volces.com",
                "test-registry-ap-southeast-1.cr.volces.com",
                "test-registry-ap-southeast-3.cr.volces.com",
                // With alphanumeric and hyphens in registry name
                "my-registry-123-cn-beijing.cr.volces.com"
        );
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "ghcr.io",
            "quay.io",
            "unknown.registry.com",
            "123456789012.dkr.ecr.invalid-region.amazonaws.com", // Invalid ECR region
            "12345.dkr.ecr.us-east-1.amazonaws.com", // Invalid account ID (not 12 digits)
            "test-registry-invalid-region.cr.volces.com" // Invalid VolcEngine region
    })
    void toNormalizedRecognizedRegistryHostname_WithUnknownHostnames_ReturnsSameHostname(String hostname) {
        String result = registryMapperService.toNormalizedRecognizedRegistryHostname(hostname);
        assertThat(result).isEqualTo(hostname);
    }

    @ParameterizedTest
    @MethodSource("provideCanaryToRecognizedRegistryHostnameMappings")
    void toNormalizedRecognizedRegistryHostname_WithCanaryHostnames_ReturnsProductionHostnames(
            String canaryHostname,
            String expectedNormalizedHostname) {
        assertThat(registryMapperService.toNormalizedRecognizedRegistryHostname(canaryHostname))
                .isEqualTo(expectedNormalizedHostname);
    }

    private static Stream<Arguments> provideCanaryToRecognizedRegistryHostnameMappings() {
        return Stream.of(
                Arguments.of(CONTAINER_REGISTRY_CANARY_HOSTNAME, TEST_NGC_CONTAINER_REGISTRY_PROD),
                Arguments.of(HELM_REGISTRY_CANARY_HOSTNAME, TEST_NGC_HELM_REGISTRY_PROD),
                Arguments.of(ARTIFACT_REGISTRY_CANARY_HOSTNAME, TEST_NGC_ARTIFACT_REGISTRY_PROD));
    }

    private static Stream<Arguments> provideCanaryToNormalizedHostnameMappings() {
        return Stream.of(
                Arguments.of(CONTAINER_REGISTRY_CANARY_HOSTNAME, TEST_NGC_CONTAINER_REGISTRY_PROD),
                Arguments.of(HELM_REGISTRY_CANARY_HOSTNAME, TEST_NGC_HELM_REGISTRY_PROD),
                Arguments.of(ARTIFACT_REGISTRY_CANARY_HOSTNAME, TEST_NGC_ARTIFACT_REGISTRY_PROD));
    }

    private static Stream<Arguments> provideNormalizedToCanaryHostnameMappings() {
        return Stream.of(
                Arguments.of(TEST_NGC_CONTAINER_REGISTRY_PROD, CONTAINER_REGISTRY_CANARY_HOSTNAME),
                Arguments.of(TEST_NGC_HELM_REGISTRY_PROD, HELM_REGISTRY_CANARY_HOSTNAME),
                Arguments.of(TEST_NGC_ARTIFACT_REGISTRY_PROD, ARTIFACT_REGISTRY_CANARY_HOSTNAME));
    }

    static Stream<Arguments> normalizeUrlTestCases() {
        return Stream.of(
                Arguments.of("https://registry.io", "https://registry.io"),
                Arguments.of("http://registry.io", "http://registry.io"),
                Arguments.of("registry.io", "https://registry.io"),
                Arguments.of("registry.io:8080", "https://registry.io:8080"),
                Arguments.of("localhost:9000", "https://localhost:9000")
        );
    }

    @ParameterizedTest
    @MethodSource("normalizeUrlTestCases")
    void normalizeUrl_ParameterizedTests(String input, String expected) {
        String result = RegistryMapperService.normalizeUrl(input);
        assertEquals(expected, result);
    }

    static Stream<Arguments> toBaseAuthUrlTestCases() {
        return Stream.of(
                Arguments.of("https://auth.registry.io", "registry.io", "https://auth.registry.io"),
                Arguments.of("", "registry.io", "https://registry.io"),
                Arguments.of(null, "registry.io", "https://registry.io"),
                Arguments.of("   ", "registry.io", "https://registry.io"),
                Arguments.of(null, "https://registry.io", "https://registry.io"),
                Arguments.of("", "http://registry.io", "http://registry.io")
        );
    }

    @ParameterizedTest
    @MethodSource("toBaseAuthUrlTestCases")
    void toBaseAuthUrl_ParameterizedTests(String authBaseUrl, String registryHost,
                                          String expected) {
        String result = RegistryMapperService.toBaseAuthUrl(authBaseUrl, registryHost);
        assertEquals(expected, result);
    }


    @Test
    void toRegistryBaseUrl_SingleParam_WithEmptyString_ThrowsException() {
        assertThrows(IllegalArgumentException.class, () -> {
            RegistryMapperService.toRegistryBaseUrl("");
        });
    }

    @Test
    void toRegistryBaseUrl_SingleParam_WithNull_ThrowsException() {
        assertThrows(IllegalArgumentException.class, () -> {
            RegistryMapperService.toRegistryBaseUrl((String) null);
        });
    }

    static Stream<Arguments> toRegistryBaseUrlSingleParamTestCases() {
        return Stream.of(
                // Normal URLs
                Arguments.of("https://registry.io", "https://registry.io"),

                // Localhost patterns
                Arguments.of("localhost-ngc:9100", "localhost:9100"),
                Arguments.of("http://localhost-docker:9105", "http://localhost:9105"),
                Arguments.of("localhost-acr:9200", "localhost:9200"),
                Arguments.of("localhost-test-123:8080", "localhost:8080"),
                Arguments.of("https://localhost-acr:9200", "https://localhost:9200"),
                Arguments.of("localhost-ngc:9100/path/localhost-docker:9105",
                             "localhost:9100/path/localhost:9105"),
                Arguments.of("localhost:9100", "localhost:9100"),
                Arguments.of("https://localhost-registry:8080/api/localhost/test",
                             "https://localhost:8080/api/localhost/test")
        );
    }

    @ParameterizedTest
    @MethodSource("toRegistryBaseUrlSingleParamTestCases")
    void toRegistryBaseUrl_SingleParam_ParameterizedTests(String input, String expected) {
        String result = RegistryMapperService.toRegistryBaseUrl(input);
        assertEquals(expected, result);
    }

    static Stream<Arguments> toRegistryBaseUrlTwoParamTestCases() {
        return Stream.of(
                Arguments.of("registry.io", "https://prod-registry.com", "https://registry.io"),
                Arguments.of("registry.io", "", "https://registry.io"),
                Arguments.of("registry.io", null, "https://registry.io")
        );
    }

    @ParameterizedTest
    @MethodSource("toRegistryBaseUrlTwoParamTestCases")
    void toRegistryBaseUrl_TwoParam_ParameterizedTests(String registryHost,
                                                       String overrideBaseUrl,
                                                       String expected) {
        String result = RegistryMapperService.toRegistryBaseUrl(registryHost, overrideBaseUrl);
        assertEquals(expected, result);
    }

    static Stream<Arguments> localhostUrlFormats() {
        return Stream.of(
                Arguments.of("localhost-ngc:9100", "localhost:9100"),
                Arguments.of("http://localhost-docker:9105", "http://localhost:9105"),
                Arguments.of("localhost-acr:9200", "localhost:9200"),
                Arguments.of("localhost-test-123:8080", "localhost:8080")
        );
    }

    @ParameterizedTest
    @MethodSource("localhostUrlFormats")
    void toRegistryBaseUrl_ParameterizedLocalhostTests(String input, String expected) {
        String result = RegistryMapperService.toRegistryBaseUrl("registry.io", input);
        assertEquals(expected, result);
    }
}
