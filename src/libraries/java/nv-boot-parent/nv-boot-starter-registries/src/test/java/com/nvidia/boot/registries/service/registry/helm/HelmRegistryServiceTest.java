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

package com.nvidia.boot.registries.service.registry.helm;

import com.nvidia.boot.registries.service.registry.client.WebClientUtils;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ACR_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ARTIFACTORY_REGISTRY_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_AZURE_REGISTRY_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_CONTAINER_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_REGISTRY_OAUTH2_GROUP_SCOPE;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_REGISTRY_OAUTH2_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_DOCKER_REGISTRY_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_PUBLIC_REGISTRY_API_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_PUBLIC_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_PUBLIC_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_REGISTRY_API_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_ECR_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_CREDENTIALS;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_REGISTRY_AUTH_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_HARBOR_REGISTRY_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_INVALID_ECR_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_HELM_REGISTRY_CRED;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_BASE_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_OAUTH2_BASE_URL;
import static com.nvidia.boot.registries.util.TestConstants.MOCK_NGC_REGISTRY_OAUTH2_GROUP_SCOPE;
import static com.nvidia.boot.registries.util.TestConstants.TEST_AZURE_REGISTRY_GLOBAL_HOST_NAME;
import static com.nvidia.boot.registries.util.TestConstants.TEST_DOCKER_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_ECR_PRIVATE_REGISTRY_HOST_NAME;
import static com.nvidia.boot.registries.util.TestConstants.TEST_ECR_PUBLIC_REGISTRY_HOST_NAME;
import static com.nvidia.boot.registries.util.TestConstants.TEST_ECR_REGISTRY_GLOBAL_HOST_NAME;
import static com.nvidia.boot.registries.util.TestConstants.TEST_HELM_CHART_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.registries.util.TestConstants.TEST_HELM_CHART_UNKNOWN_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_HELM_REGISTRY_HOST_NAME_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_ARTIFACT_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_CHART;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_CHART_WITH_CANARY_HOST;
import static com.nvidia.boot.registries.util.TestConstants.TEST_NGC_HELM_REGISTRY;
import static com.nvidia.boot.registries.util.TestConstants.TEST_RECOGNIZED_HELM_REGISTRY_KEY_1;
import static com.nvidia.boot.registries.util.TestConstants.TEST_REGISTRY_CONFIG_PROPERTIES;
import static com.nvidia.boot.registries.util.TestUtils.registryConfig;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.artifactory.MockArtifactoryAuthServer;
import com.nvidia.boot.mock.azure.MockAcrAuthServer;
import com.nvidia.boot.mock.docker.MockDockerRegistryAuthServer;
import com.nvidia.boot.mock.docker.MockDockerRegistryServer;
import com.nvidia.boot.mock.ecr.MockEcrPrivateRegistryServer;
import com.nvidia.boot.mock.ecr.MockEcrPublicRegistryServer;
import com.nvidia.boot.mock.harbor.MockHarborAuthServer;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.oci.MockOciRegistryServer;
import com.nvidia.boot.registries.configurations.RegistryConfigurationProperties.RecognizedRegistryConfiguration;
import com.nvidia.boot.registries.service.registry.RegistryLookupService;
import com.nvidia.boot.registries.service.registry.RegistryMapperService;
import com.nvidia.boot.registries.service.registry.RegistryValidationService;
import com.nvidia.boot.registries.service.registry.client.acr.AzureRegistryClient;
import com.nvidia.boot.registries.service.registry.client.artifactory.ArtifactoryClient;
import com.nvidia.boot.registries.service.registry.client.docker.DockerRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ecr.pub.EcrPublicArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ecr.pvt.EcrPrivateArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.client.harbor.HarborRegistryClient;
import com.nvidia.boot.registries.service.registry.client.ngc.NgcArtifactRegistryClient;
import com.nvidia.boot.registries.service.registry.helm.acr.AcrHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.artifactory.ArtifactoryHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.docker.DockerHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.ecr.pub.EcrPublicHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.ecr.pvt.EcrPrivateHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.harbor.HarborHelmRegistry;
import com.nvidia.boot.registries.service.registry.helm.ngc.NgcHelmRegistry;
import java.net.URI;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.stream.Stream;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.web.reactive.function.client.WebClient;

class HelmRegistryServiceTest {

    private static HelmRegistryService helmRegistryService;
    private static MockOciRegistryServer mockAcrHelmRegistryServer;
    private static MockOciRegistryServer mockHarborHelmRegistryServer;
    private static MockOciRegistryServer mockArtifactoryHelmRegistryServer;

    @BeforeAll
    static void beforeAll() {
        MockCasServer.start(MOCK_NGC_REGISTRY_OAUTH2_BASE_URL, MOCK_NGC_REGISTRY_BASE_URL);
        MockDockerRegistryServer.start(MOCK_DOCKER_REGISTRY_URL);
        MockDockerRegistryAuthServer.start(MOCK_DOCKER_REGISTRY_OAUTH2_URL);
        MockEcrPrivateRegistryServer.start(MOCK_ECR_REGISTRY_API_URL);
        MockEcrPublicRegistryServer.start(MOCK_ECR_PUBLIC_REGISTRY_API_URL);
        MockAcrAuthServer.start(MOCK_AZURE_REGISTRY_AUTH_URL);
        mockAcrHelmRegistryServer = new MockOciRegistryServer();
        mockAcrHelmRegistryServer.start(MOCK_AZURE_REGISTRY_URL);
        MockHarborAuthServer.start(MOCK_HARBOR_REGISTRY_AUTH_URL);
        mockHarborHelmRegistryServer = new MockOciRegistryServer();
        mockHarborHelmRegistryServer.start(MOCK_HARBOR_REGISTRY_URL);
        MockArtifactoryAuthServer.start(MOCK_ARTIFACTORY_REGISTRY_AUTH_URL);
        mockArtifactoryHelmRegistryServer = new MockOciRegistryServer();
        mockArtifactoryHelmRegistryServer.start(MOCK_ARTIFACTORY_REGISTRY_URL);

        var ngcArtifactRegistryClient = new NgcArtifactRegistryClient(
                WebClientUtils.builder(),
                MOCK_NGC_REGISTRY_BASE_URL,
                MOCK_NGC_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_READ_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_WRITE_TIMEOUT,
                MOCK_NGC_REGISTRY_CLIENT_CONNECT_TIMEOUT,
                MOCK_NGC_REGISTRY_OAUTH2_BASE_URL,
                MOCK_NGC_REGISTRY_OAUTH2_GROUP_SCOPE);
        // manually set the host name for test so it's not localhost
        ngcArtifactRegistryClient.setHostname(TEST_NGC_HELM_REGISTRY);

        var ecrArtifactRegistryClient = new EcrPrivateArtifactRegistryClient(
                WebClientUtils.builder(),
                MOCK_ECR_REGISTRY_API_URL,
                MOCK_ECR_REGISTRY_CLIENT_CALL_TIMEOUT);

        EcrPublicArtifactRegistryClient ecrPublicArtifactRegistryClient =
                new EcrPublicArtifactRegistryClient(
                        WebClientUtils.builder(),
                        MOCK_ECR_PUBLIC_REGISTRY_API_URL,
                        MOCK_ECR_PUBLIC_REGISTRY_CLIENT_CALL_TIMEOUT);

        var dockerRegistryClient = new DockerRegistryClient(
                WebClientUtils.builder(),
                MOCK_DOCKER_REGISTRY_URL,
                MOCK_DOCKER_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_DOCKER_REGISTRY_OAUTH2_URL,
                MOCK_DOCKER_REGISTRY_OAUTH2_GROUP_SCOPE);
        dockerRegistryClient.setHostname(TEST_DOCKER_REGISTRY);

        var azureRegistryClient = new AzureRegistryClient(
                WebClientUtils.builder(),
                MOCK_AZURE_REGISTRY_URL,
                MOCK_AZURE_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_AZURE_REGISTRY_AUTH_URL);

        var harborRegistryClient = new HarborRegistryClient(
                WebClientUtils.builder(),
                MOCK_HARBOR_REGISTRY_URL,
                MOCK_HARBOR_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_HARBOR_REGISTRY_AUTH_URL);

        var artifactoryClient = new ArtifactoryClient(
                WebClientUtils.builder(),
                MOCK_ARTIFACTORY_REGISTRY_URL,
                MOCK_ARTIFACTORY_REGISTRY_CLIENT_CALL_TIMEOUT,
                MOCK_ARTIFACTORY_REGISTRY_AUTH_URL);

        var ngcHelmRegistry = new NgcHelmRegistry(ngcArtifactRegistryClient);
        var dockerHelmRegistry = new DockerHelmRegistry(dockerRegistryClient);
        var ecrHelmRegistry = new EcrPrivateHelmRegistry(ecrArtifactRegistryClient);
        var ecrPublicHelmRegistry = new EcrPublicHelmRegistry(ecrPublicArtifactRegistryClient);
        var acrHelmRegistry = new AcrHelmRegistry(azureRegistryClient);
        var harborHelmRegistry = new HarborHelmRegistry(harborRegistryClient);
        var artifactoryHelmRegistry = new ArtifactoryHelmRegistry(artifactoryClient);
        var registryMapperService = new RegistryMapperService(TEST_NGC_CONTAINER_REGISTRY,
                                                              TEST_NGC_ARTIFACT_REGISTRY,
                                                              TEST_NGC_HELM_REGISTRY);
        var registryLookupService = new RegistryLookupService(
                Collections.emptyList(), Collections.emptyList(),
                Collections.emptyList(), Collections.emptyList(),
                TEST_REGISTRY_CONFIG_PROPERTIES.getRecognized(), registryMapperService);
        var registryValidationService = new RegistryValidationService(registryLookupService);
        helmRegistryService = new HelmRegistryService(
                List.of(ngcHelmRegistry,
                        dockerHelmRegistry,
                        ecrHelmRegistry,
                        ecrPublicHelmRegistry,
                        acrHelmRegistry,
                        harborHelmRegistry,
                        artifactoryHelmRegistry),
                registryMapperService,
                registryValidationService);
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(MOCK_ECR_REGISTRY_API_URL).getHost(),
                TEST_ECR_REGISTRY_GLOBAL_HOST_NAME);
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(MOCK_ECR_PUBLIC_REGISTRY_API_URL).getHost(),
                TEST_ECR_PUBLIC_REGISTRY_HOST_NAME);
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(MOCK_AZURE_REGISTRY_URL).getHost(),
                TEST_AZURE_REGISTRY_GLOBAL_HOST_NAME);
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(MOCK_HARBOR_REGISTRY_URL).getHost(),
                TEST_HARBOR_REGISTRY);
        helmRegistryService.overwriteRegistryHostnameMap(
                URI.create(MOCK_ARTIFACTORY_REGISTRY_URL).getHost(),
                TEST_ARTIFACTORY_REGISTRY);
    }

    @AfterAll
    static void cleanup() {
        MockCasServer.stop();
        MockEcrPrivateRegistryServer.stop();
        MockEcrPublicRegistryServer.stop();
        MockAcrAuthServer.stop();
        mockAcrHelmRegistryServer.stop();
        MockHarborAuthServer.stop();
        mockHarborHelmRegistryServer.stop();
        MockArtifactoryAuthServer.stop();
        mockArtifactoryHelmRegistryServer.stop();
    }

    static Stream<Arguments> getValidHelmRegistry() {
        return Stream.of(
                // ngc
                Arguments.of(TEST_NGC_HELM_CHART.toString(), MOCK_NGC_HELM_REGISTRY_CRED),
                Arguments.of(TEST_NGC_HELM_CHART_WITH_CANARY_HOST.toString(),
                             MOCK_NGC_HELM_REGISTRY_CRED),

                // docker
                Arguments.of("oci://" + TEST_DOCKER_CONTAINER_IMAGE,
                             MOCK_DOCKER_CONTAINER_REGISTRY_CRED),
                Arguments.of("oci://" + TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST,
                             MOCK_DOCKER_CONTAINER_REGISTRY_CRED),

                // ecr
                Arguments.of(TEST_ECR_HELM_CHART_WITH_TAG.toString(),
                             MOCK_ECR_REGISTRY_CRED),
                Arguments.of(TEST_ECR_HELM_CHART_WITH_DIGEST.toString(),
                             MOCK_ECR_REGISTRY_CRED),

                // ecr public
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG.toString(),
                             MOCK_ECR_PUBLIC_REGISTRY_CRED),
                Arguments.of(TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST.toString(),
                             MOCK_ECR_PUBLIC_REGISTRY_CRED),

                // acr
                Arguments.of(TEST_ACR_HELM_CHART_WITH_TAG.toString(),
                             MOCK_ACR_CREDENTIALS),
                Arguments.of(TEST_ACR_HELM_CHART_WITH_DIGEST.toString(),
                             MOCK_ACR_CREDENTIALS),

                // harbor
                Arguments.of(TEST_HARBOR_HELM_CHART_WITH_TAG.toString(),
                             MOCK_HARBOR_CREDENTIALS),
                Arguments.of(TEST_HARBOR_HELM_CHART_WITH_DIGEST.toString(),
                             MOCK_HARBOR_CREDENTIALS),

                // artifactory
                Arguments.of(TEST_ARTIFACTORY_HELM_CHART_WITH_TAG.toString(),
                             MOCK_ARTIFACTORY_CREDENTIALS),
                Arguments.of(TEST_ARTIFACTORY_HELM_CHART_WITH_DIGEST.toString(),
                             MOCK_ARTIFACTORY_CREDENTIALS));
    }

    @ParameterizedTest
    @MethodSource("getValidHelmRegistry")
    void validateArtifact_Success(String helmUrl, String apiKey) {
        helmRegistryService.validateArtifact(helmUrl, List.of(apiKey));
    }

    static Stream<Arguments> getInvalidHelmRegistry() {
        return Stream.of(
                Arguments.of(TEST_NGC_HELM_CHART_PERMISSION_DENIED.toString(),
                             List.of(MOCK_NGC_HELM_REGISTRY_CRED),
                             ForbiddenException.class),
                Arguments.of(TEST_HELM_CHART_UNKNOWN_REGISTRY.toString(),
                             List.of(MOCK_NGC_HELM_REGISTRY_CRED),
                             BadRequestException.class),
                Arguments.of(TEST_HELM_CHART_NOT_EXISTS.toString(),
                             List.of(MOCK_NGC_HELM_REGISTRY_CRED),
                             NotFoundException.class),
                Arguments.of(TEST_NGC_HELM_CHART.toString(),
                             List.of(),
                             IllegalStateException.class),
                // ecr
                Arguments.of(TEST_ECR_HELM_CHART_TAG_NOT_FOUND.toString(),
                             List.of(MOCK_ECR_REGISTRY_CRED),
                             BadRequestException.class),
                Arguments.of(TEST_ECR_HELM_CHART_DIGEST_NOT_FOUND.toString(),
                             List.of(MOCK_ECR_REGISTRY_CRED),
                             BadRequestException.class),

                // docker
                // docker container image and helm chart urls are same structure
                Arguments.of("oci://" + TEST_DOCKER_CONTAINER_IMAGE_NOT_EXISTS,
                             List.of(MOCK_DOCKER_CONTAINER_REGISTRY_CRED),
                             NotFoundException.class),
                Arguments.of("oci://" + TEST_DOCKER_CONTAINER_IMAGE_PERMISSION_DENIED,
                             List.of(MOCK_DOCKER_CONTAINER_REGISTRY_CRED),
                             ForbiddenException.class),

                // acr
                Arguments.of(TEST_ACR_HELM_CHART_PERMISSION_DENIED.toString(),
                             List.of(MOCK_ACR_CREDENTIALS),
                             ForbiddenException.class),
                Arguments.of(TEST_ACR_HELM_CHART_NOT_EXISTS.toString(),
                             List.of(MOCK_ACR_CREDENTIALS),
                             NotFoundException.class),

                // harbor
                Arguments.of(TEST_HARBOR_HELM_CHART_PERMISSION_DENIED.toString(),
                             List.of(MOCK_HARBOR_CREDENTIALS),
                             ForbiddenException.class),
                Arguments.of(TEST_HARBOR_HELM_CHART_NOT_EXISTS.toString(),
                             List.of(MOCK_HARBOR_CREDENTIALS),
                             NotFoundException.class),

                // artifactory
                Arguments.of(TEST_ARTIFACTORY_HELM_CHART_PERMISSION_DENIED.toString(),
                             List.of(MOCK_ARTIFACTORY_CREDENTIALS),
                             ForbiddenException.class),
                Arguments.of(TEST_ARTIFACTORY_HELM_CHART_NOT_EXISTS.toString(),
                             List.of(MOCK_ARTIFACTORY_CREDENTIALS),
                             NotFoundException.class));
    }

    @ParameterizedTest
    @MethodSource("getInvalidHelmRegistry")
    void validateArtifact_Fail(String helmUrl,
                               List<String> apiKeys,
                               Class<Throwable> expectedException) {
        assertThrows(expectedException,
                     () -> helmRegistryService.validateArtifact(helmUrl, apiKeys));
    }

    static Stream<Arguments> getValidCredentialsForHelmRegistry() {
        return Stream.of(
                // ECR private
                Arguments.of(TEST_ECR_PRIVATE_REGISTRY_HOST_NAME,
                             List.of(MOCK_ECR_REGISTRY_CRED)),
                // ECR public
                Arguments.of(TEST_ECR_PUBLIC_REGISTRY_HOST_NAME,
                             List.of(MOCK_ECR_PUBLIC_REGISTRY_CRED)),
                // Multiple credentials with one valid
                Arguments.of(TEST_ECR_PRIVATE_REGISTRY_HOST_NAME,
                             List.of("invalid-cred", MOCK_ECR_REGISTRY_CRED)));
    }

    @ParameterizedTest
    @MethodSource("getValidCredentialsForHelmRegistry")
    void validateCredentials_Success(String hostname, List<String> credentials) {
        assertDoesNotThrow(() -> helmRegistryService.validateCredentials(hostname, credentials));
    }

    static Stream<Arguments> getInvalidCredentialsForHelmRegistry() {
        return Stream.of(
                // Invalid credentials
                Arguments.of(TEST_ECR_PRIVATE_REGISTRY_HOST_NAME,
                             List.of(MOCK_INVALID_ECR_REGISTRY_CRED),
                             BadRequestException.class),
                // Empty credentials list
                Arguments.of(TEST_ECR_PRIVATE_REGISTRY_HOST_NAME,
                             List.of(),
                             IllegalStateException.class),
                // Unknown hostname
                Arguments.of(TEST_HELM_CHART_UNKNOWN_REGISTRY.toString(),
                             List.of(MOCK_ECR_REGISTRY_CRED),
                             BadRequestException.class));
    }

    @ParameterizedTest
    @MethodSource("getInvalidCredentialsForHelmRegistry")
    void validateCredentials_Fail(String hostname,
                                  List<String> credentials,
                                  Class<Throwable> expectedException) {
        assertThrows(expectedException,
                     () -> helmRegistryService.validateCredentials(hostname, credentials));
    }

    @Test
    void validateArtifact_ArtifactValidationDisabled_ReturnsWithoutValidating() {
        var service = createServiceWithValidationDisabled(true, false);

        assertDoesNotThrow(() -> service.validateArtifact(
                TEST_HELM_CHART_1.toString(), List.of("dummy-cred")));
    }

    @Test
    void validateCredentials_CredentialValidationDisabled_ReturnsWithoutValidating() {
        var service = createServiceWithValidationDisabled(false, true);

        assertDoesNotThrow(() -> service.validateCredentials(
                TEST_HELM_REGISTRY_HOST_NAME_1, List.of("dummy-cred")));
    }

    private static HelmRegistryService createServiceWithValidationDisabled(
            boolean credentialValidation, boolean artifactValidation) {
        var recognizedRegistryConfig = new RecognizedRegistryConfiguration();
        recognizedRegistryConfig.setHelm(Map.of(
                TEST_RECOGNIZED_HELM_REGISTRY_KEY_1,
                registryConfig(TEST_HELM_REGISTRY_HOST_NAME_1, credentialValidation,
                               artifactValidation)));

        var mapperService = new RegistryMapperService(
                TEST_NGC_CONTAINER_REGISTRY,
                TEST_NGC_ARTIFACT_REGISTRY,
                TEST_NGC_HELM_REGISTRY);
        var lookupService = new RegistryLookupService(
                Collections.emptyList(), Collections.emptyList(),
                Collections.emptyList(), Collections.emptyList(),
                recognizedRegistryConfig, mapperService);
        var validationService = new RegistryValidationService(lookupService);
        return new HelmRegistryService(List.of(), mapperService, validationService);
    }
}
