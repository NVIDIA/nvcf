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
package com.nvidia.nvcf.rest.registry;

import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_CONTAINER_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_CONTAINER_REGISTRY;
import static com.nvidia.boot.registries.service.registry.client.acr.AzureRegistryClient.AZURE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PUBLIC_REGISTRY_HOSTNAME;
import static com.nvidia.boot.registries.service.registry.client.volcengine.VolcengineRegistryUtils.VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ACR_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ARTIFACTORY_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.DOCKER_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PRIVATE_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.ECR_PUBLIC_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.HARBOR_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.NGC_PRIVATE_REGISTRY_NAME;
import static com.nvidia.boot.registries.util.RegistriesConstants.VOLCENGINE_REGISTRY_NAME;
import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_MANAGE_REGISTRY_CREDENTIALS;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACR_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACR_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_ARTIFACTORY_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_ARTIFACTORY_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_ARTIFACTORY_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CUSTOM_REGISTRY_DISPLAY_NAME_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_CUSTOM_REGISTRY_HOST_NAME_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_HUB_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_HUB_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_ECR_PRIVATE_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_ECR_PRIVATE_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_ECR_PUBLIC_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_ECR_PUBLIC_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_HARBOR_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_HARBOR_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_HARBOR_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_INVALID_ECR_PRIVATE_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_INVALID_ECR_PUBLIC_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_INVALID_VOLCENGINE_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_LEGACY_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_LEGACY_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_SECRET_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_MODEL_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VOLCENGINE_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VOLCENGINE_SECRET_2;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static java.nio.charset.StandardCharsets.UTF_8;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.springframework.http.HttpStatus.BAD_REQUEST;
import static org.springframework.http.HttpStatus.CONFLICT;
import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.NOT_FOUND;
import static org.springframework.http.HttpStatus.NO_CONTENT;
import static org.springframework.http.HttpStatus.OK;
import static org.springframework.http.HttpStatus.UNAUTHORIZED;

import com.google.common.collect.Sets;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.artifactory.MockArtifactoryAuthServer;
import com.nvidia.boot.mock.azure.MockAcrAuthServer;
import com.nvidia.boot.mock.docker.MockDockerRegistryAuthServer;
import com.nvidia.boot.mock.ecr.MockEcrPrivateRegistryServer;
import com.nvidia.boot.mock.ecr.MockEcrPublicRegistryServer;
import com.nvidia.boot.mock.harbor.MockHarborAuthServer;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.boot.mock.volcengine.MockVolcengineRegistryServer;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.registry.dto.AddRegistryCredentialRequest;
import com.nvidia.nvcf.rest.registry.dto.ListRegistryCredentialDetailsResponse;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.rest.registry.dto.RecognizedRegistriesResponse;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDetailsResponse;
import com.nvidia.nvcf.rest.registry.dto.UpdateRegistryCredentialRequest;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.registry.RegistryCredentialLookupService;
import com.nvidia.nvcf.service.registry.RegistryCredentialService;
import com.nvidia.nvcf.service.registry.RegistryFunctionMapperService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.TestConstants;
import java.net.URI;
import java.util.Arrays;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.logging.log4j.util.Strings;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.util.CollectionUtils;
import tools.jackson.databind.node.StringNode;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class RegistryCredentialManagementControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestRegistryCredentialService testRegistryCredentialService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private RegistryCredentialService registryCredentialService;

    @Autowired
    private RegistryCredentialLookupService registryCredentialLookupService;

    @Autowired
    private RegistryFunctionMapperService registryFunctionMapperService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @Value("${nvcf.registries.recognized.container.docker.oauth2.base-url}")
    private String dockerAuthBaseUrl;

    @Value("${nvcf.registries.recognized.container.ecr.hostname}")
    private String ecrPrivateBaseUrl;

    @Value("${nvcf.registries.recognized.container.ecr-public.hostname}")
    private String ecrPublicBaseUrl;

    @Value("${nvcf.registries.recognized.container.volcengine.hostname}")
    private String volcengineBaseUrl;

    @Value("${nvcf.registries.recognized.container.acr.oauth2.base-url}")
    private String azureAuthBaseUrl;

    @Value("${nvcf.registries.recognized.container.harbor.oauth2.base-url}")
    private String harborAuthBaseUrl;

    @Value("${nvcf.registries.recognized.container.artifactory.oauth2.base-url}")
    private String artifactoryAuthBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());

        MockEssServer.start(essBaseUrl);
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        MockDockerRegistryAuthServer.start(dockerAuthBaseUrl);
        MockEcrPrivateRegistryServer.start(ecrPrivateBaseUrl);
        MockEcrPublicRegistryServer.start(ecrPublicBaseUrl);
        MockVolcengineRegistryServer.start(volcengineBaseUrl);
        MockAcrAuthServer.start(azureAuthBaseUrl);
        MockHarborAuthServer.start(harborAuthBaseUrl);
        MockArtifactoryAuthServer.start(artifactoryAuthBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockEssServer.stop();
        MockApiKeysServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockDockerRegistryAuthServer.stop();
        MockEcrPrivateRegistryServer.stop();
        MockEcrPublicRegistryServer.stop();
        MockVolcengineRegistryServer.stop();
        MockAcrAuthServer.stop();
        MockHarborAuthServer.stop();
        MockArtifactoryAuthServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testAccountService.cleanupAccountsClientsAndRegistries();
        resetToDefault();
    }

    Stream<Arguments> getListRegistryCredentialsArgs() {
        var jwtCases = Stream.of(
                Arguments.of(null, null, UNAUTHORIZED, -1),
                Arguments.of("invalid-key", null, UNAUTHORIZED, -1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(), 100),
                             null,
                             FORBIDDEN, -1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             null,
                             FORBIDDEN, -1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             null,
                             OK, 3),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "container",
                             OK, 1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "helm",
                             OK, 1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "resource",
                             OK, 1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "model",
                             OK, 1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "model,resource",
                             OK, 1)
        );

        var apiKeyCases = Stream.of(
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             OK, 3),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             OK, 3),
                // apikey with missing scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             FORBIDDEN, -1),
                // apikey with incorrect scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of("list_functions"));
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             FORBIDDEN, -1),
                // apikey with non-existent NCA ID should return empty list
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-nca-id", TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             OK, 0)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("getListRegistryCredentialsArgs")
    void shouldListRegistryCredentials(
            Object tokenSupplier,
            String artifactTypes,
            HttpStatus expectedStatus,
            int expectedSize) {
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = getToken(tokenSupplier);

        var builder = Strings.isBlank(artifactTypes) ?
                RequestEntity.get(URI.create("/v2/nvcf/registry-credentials")) :
                RequestEntity.get(
                        URI.create("/v2/nvcf/registry-credentials?artifactType=" + artifactTypes));

        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }

        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       ListRegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.registryCredentials()).hasSize(expectedSize);

        if (expectedSize <= 0) {
            return;
        }

        if (Strings.isBlank(artifactTypes)) {
            assertThat(responseBody.registryCredentials()).hasSize(expectedSize);
        } else {
            var numOfArtifactTypes = 0;
            if (artifactTypes.contains("container")) {
                numOfArtifactTypes++;
            }
            if (artifactTypes.contains("helm")) {
                numOfArtifactTypes++;
            }
            if (artifactTypes.contains("model") || artifactTypes.contains("resource")) {
                numOfArtifactTypes++;
            }
            assertThat(responseBody.registryCredentials()).hasSize(numOfArtifactTypes);
        }

        if ((Strings.isBlank(artifactTypes)
                || Strings.isNotBlank(artifactTypes) && artifactTypes.contains("container"))) {
            var containerRegistryCredential = responseBody.registryCredentials()
                    .stream()
                    .filter(rc -> rc.artifactTypes().contains(ArtifactTypeEnum.CONTAINER))
                    .toList().getFirst();
            assertThat(containerRegistryCredential.ncaId()).isEqualTo(TEST_NCA_ID);
            assertThat(containerRegistryCredential.registryCredentialName())
                    .isEqualTo("ngc-container-registry-credential");
            assertThat(containerRegistryCredential.registryName())
                    .isEqualTo(NGC_PRIVATE_REGISTRY_NAME);
            assertThat(containerRegistryCredential.registryHostname()).isEqualTo("stg.nvcr.io");
            assertThat(containerRegistryCredential.artifactTypes())
                    .contains(ArtifactTypeEnum.CONTAINER);
            assertThat(containerRegistryCredential.provisionedBy())
                    .isEqualTo(ProvisionedByEnum.SYSTEM);
        }

        if ((Strings.isBlank(artifactTypes)
                || Strings.isNotBlank(artifactTypes) && artifactTypes.contains("helm"))) {
            var helmRegistryCredential = responseBody.registryCredentials()
                    .stream()
                    .filter(rc -> rc.artifactTypes().contains(ArtifactTypeEnum.HELM))
                    .toList().getFirst();
            assertThat(helmRegistryCredential.ncaId()).isEqualTo("test-nca-id");
            assertThat(helmRegistryCredential.registryCredentialName())
                    .isEqualTo("ngc-helm-registry-credential");
            assertThat(helmRegistryCredential.registryName())
                    .isEqualTo(NGC_PRIVATE_REGISTRY_NAME);
            assertThat(helmRegistryCredential.registryHostname())
                    .isEqualTo("helm.stg.ngc.nvidia.com");
            assertThat(helmRegistryCredential.artifactTypes()).contains(ArtifactTypeEnum.HELM);
            assertThat(helmRegistryCredential.provisionedBy()).isEqualTo(ProvisionedByEnum.SYSTEM);
        }

        if ((Strings.isBlank(artifactTypes)
                || Strings.isNotBlank(artifactTypes) && artifactTypes.contains("model"))) {
            var modelRegistryCredential = responseBody.registryCredentials()
                    .stream()
                    .filter(rc -> rc.artifactTypes().contains(ArtifactTypeEnum.MODEL))
                    .toList().getFirst();
            assertThat(modelRegistryCredential.ncaId()).isEqualTo("test-nca-id");
            assertThat(modelRegistryCredential.registryCredentialName())
                    .isEqualTo("ngc-model-resource-registry-credential");
            assertThat(modelRegistryCredential.registryName())
                    .isEqualTo(NGC_PRIVATE_REGISTRY_NAME);
            assertThat(modelRegistryCredential.registryHostname())
                    .isEqualTo("api.stg.ngc.nvidia.com");
            assertThat(modelRegistryCredential.artifactTypes()).contains(ArtifactTypeEnum.MODEL);
            assertThat(modelRegistryCredential.provisionedBy())
                    .isEqualTo(ProvisionedByEnum.SYSTEM);
        }

        if ((Strings.isBlank(artifactTypes)
                || Strings.isNotBlank(artifactTypes) && artifactTypes.contains("resource"))) {
            var resourceRegistryCredential = responseBody.registryCredentials()
                    .stream()
                    .filter(rc -> rc.artifactTypes().contains(ArtifactTypeEnum.RESOURCE))
                    .toList().getFirst();
            assertThat(resourceRegistryCredential.ncaId()).isEqualTo("test-nca-id");
            assertThat(resourceRegistryCredential.registryCredentialName())
                    .isEqualTo("ngc-model-resource-registry-credential");
            assertThat(resourceRegistryCredential.registryName())
                    .isEqualTo(NGC_PRIVATE_REGISTRY_NAME);
            assertThat(resourceRegistryCredential.registryHostname())
                    .isEqualTo("api.stg.ngc.nvidia.com");
            assertThat(resourceRegistryCredential.artifactTypes())
                    .contains(ArtifactTypeEnum.RESOURCE);
            assertThat(resourceRegistryCredential.provisionedBy())
                    .isEqualTo(ProvisionedByEnum.SYSTEM);
        }
    }

    Stream<Arguments> getListFilteredRegistryCredentialsArgs() {
        var jwtCases = Stream.of(
                Arguments.of(null, null, null, UNAUTHORIZED, -1),
                Arguments.of("invalid-key", null, null, UNAUTHORIZED, -1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(), 100),
                             null,
                             null,
                             FORBIDDEN, -1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             null,
                             null,
                             FORBIDDEN, -1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             null,
                             null,
                             OK, 6),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "container",
                             "system",
                             OK, 1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "model",
                             "user",
                             OK, 1),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "resource",
                             "system,user",
                             OK, 2),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "model",
                             null,
                             OK, 2),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             "model,resource",
                             "user",
                             OK, 2),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             null,
                             "user",
                             OK, 3)
        );

        var apiKeyCases = Stream.of(
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             null,
                             OK, 6),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             null,
                             OK, 6),
                // apikey with missing scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             null,
                             FORBIDDEN, -1),
                // apikey with non-existent NCA ID should return empty list
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-nca-id", TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             null,
                             OK, 0)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("getListFilteredRegistryCredentialsArgs")
    void shouldListFilteredRegistryCredentials(
            Object tokenSupplier,
            String rawArtifactTypes,
            String rawProvisionedBys,
            HttpStatus expectedStatus,
            int expectedSize) {
        // Creates SYSTEM provisioned registry credentials.
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = getToken(tokenSupplier);

        // Convert the passed in raw artifact types and provisionedBys strings to enums.
        var artifactTypeEnums = (StringUtils.isBlank(rawArtifactTypes)) ?
                Set.of(ArtifactTypeEnum.values()) :
                Arrays.stream(rawArtifactTypes.split(","))
                        .map(ArtifactTypeEnum::fromText)
                        .collect(Collectors.toSet());
        var provisionedByEnums = (StringUtils.isBlank(rawProvisionedBys)) ?
                Set.of(ProvisionedByEnum.values()) :
                Arrays.stream(rawProvisionedBys.split(","))
                        .map(ProvisionedByEnum::fromText)
                        .collect(Collectors.toSet());

        // Add USER provisioned registry credentials based on expected artifact types.
        if (provisionedByEnums.contains(ProvisionedByEnum.USER)) {
            var addRegistryCredentialRequestsModel = AddRegistryCredentialRequest.builder()
                    .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                    .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                    .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1)
                    .build();
            var addRegistryCredentialRequestsResource = AddRegistryCredentialRequest.builder()
                    .registryHostname(TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname())
                    .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                    .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1)
                    .build();
            var addRegistryCredentialRequestsContainer = AddRegistryCredentialRequest.builder()
                    .registryHostname(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                    .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                    .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                    .build();
            artifactTypeEnums.forEach(at -> {
                var addRegistryCredentialRequest = switch (at) {
                    case CONTAINER -> addRegistryCredentialRequestsContainer;
                    case MODEL -> addRegistryCredentialRequestsModel;
                    case RESOURCE -> addRegistryCredentialRequestsResource;
                    case HELM ->
                            null; // Cannot add another cred for helm registry with same hostname
                };
                if (addRegistryCredentialRequest != null) {
                    var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID);
                    registryCredentialService.addRegistryCredential(
                            TEST_NCA_ID, accountEntity,
                            addRegistryCredentialRequest, ProvisionedByEnum.USER,
                            testCommonService.getAuditEventPayloadBuilder());
                }
            });
        }

        var uri = "/v2/nvcf/registry-credentials";
        if (StringUtils.isNotBlank(rawArtifactTypes)
                || StringUtils.isNotBlank(rawProvisionedBys)) {
            var queryParams = "";
            if (StringUtils.isNotBlank(rawArtifactTypes)) {
                queryParams = "?artifactType=" + rawArtifactTypes;
            }

            if (StringUtils.isNotBlank(rawProvisionedBys)) {
                var separator = !queryParams.isEmpty() ? "&" : "?";
                queryParams += separator + "provisionedBy=" + rawProvisionedBys;
            }
            uri += queryParams;
        }

        var builder = RequestEntity.get(URI.create(uri));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }

        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       ListRegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.registryCredentials()).hasSize(expectedSize);

        if (expectedSize <= 0) {
            return;
        }

        var registryCredentials = responseBody.registryCredentials();
        if (provisionedByEnums.contains(ProvisionedByEnum.SYSTEM)
                && provisionedByEnums.contains(ProvisionedByEnum.USER)) {
            artifactTypeEnums.forEach(artifactType -> {
                var filteredRegCreds = registryCredentials.stream()
                        .filter(rc -> rc.artifactTypes().contains(artifactType))
                        .toList();
                assertThat(filteredRegCreds).isNotEmpty();
                var providedBys = filteredRegCreds.stream()
                        .map(RegistryCredentialDetailsDto::provisionedBy)
                        .collect(Collectors.toSet());
                assertThat(providedBys).contains(ProvisionedByEnum.SYSTEM);
                if (artifactType == ArtifactTypeEnum.HELM) {
                    assertThat(providedBys).doesNotContain(ProvisionedByEnum.USER);
                    assertThat(filteredRegCreds).hasSize(1);
                } else {
                    assertThat(providedBys).contains(ProvisionedByEnum.USER);
                    assertThat(filteredRegCreds).hasSize(2);
                }
            });
        } else if (provisionedByEnums.contains(ProvisionedByEnum.SYSTEM)
                && !provisionedByEnums.contains(ProvisionedByEnum.USER)) {
            artifactTypeEnums.forEach(artifactType -> {
                var filteredRegCreds = registryCredentials.stream()
                        .filter(rc -> rc.artifactTypes().contains(artifactType))
                        .toList();
                assertThat(filteredRegCreds).isNotEmpty();
                var providedBys = filteredRegCreds.stream()
                        .map(RegistryCredentialDetailsDto::provisionedBy)
                        .collect(Collectors.toSet());
                assertThat(providedBys).hasSize(1).contains(ProvisionedByEnum.SYSTEM);
                assertThat(filteredRegCreds).hasSize(1);
            });
        } else if (!provisionedByEnums.contains(ProvisionedByEnum.SYSTEM)
                && provisionedByEnums.contains(ProvisionedByEnum.USER)) {
            artifactTypeEnums.forEach(artifactType -> {
                var filteredRegCreds = registryCredentials.stream()
                        .filter(rc -> rc.artifactTypes().contains(artifactType))
                        .toList();
                if (artifactType == ArtifactTypeEnum.HELM) {
                    assertThat(filteredRegCreds).isEmpty();
                } else {
                    assertThat(filteredRegCreds).isNotEmpty();
                    var providedBys = filteredRegCreds.stream()
                            .map(RegistryCredentialDetailsDto::provisionedBy)
                            .collect(Collectors.toSet());
                    assertThat(providedBys).hasSize(1).contains(ProvisionedByEnum.USER);
                    assertThat(filteredRegCreds).hasSize(1);
                }
            });
        }
    }

    Stream<Arguments> getAddRegistryCredentialsArgs() {
        var validAddRegistryCredentialRequestsModel = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1)
                .build();
        var validAddRegistryCredentialRequestsResource = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1)
                .build();
        var validAddRegistryCredentialRequestsContainer = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var validAddRegistryCredentialRequestHelm = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_HELM_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1)
                .build();
        var existsAddRegistryCredentialRequestHelm = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_HELM_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(SecretDto.builder()
                                .name("ngc-helm-registry-credential")
                                .value(new StringNode(Base64.getEncoder().encodeToString(
                                        "$oauthtoken:nvapi-shsh!shsh".getBytes(UTF_8))))
                                .build())
                .build();
        var existsAddRegistryCredentialRequestContainer = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(SecretDto.builder()
                                .name("ngc-container-registry-credential")
                                .value(new StringNode(Base64.getEncoder().encodeToString(
                                        "$oauthtoken:nvapi-shsh!shsh".getBytes(UTF_8))))
                                .build())
                .build();
        var existsAddRegistryCredentialRequestModel = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                .secret(SecretDto.builder()
                                .name("ngc-model-resource-registry-credential")
                                .value(new StringNode(Base64.getEncoder().encodeToString(
                                        "$oauthtoken:nvapi-shsh!shsh".getBytes(UTF_8))))
                                .build())
                .build();
        var existsAddRegistryCredentialRequestResource = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                .secret(SecretDto.builder()
                                .name("ngc-model-resource-registry-credential")
                                .value(new StringNode(Base64.getEncoder().encodeToString(
                                        "$oauthtoken:nvapi-shsh!shsh".getBytes(UTF_8))))
                                .build())
                .build();
        var unknownRegistry = "unknown-registry.com";
        var unknownRegistryAddRegistryCredentialRequestsModel =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(unknownRegistry)
                        .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                        .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1)
                        .build();
        var unknownRegistryAddRegistryCredentialRequestsResource =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(unknownRegistry)
                        .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                        .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1)
                        .build();
        var unknownRegistryAddRegistryCredentialRequestsHelm =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(unknownRegistry)
                        .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1)
                        .build();
        var unknownRegistryAddRegistryCredentialRequestsContainer =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(unknownRegistry)
                        .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                        .build();
        var nonBase64EncodedSecretValueRegistryAddRegistryCredentialRequestsModel =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                        .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_BASE64_ENCODED)
                        .build();
        var nonBase64EncodedSecretValueAddRegistryCredentialRequestsResource =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(
                                TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                        .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_BASE64_ENCODED)
                        .build();
        var nonBase64EncodedSecretValueAddRegistryCredentialRequestsHelm =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(TEST_NGC_HELM_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_BASE64_ENCODED)
                        .build();
        var nonBase64EncodedSecretValueAddRegistryCredentialRequestsContainer =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(
                                TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_BASE64_ENCODED)
                        .build();
        var missingOauthTokenSecretValueRegistryAddRegistryCredentialRequestsModel =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                        .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_OAUTH_TOKEN)
                        .build();
        var missingOauthTokenSecretValueAddRegistryCredentialRequestsResource =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(
                                TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                        .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_OAUTH_TOKEN)
                        .build();
        var missingOauthTokenSecretValueAddRegistryCredentialRequestsHelm =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(TEST_NGC_HELM_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_OAUTH_TOKEN)
                        .build();
        var missingOauthTokenSecretValueAddRegistryCredentialRequestsContainer =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(
                                TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_OAUTH_TOKEN)
                        .build();
        var missingUsernameSecretValueRegistryAddRegistryCredentialRequestsModel =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                        .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_USER_NAME)
                        .build();
        var missingUsernameSecretValueAddRegistryCredentialRequestsResource =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(
                                TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                        .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_USER_NAME)
                        .build();
        var missingUsernameSecretValueAddRegistryCredentialRequestsHelm =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(TEST_NGC_HELM_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_USER_NAME)
                        .build();
        var missingUsernameSecretValueAddRegistryCredentialRequestsContainer =
                AddRegistryCredentialRequest.builder()
                        .registryHostname(
                                TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                        .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_USER_NAME)
                        .build();

        var jwtCases = Stream.of(
                Arguments.of(null, validAddRegistryCredentialRequestsModel, UNAUTHORIZED),
                Arguments.of("invalid-key", validAddRegistryCredentialRequestsModel, UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(), 100),
                             validAddRegistryCredentialRequestsModel,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             validAddRegistryCredentialRequestsModel,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             validAddRegistryCredentialRequestsModel,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             validAddRegistryCredentialRequestsResource,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             validAddRegistryCredentialRequestsContainer,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             validAddRegistryCredentialRequestHelm,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             existsAddRegistryCredentialRequestHelm,
                             CONFLICT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             existsAddRegistryCredentialRequestContainer,
                             CONFLICT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             existsAddRegistryCredentialRequestModel,
                             CONFLICT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             existsAddRegistryCredentialRequestResource,
                             CONFLICT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             unknownRegistryAddRegistryCredentialRequestsModel,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             unknownRegistryAddRegistryCredentialRequestsResource,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             unknownRegistryAddRegistryCredentialRequestsHelm,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             unknownRegistryAddRegistryCredentialRequestsContainer,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             nonBase64EncodedSecretValueRegistryAddRegistryCredentialRequestsModel,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             nonBase64EncodedSecretValueAddRegistryCredentialRequestsResource,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             nonBase64EncodedSecretValueAddRegistryCredentialRequestsHelm,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             nonBase64EncodedSecretValueAddRegistryCredentialRequestsContainer,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             missingOauthTokenSecretValueRegistryAddRegistryCredentialRequestsModel,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             missingOauthTokenSecretValueAddRegistryCredentialRequestsResource,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             missingOauthTokenSecretValueAddRegistryCredentialRequestsHelm,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             missingOauthTokenSecretValueAddRegistryCredentialRequestsContainer,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             missingUsernameSecretValueRegistryAddRegistryCredentialRequestsModel,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             missingUsernameSecretValueAddRegistryCredentialRequestsResource,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             missingUsernameSecretValueAddRegistryCredentialRequestsHelm,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             missingUsernameSecretValueAddRegistryCredentialRequestsContainer,
                             BAD_REQUEST)
        );

        var apiKeyCases = Stream.of(
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             validAddRegistryCredentialRequestsModel,
                             OK),
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             validAddRegistryCredentialRequestsModel,
                             OK),
                // apikey with missing scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             validAddRegistryCredentialRequestsModel,
                             FORBIDDEN),
                // apikey with incorrect scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of("list_functions"));
                                 return "nvapi-stg-some-key";
                             },
                             validAddRegistryCredentialRequestsModel,
                             FORBIDDEN),
                // apikey with non-existent NCA ID should return NOT_FOUND
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-nca-id", TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             validAddRegistryCredentialRequestsModel,
                             NOT_FOUND)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("getAddRegistryCredentialsArgs")
    void shouldAddRegistryCredential(
            Object tokenSupplier, AddRegistryCredentialRequest request, HttpStatus expectedStatus) {
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = getToken(tokenSupplier);
        var builder = RequestEntity.post(URI.create("/v2/nvcf/registry-credentials"))
                .contentType(MediaType.APPLICATION_JSON);

        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(request);
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        assertThat(responseBody.registryCredential().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.registryCredential().registryHostname())
                .isEqualTo(request.registryHostname());
        assertThat(responseBody.registryCredential().registryName())
                .isEqualTo(NGC_PRIVATE_REGISTRY_NAME);
        assertThat(responseBody.registryCredential().artifactTypes())
                .isEqualTo(request.artifactTypes());
        assertThat(responseBody.registryCredential().provisionedBy())
                .isEqualTo(ProvisionedByEnum.USER);
    }

    Stream<Arguments> getAddRegistryCredentialsWithInvalidRegionArgs() {
        // ECR Private registry with invalid region
        var invalidEcrRegionAddRegistryCredentialRequestsContainer =
                AddRegistryCredentialRequest.builder()
                        .registryHostname("123456789012.dkr.ecr.invalid-region.amazonaws.com")
                        .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                        .build();
        var invalidEcrRegionAddRegistryCredentialRequestsHelm =
                AddRegistryCredentialRequest.builder()
                        .registryHostname("123456789012.dkr.ecr.xyz-east-1.amazonaws.com")
                        .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1)
                        .build();

        // VolcEngine registry with invalid region
        var invalidVolcengineRegionAddRegistryCredentialRequestsContainer =
                AddRegistryCredentialRequest.builder()
                        .registryHostname("test-registry-invalid-region.cr.volces.com")
                        .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                        .build();
        var invalidVolcengineRegionAddRegistryCredentialRequestsHelm =
                AddRegistryCredentialRequest.builder()
                        .registryHostname("test-registry-us-east-1.cr.volces.com")
                        .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1)
                        .build();

        return Stream.of(
                Arguments.of(invalidEcrRegionAddRegistryCredentialRequestsContainer),
                Arguments.of(invalidEcrRegionAddRegistryCredentialRequestsHelm),
                Arguments.of(invalidVolcengineRegionAddRegistryCredentialRequestsContainer),
                Arguments.of(invalidVolcengineRegionAddRegistryCredentialRequestsHelm));
    }

    @ParameterizedTest
    @MethodSource("getAddRegistryCredentialsWithInvalidRegionArgs")
    void shouldThrowBadRequestForAddRegistryCredentialsWithInvalidRegion(
            AddRegistryCredentialRequest request) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                    100);
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var builder = RequestEntity.post(URI.create("/v2/nvcf/registry-credentials"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token);

        var requestEntity = builder.body(request);
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(BAD_REQUEST);
    }

    Stream<Arguments> getGetRegistryCredentialDetailArgs() {
        var addRegistryCredentialRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1)
                .build();

        var jwtCases = Stream.of(
                Arguments.of(null, null, UNAUTHORIZED),
                Arguments.of("invalid-key", null, UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(), 100),
                             null,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             null,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequest,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             null,
                             NOT_FOUND)
        );

        var apiKeyCases = Stream.of(
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequest,
                             OK),
                Arguments.of((Supplier<String>) () -> {
                                setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                            List.of(),
                                            List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                return "nvapi-stg-some-key";
                            },
                            addRegistryCredentialRequest,
                            OK),
                // apikey with missing scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequest,
                             FORBIDDEN),
                // apikey with non-existent NCA ID should return NOT_FOUND
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-nca-id", TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequest,
                             NOT_FOUND)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("getGetRegistryCredentialDetailArgs")
    void shouldGetRegistryCredentialDetail(
            Object tokenSupplier,
            AddRegistryCredentialRequest addRegistryCredentialRequest,
            HttpStatus expectedStatus) {
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = getToken(tokenSupplier);
        var registryCredentialId = UUID.randomUUID();

        if (addRegistryCredentialRequest != null) {
            var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID);
            var entity = registryCredentialService.addRegistryCredential(
                    TEST_NCA_ID, accountEntity,
                    addRegistryCredentialRequest, ProvisionedByEnum.USER,
                    testCommonService.getAuditEventPayloadBuilder());
            registryCredentialId = entity.getKey().getRegistryCredentialId();
        }

        var builder = RequestEntity.get(
                URI.create("/v2/nvcf/registry-credentials/" + registryCredentialId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        assertThat(responseBody.registryCredential().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.registryCredential().registryHostname())
                .isEqualTo(addRegistryCredentialRequest.registryHostname());
        assertThat(responseBody.registryCredential().registryName())
                .isEqualTo(NGC_PRIVATE_REGISTRY_NAME);
        assertThat(responseBody.registryCredential().artifactTypes())
                .isEqualTo(addRegistryCredentialRequest.artifactTypes());
        assertThat(responseBody.registryCredential().provisionedBy())
                .isEqualTo(ProvisionedByEnum.USER);
    }

    Stream<Arguments> getUpdateRegistryCredentialArgs() {
        var addRegistryCredentialRequestsModel = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1)
                .build();
        var updateRegistryCredentialRequestsModel = UpdateRegistryCredentialRequest.builder()
                .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1_UPDATED)
                .build();
        var addRegistryCredentialRequestsResource = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1)
                .build();
        var updateRegistryCredentialRequestsResource = UpdateRegistryCredentialRequest.builder()
                .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1_UPDATED)
                .build();
        var addRegistryCredentialRequestsHelm = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_HELM_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1)
                .build();
        var updateRegistryCredentialRequestsHelm = UpdateRegistryCredentialRequest.builder()
                .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1_UPDATED)
                .build();
        var addRegistryCredentialRequestsContainer = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var updateRegistryCredentialRequestsContainer = UpdateRegistryCredentialRequest.builder()
                .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1_UPDATED)
                .build();
        var addDockerRegistryCredentialRequestsContainer = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var updateDockerRegistryCredentialRequestsHelm = UpdateRegistryCredentialRequest.builder()
                .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                .build();
        var nonBase64EncodedSecretValueRegistryUpdateRegistryCredentialRequestsModel =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_BASE64_ENCODED)
                        .build();
        var nonBase64EncodedSecretValueUpdateRegistryCredentialRequestsResource =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_BASE64_ENCODED)
                        .build();
        var nonBase64EncodedSecretValueUpdateRegistryCredentialRequestsHelm =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_BASE64_ENCODED)
                        .build();
        var nonBase64EncodedSecretValueUpdateRegistryCredentialRequestsContainer =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_BASE64_ENCODED)
                        .build();
        var missingOauthTokenSecretValueRegistryUpdateRegistryCredentialRequestsModel =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_OAUTH_TOKEN)
                        .build();
        var missingOauthTokenSecretValueUpdateRegistryCredentialRequestsResource =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_OAUTH_TOKEN)
                        .build();
        var missingOauthTokenSecretValueUpdateRegistryCredentialRequestsHelm =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_OAUTH_TOKEN)
                        .build();
        var missingOauthTokenSecretValueUpdateRegistryCredentialRequestsContainer =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_OAUTH_TOKEN)
                        .build();
        var missingUsernameSecretValueRegistryUpdateRegistryCredentialRequestsModel =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1_MISSING_USER_NAME)
                        .build();
        var missingUsernameSecretValueUpdateRegistryCredentialRequestsResource =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1_MISSING_USER_NAME)
                        .build();
        var missingUsernameSecretValueUpdateRegistryCredentialRequestsHelm =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1_MISSING_USER_NAME)
                        .build();
        var missingUsernameSecretValueUpdateRegistryCredentialRequestsContainer =
                UpdateRegistryCredentialRequest.builder()
                        .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1_MISSING_USER_NAME)
                        .build();
        var sameSecretNameWithOtherExistingSecretCredentialRequestsContainer =
                UpdateRegistryCredentialRequest.builder()
                        .secret(SecretDto.builder()
                                        .name("ngc-container-registry-credential")
                                        .value(new StringNode(Base64.getEncoder().encodeToString(
                                                "$oauthtoken:test-container-secret-val-1-updated".getBytes(
                                                        UTF_8)))).build())
                        .build();

        var jwtCases = Stream.of(
                Arguments.of(null,
                             null,
                             updateRegistryCredentialRequestsModel,
                             UNAUTHORIZED),
                Arguments.of("invalid-key",
                             null,
                             updateRegistryCredentialRequestsModel,
                             UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(), 100),
                             null,
                             updateRegistryCredentialRequestsModel,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             null,
                             updateRegistryCredentialRequestsModel,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             null,
                             updateRegistryCredentialRequestsModel,
                             NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsModel,
                             updateRegistryCredentialRequestsModel,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsResource,
                             updateRegistryCredentialRequestsResource,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsHelm,
                             updateRegistryCredentialRequestsHelm,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             updateRegistryCredentialRequestsContainer,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addDockerRegistryCredentialRequestsContainer,
                             updateDockerRegistryCredentialRequestsHelm,
                             OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsModel,
                             nonBase64EncodedSecretValueRegistryUpdateRegistryCredentialRequestsModel,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsResource,
                             nonBase64EncodedSecretValueUpdateRegistryCredentialRequestsResource,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsHelm,
                             nonBase64EncodedSecretValueUpdateRegistryCredentialRequestsHelm,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             nonBase64EncodedSecretValueUpdateRegistryCredentialRequestsContainer,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsModel,
                             missingOauthTokenSecretValueRegistryUpdateRegistryCredentialRequestsModel,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsResource,
                             missingOauthTokenSecretValueUpdateRegistryCredentialRequestsResource,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsHelm,
                             missingOauthTokenSecretValueUpdateRegistryCredentialRequestsHelm,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             missingOauthTokenSecretValueUpdateRegistryCredentialRequestsContainer,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsModel,
                             missingUsernameSecretValueRegistryUpdateRegistryCredentialRequestsModel,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsResource,
                             missingUsernameSecretValueUpdateRegistryCredentialRequestsResource,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsHelm,
                             missingUsernameSecretValueUpdateRegistryCredentialRequestsHelm,
                             BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             missingUsernameSecretValueUpdateRegistryCredentialRequestsContainer,
                             BAD_REQUEST),
                // Missing secret and artifact-types in the UpdateRegistryCredentialRequest body.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             UpdateRegistryCredentialRequest.builder().build(),
                             BAD_REQUEST),
                // Try updating the artifact-types of registry credential by adding MODEL
                // artifact-type when the corresponding registry is only recognized for CONTAINER
                // artifact-type.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             UpdateRegistryCredentialRequest.builder()
                                     .artifactTypeEnums(Set.of(ArtifactTypeEnum.MODEL))
                                     .build(),
                             BAD_REQUEST),
                // Try adding HELM artifact-type to a stg.nvcr.io registry-credential.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             UpdateRegistryCredentialRequest.builder()
                                     .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                                     .build(),
                             BAD_REQUEST),
                // Try updating the registry credential with same secret name as an existing
                // registry credential.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             sameSecretNameWithOtherExistingSecretCredentialRequestsContainer,
                             CONFLICT)
        );

        var apiKeyCases = Stream.of(
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequestsModel,
                             updateRegistryCredentialRequestsModel,
                             OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequestsModel,
                             updateRegistryCredentialRequestsModel,
                             OK),
                // apikey with missing scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             updateRegistryCredentialRequestsModel,
                             FORBIDDEN),
                // apikey with non-existent NCA ID should return NOT_FOUND
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-nca-id", TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequestsModel,
                             updateRegistryCredentialRequestsModel,
                             NOT_FOUND)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("getUpdateRegistryCredentialArgs")
    void shouldUpdateRegistryCredential(
            Object tokenSupplier,
            AddRegistryCredentialRequest addRegistryCredentialRequest,
            UpdateRegistryCredentialRequest updateRegistryCredentialRequest,
            HttpStatus expectedStatus) {
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = getToken(tokenSupplier);
        var registryCredentialId = UUID.randomUUID();
        String registryHostname = null;

        if (addRegistryCredentialRequest != null) {
            var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID);
            var entity = registryCredentialService.addRegistryCredential(
                    TEST_NCA_ID, accountEntity,
                    addRegistryCredentialRequest, ProvisionedByEnum.USER,
                    testCommonService.getAuditEventPayloadBuilder());
            registryCredentialId = entity.getKey().getRegistryCredentialId();
            registryHostname = addRegistryCredentialRequest.registryHostname();
        }

        var builder = RequestEntity.patch(
                URI.create("/v2/nvcf/registry-credentials/" + registryCredentialId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(updateRegistryCredentialRequest);
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var origArtifactTypes = addRegistryCredentialRequest.artifactTypes();
        var updatedArtifactTypes = updateRegistryCredentialRequest.artifactTypeEnums();
        var artifactTypes = CollectionUtils.isEmpty(updatedArtifactTypes) ?
                origArtifactTypes : Sets.union(origArtifactTypes, updatedArtifactTypes);

        assertThat(responseBody.registryCredential().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.registryCredential().registryHostname())
                .isEqualTo(registryHostname);
        assertThat(responseBody.registryCredential().registryName())
                .isIn(NGC_PRIVATE_REGISTRY_NAME, DOCKER_REGISTRY_NAME);
        assertThat(responseBody.registryCredential().artifactTypes())
                .containsExactlyInAnyOrderElementsOf(artifactTypes);

        var credential = registryFunctionMapperService
                .toRegistryCredentialDto(responseBody.registryCredential());
        if (updateRegistryCredentialRequest.secret() != null) {
            assertThat(credential.secret())
                    .isEqualTo(updateRegistryCredentialRequest.secret());
        } else {
            assertThat(credential.secret())
                    .isEqualTo(addRegistryCredentialRequest.secret());
        }
    }

    Stream<Arguments> getDeleteRegistryCredentialArgs() {
        var addRegistryCredentialRequestsModel = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                .secret(TestConstants.TEST_THIRD_PARTY_MODEL_SECRET_1)
                .build();
        var addRegistryCredentialRequestsResource = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.RESOURCE))
                .secret(TestConstants.TEST_THIRD_PARTY_RESOURCE_SECRET_1)
                .build();
        var addRegistryCredentialRequestsContainer = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var addRegistryCredentialRequestsHelm = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_HELM_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(TestConstants.TEST_THIRD_PARTY_HELM_SECRET_1)
                .build();
        var jwtCases = Stream.of(
                Arguments.of(null,
                             null,
                             ArtifactTypeEnum.CONTAINER,
                             UNAUTHORIZED),
                Arguments.of("invalid-key",
                             null,
                             ArtifactTypeEnum.CONTAINER,
                             UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(), 100),
                             null,
                             ArtifactTypeEnum.CONTAINER,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             null,
                             ArtifactTypeEnum.CONTAINER,
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             null,
                             ArtifactTypeEnum.CONTAINER,
                             NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsModel,
                             ArtifactTypeEnum.MODEL,
                             NO_CONTENT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsResource,
                             ArtifactTypeEnum.RESOURCE,
                             NO_CONTENT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsHelm,
                             ArtifactTypeEnum.HELM,
                             NO_CONTENT),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             addRegistryCredentialRequestsContainer,
                             ArtifactTypeEnum.CONTAINER,
                             NO_CONTENT)
        );

        var apiKeyCases = Stream.of(
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequestsModel,
                             ArtifactTypeEnum.MODEL,
                             NO_CONTENT),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequestsModel,
                             ArtifactTypeEnum.MODEL,
                             NO_CONTENT),
                // apikey with missing scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             null,
                             ArtifactTypeEnum.CONTAINER,
                             FORBIDDEN),
                // apikey with non-existent NCA ID should return NOT_FOUND
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-nca-id", TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             addRegistryCredentialRequestsModel,
                             ArtifactTypeEnum.MODEL,
                             NOT_FOUND)
        );

        return Stream.concat(jwtCases, apiKeyCases);

    }

    @ParameterizedTest
    @MethodSource("getDeleteRegistryCredentialArgs")
    void shouldDeleteRegistryCredential(
            Object tokenSupplier,
            AddRegistryCredentialRequest addRegistryCredentialRequest,
            ArtifactTypeEnum artifactType,
            HttpStatus expectedStatus) {
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = getToken(tokenSupplier);
        var registryCredentialId = UUID.randomUUID();

        if (addRegistryCredentialRequest != null) {
            var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID);
            var entity = registryCredentialService.addRegistryCredential(
                    TEST_NCA_ID, accountEntity,
                    addRegistryCredentialRequest, ProvisionedByEnum.USER,
                    testCommonService.getAuditEventPayloadBuilder());
            registryCredentialId = entity.getKey().getRegistryCredentialId();
        }

        var registryCredentialIdFinal = registryCredentialId;
        var builder = RequestEntity.delete(
                URI.create("/v2/nvcf/registry-credentials/" + registryCredentialId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        assertThat(responseEntity.getHeaders().getContentType()).isNull();
        assertThrows(NotFoundException.class, () ->
                testRegistryCredentialService.getRegistryCredentialDetails(TEST_NCA_ID,
                                                                           registryCredentialIdFinal));
    }

    Stream<Arguments> getGetRecognizedRegistriesArgs() {
        var jwtCases = Stream.of(
                Arguments.of(null,
                             UNAUTHORIZED),
                Arguments.of("invalid-key",
                             UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(), 100),
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                             100),
                             OK)
        );

        var apiKeyCases = Stream.of(
                // apikey with correct scope should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             OK),
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             OK),
                // apikey with missing scope should return FORBIDDEN
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             FORBIDDEN),
                // apikey with non-existent NCA ID should succeed
                Arguments.of((Supplier<String>) () -> {
                                 setResponse("non-existent-nca-id", TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS));
                                 return "nvapi-stg-some-key";
                             },
                             OK)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource("getGetRecognizedRegistriesArgs")
    void shouldGetRecognizedRegistries(
            Object tokenSupplier,
            HttpStatus expectedStatus) {
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = getToken(tokenSupplier);
        var builder = RequestEntity.get(URI.create("/v2/nvcf/recognized-registries"));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RecognizedRegistriesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var response = responseEntity.getBody();
        assertThat(response).isNotNull();
        assertThat(response.recognizedRegistries()).isEqualTo(Map.of(
                ArtifactTypeEnum.CONTAINER,
                List.of(Map.of("name", ECR_PRIVATE_REGISTRY_NAME,
                               "hostname", ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME),
                        Map.of("name", ECR_PUBLIC_REGISTRY_NAME,
                               "hostname", ECR_PUBLIC_REGISTRY_HOSTNAME),
                        Map.of("name", ACR_REGISTRY_NAME,
                               "hostname",
                               AZURE_REGISTRY_GLOBAL_HOSTNAME),
                        Map.of("name", TEST_CUSTOM_REGISTRY_DISPLAY_NAME_1,
                               "hostname", TEST_CUSTOM_REGISTRY_HOST_NAME_1),
                        Map.of("name", DOCKER_REGISTRY_NAME,
                               "hostname",
                               TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL.registryHostname()),
                        Map.of("name", HARBOR_REGISTRY_NAME,
                               "hostname", TEST_HARBOR_REGISTRY_CREDENTIAL.registryHostname()),
                        Map.of("name", ARTIFACTORY_REGISTRY_NAME,
                               "hostname",
                               TEST_ARTIFACTORY_REGISTRY_CREDENTIAL.registryHostname()),
                        Map.of("name", NGC_PRIVATE_REGISTRY_NAME,
                               "hostname",
                               TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL.registryHostname()),
                        Map.of("name", VOLCENGINE_REGISTRY_NAME,
                               "hostname",
                               VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME)),
                ArtifactTypeEnum.HELM,
                List.of(Map.of("name", ECR_PRIVATE_REGISTRY_NAME,
                               "hostname", ECR_PRIVATE_REGISTRY_GLOBAL_HOSTNAME),
                        Map.of("name", ECR_PUBLIC_REGISTRY_NAME,
                               "hostname", ECR_PUBLIC_REGISTRY_HOSTNAME),
                        Map.of("name", ACR_REGISTRY_NAME,
                               "hostname",
                               AZURE_REGISTRY_GLOBAL_HOSTNAME),
                        Map.of("name", TEST_CUSTOM_REGISTRY_DISPLAY_NAME_1,
                               "hostname", TEST_CUSTOM_REGISTRY_HOST_NAME_1),
                        Map.of("name", DOCKER_REGISTRY_NAME,
                               "hostname",
                               TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL.registryHostname()),
                        Map.of("name", HARBOR_REGISTRY_NAME,
                               "hostname", TEST_HARBOR_REGISTRY_CREDENTIAL.registryHostname()),
                        Map.of("name", ARTIFACTORY_REGISTRY_NAME,
                               "hostname",
                               TEST_ARTIFACTORY_REGISTRY_CREDENTIAL.registryHostname()),
                        Map.of("name", NGC_PRIVATE_REGISTRY_NAME,
                               "hostname", TEST_NGC_HELM_REGISTRY_CREDENTIAL.registryHostname()),
                        Map.of("name", VOLCENGINE_REGISTRY_NAME,
                               "hostname", VOLCENGINE_REGISTRY_GLOBAL_HOSTNAME)),
                ArtifactTypeEnum.MODEL, List.of(Map.of(
                        "name", NGC_PRIVATE_REGISTRY_NAME,
                        "hostname",
                        TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())),
                ArtifactTypeEnum.RESOURCE, List.of(Map.of(
                        "name", NGC_PRIVATE_REGISTRY_NAME,
                        "hostname",
                        TEST_NGC_RESOURCE_REGISTRY_CREDENTIAL.registryHostname()))));
    }

    @Test
    void shouldEnforceRegistryCredentialLimitForAccount() {
        // Setup: Create default accounts and registry credentials (3 registry credentials per account)
        testAccountService.createDefaultAccountsClientsAndRegistries();

        // Setup: Update the max registry credentials allowed to 5
        testAccountService.updateAccountMaxRegistryCredentials(TEST_NCA_ID, 5);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS), 100);

        // Attempt to add first registry credential - should succeed
        var firstRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                .secret(SecretDto.builder()
                                .name("test-secret-1")
                                .value(new StringNode(Base64.getEncoder().encodeToString(
                                        ("$oauthtoken:test-token-1").getBytes(UTF_8))))
                                .build())
                .build();

        var fistRequestEntity = RequestEntity.post(URI.create("/v2/nvcf/registry-credentials"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(firstRequest);

        var firstResponse = testRestTemplate.exchange(fistRequestEntity,
                                                      RegistryCredentialDetailsResponse.class);

        assertThat(firstResponse.getStatusCode()).isEqualTo(OK);
        assertThat(firstResponse.getBody()).isNotNull();

        // Attempt to add second new registry credential - should succeed
        var secondRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                .secret(SecretDto.builder()
                                .name("test-secret-2")
                                .value(new StringNode(Base64.getEncoder().encodeToString(
                                        ("$oauthtoken:test-token-2").getBytes(UTF_8))))
                                .build())
                .build();

        var secondRequestEntity = RequestEntity.post(URI.create("/v2/nvcf/registry-credentials"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(secondRequest);

        var secondResponse = testRestTemplate.exchange(secondRequestEntity,
                                                       RegistryCredentialDetailsResponse.class);

        assertThat(secondResponse.getStatusCode()).isEqualTo(OK);
        assertThat(secondResponse.getBody()).isNotNull();

        // Attempt to add third new registry credential - should fail
        var thirdRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_MODEL_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.MODEL))
                .secret(SecretDto.builder()
                                .name("test-secret-3")
                                .value(new StringNode(Base64.getEncoder().encodeToString(
                                        ("$oauthtoken:test-token-3").getBytes(UTF_8))))
                                .build())
                .build();

        var thirdRequestEntity = RequestEntity.post(URI.create("/v2/nvcf/registry-credentials"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(thirdRequest);

        var thirdResponse = testRestTemplate.exchange(thirdRequestEntity,
                                                      RegistryCredentialDetailsResponse.class);

        assertThat(thirdResponse.getStatusCode()).isEqualTo(BAD_REQUEST);
    }

    Stream<Arguments> getAddThirdPartyRegistryCredentialsWithValidationArgs() {
        var validNgcContainerAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_NGC_CONTAINER_SECRET_1)
                .build();
        var validNgcHelmAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_HELM_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(TEST_NGC_HELM_SECRET_1)
                .build();
        var validNgcHelmLegacyAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_HELM_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(TEST_NGC_HELM_LEGACY_SECRET_1)
                .build();
        var validDockerAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL.registryHostname())
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_DOCKER_HUB_SECRET_1)
                .build();
        var validEcrPrivateAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ECR_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_ECR_PRIVATE_SECRET_1)
                .build();
        var validEcrPublicAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(ECR_PUBLIC_REGISTRY_HOSTNAME)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_ECR_PUBLIC_SECRET_1)
                .build();
        var validVolcengineAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_VOLCENGINE_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_VOLCENGINE_SECRET_1)
                .build();
        var validAcrAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ACR_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_ACR_SECRET_1)
                .build();
        var validHarborAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_HARBOR_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_HARBOR_SECRET_1)
                .build();
        var validArtifactoryAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ARTIFACTORY_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_ARTIFACTORY_SECRET_1)
                .build();

        // Invalid credentials
        var invalidEcrPrivateAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ECR_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_INVALID_ECR_PRIVATE_SECRET_1)
                .build();
        var invalidEcrPublicAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(ECR_PUBLIC_REGISTRY_HOSTNAME)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_INVALID_ECR_PUBLIC_SECRET_1)
                .build();
        var invalidVolcengineAddRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_VOLCENGINE_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_INVALID_VOLCENGINE_SECRET_1)
                .build();
        var invalidDescriptionTooLongRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_NGC_CONTAINER_SECRET_1)
                .description("a".repeat(257))  // Exceeds MAX_DESCRIPTION_LENGTH of 256
                .build();

        return Stream.of(
                // Valid credentials
                Arguments.of(validNgcContainerAddRequest, OK),
                Arguments.of(validNgcHelmAddRequest, OK),
                Arguments.of(validNgcHelmLegacyAddRequest, OK),
                Arguments.of(validDockerAddRequest, OK),
                Arguments.of(validEcrPrivateAddRequest, OK),
                Arguments.of(validEcrPublicAddRequest, OK),
                Arguments.of(validVolcengineAddRequest, OK),
                Arguments.of(validAcrAddRequest, OK),
                Arguments.of(validHarborAddRequest, OK),
                Arguments.of(validArtifactoryAddRequest, OK),
                // Invalid credentials - should fail validation
                Arguments.of(invalidEcrPrivateAddRequest, BAD_REQUEST),
                Arguments.of(invalidEcrPublicAddRequest, BAD_REQUEST),
                Arguments.of(invalidVolcengineAddRequest, BAD_REQUEST),
                Arguments.of(invalidDescriptionTooLongRequest, BAD_REQUEST));
    }

    @ParameterizedTest
    @MethodSource("getAddThirdPartyRegistryCredentialsWithValidationArgs")
    void shouldAddThirdPartyRegistryCredentialWithValidation(
            AddRegistryCredentialRequest request, HttpStatus expectedStatus) {
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                    100);
        var builder = RequestEntity.post(URI.create("/v2/nvcf/registry-credentials"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token);

        var requestEntity = builder.body(request);
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        assertThat(responseBody.registryCredential().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.registryCredential().registryHostname())
                .isEqualTo(request.registryHostname());
        assertThat(responseBody.registryCredential().artifactTypes())
                .isEqualTo(request.artifactTypes());
        assertThat(responseBody.registryCredential().provisionedBy())
                .isEqualTo(ProvisionedByEnum.USER);
    }

    Stream<Arguments> getUpdateThirdPartyRegistryCredentialsWithValidationArgs() {
        var addNgcContainerRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_NGC_CONTAINER_SECRET_1)
                .build();
        var updateNgcContainerSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_NGC_CONTAINER_SECRET_2)
                .build();
        var addNgcHelmRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_HELM_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(TEST_NGC_HELM_SECRET_1)
                .build();
        var updateNgcHelmSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_NGC_HELM_SECRET_2)
                .build();
        var addNgcHelmLegacyRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_NGC_HELM_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.HELM))
                .secret(TEST_NGC_HELM_LEGACY_SECRET_1)
                .build();
        var updateNgcHelmLegacySecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_NGC_HELM_LEGACY_SECRET_2)
                .build();

        var addDockerRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_DOCKER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_DOCKER_HUB_SECRET_1)
                .build();
        var updateDockerSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_DOCKER_HUB_SECRET_2)
                .build();
        var updateDockerAddArtifactTypeRequest = UpdateRegistryCredentialRequest.builder()
                .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                .build();

        var addEcrPrivateRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ECR_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_ECR_PRIVATE_SECRET_1)
                .build();
        var updateEcrPrivateSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_ECR_PRIVATE_SECRET_2)
                .build();
        var updateEcrPrivateInvalidSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_INVALID_ECR_PRIVATE_SECRET_1)
                .build();
        var updateEcrPrivateAddArtifactTypeRequest = UpdateRegistryCredentialRequest.builder()
                .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                .build();

        var addEcrPublicRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(ECR_PUBLIC_REGISTRY_HOSTNAME)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_ECR_PUBLIC_SECRET_1)
                .build();
        var updateEcrPublicSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_ECR_PUBLIC_SECRET_2)
                .build();
        var updateEcrPublicInvalidSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_INVALID_ECR_PUBLIC_SECRET_1)
                .build();
        var updateEcrPublicAddArtifactTypeRequest = UpdateRegistryCredentialRequest.builder()
                .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                .build();

        var addVolcengineRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_VOLCENGINE_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_VOLCENGINE_SECRET_1)
                .build();
        var updateVolcengineSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_VOLCENGINE_SECRET_2)
                .build();
        var updateVolcengineInvalidSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_INVALID_VOLCENGINE_SECRET_1)
                .build();
        var updateVolcengineAddArtifactTypeRequest = UpdateRegistryCredentialRequest.builder()
                .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                .build();

        var addAcrRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ACR_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_ACR_SECRET_1)
                .build();
        var updateAcrSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_ACR_SECRET_2)
                .build();
        var updateAcrAddArtifactTypeRequest = UpdateRegistryCredentialRequest.builder()
                .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                .build();

        var addHarborRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_HARBOR_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_HARBOR_SECRET_1)
                .build();
        var updateHarborSecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_HARBOR_SECRET_2)
                .build();
        var updateHarborAddArtifactTypeRequest = UpdateRegistryCredentialRequest.builder()
                .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                .build();

        var addArtifactoryRequest = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ARTIFACTORY_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_ARTIFACTORY_SECRET_1)
                .build();
        var updateArtifactorySecretRequest = UpdateRegistryCredentialRequest.builder()
                .secret(TEST_ARTIFACTORY_SECRET_2)
                .build();
        var updateArtifactoryAddArtifactTypeRequest = UpdateRegistryCredentialRequest.builder()
                .artifactTypeEnums(Set.of(ArtifactTypeEnum.HELM))
                .build();

        return Stream.of(
                // Valid NGC Container registry credentials update with validation
                Arguments.of(addNgcContainerRequest, updateNgcContainerSecretRequest, OK),
                // Valid NGC Helm registry credentials update with validation (apikey)
                Arguments.of(addNgcHelmRequest, updateNgcHelmSecretRequest, OK),
                // Valid NGC Helm registry credentials update with validation (Legacy apikey)
                Arguments.of(addNgcHelmLegacyRequest, updateNgcHelmLegacySecretRequest, OK),
                // Valid Docker Hub registry credentials update with validation
                Arguments.of(addDockerRequest, updateDockerSecretRequest, OK),
                // Valid ECR Private registry credentials update with validation
                Arguments.of(addEcrPrivateRequest, updateEcrPrivateSecretRequest, OK),
                // Valid ECR Public registry credentials update with validation
                Arguments.of(addEcrPublicRequest, updateEcrPublicSecretRequest, OK),
                // Valid Volcengine registry credentials update with validation
                Arguments.of(addVolcengineRequest, updateVolcengineSecretRequest, OK),
                // Valid ACR registry credentials update with validation
                Arguments.of(addAcrRequest, updateAcrSecretRequest, OK),
                // Valid Harbor registry credentials update with validation
                Arguments.of(addHarborRequest, updateHarborSecretRequest, OK),
                // Valid Artifactory registry credentials update with validation
                Arguments.of(addArtifactoryRequest, updateArtifactorySecretRequest, OK),
                // Invalid ECR Private registry credentials update - should fail validation
                Arguments.of(addEcrPrivateRequest, updateEcrPrivateInvalidSecretRequest,
                             BAD_REQUEST),
                // Invalid ECR Public registry credentials update - should fail validation
                Arguments.of(addEcrPublicRequest, updateEcrPublicInvalidSecretRequest, BAD_REQUEST),
                // Invalid Volcengine registry credentials update - should fail validation
                Arguments.of(addVolcengineRequest, updateVolcengineInvalidSecretRequest,
                             BAD_REQUEST),
                // Docker Hub - add HELM artifact type (validates existing secret for new artifact type)
                Arguments.of(addDockerRequest, updateDockerAddArtifactTypeRequest, OK),
                // ECR Private - add HELM artifact type (validates existing secret for new artifact type)
                Arguments.of(addEcrPrivateRequest, updateEcrPrivateAddArtifactTypeRequest, OK),
                // ECR Public - add HELM artifact type (validates existing secret for new artifact type)
                Arguments.of(addEcrPublicRequest, updateEcrPublicAddArtifactTypeRequest, OK),
                // Volcengine - add HELM artifact type (validates existing secret for new artifact type)
                Arguments.of(addVolcengineRequest, updateVolcengineAddArtifactTypeRequest, OK),
                // ACR - add HELM artifact type (validates existing secret for new artifact type)
                Arguments.of(addAcrRequest, updateAcrAddArtifactTypeRequest, OK),
                // Harbor - add HELM artifact type (validates existing secret for new artifact type)
                Arguments.of(addHarborRequest, updateHarborAddArtifactTypeRequest, OK),
                // Artifactory - add HELM artifact type (validates existing secret for new artifact type)
                Arguments.of(addArtifactoryRequest, updateArtifactoryAddArtifactTypeRequest, OK));
    }

    @ParameterizedTest
    @MethodSource("getUpdateThirdPartyRegistryCredentialsWithValidationArgs")
    void shouldUpdateThirdPartyRegistryCredentialWithValidation(
            AddRegistryCredentialRequest addRegistryCredentialRequest,
            UpdateRegistryCredentialRequest updateRegistryCredentialRequest,
            HttpStatus expectedStatus) {
        testAccountService.createDefaultAccountsClientsAndRegistries();
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_MANAGE_REGISTRY_CREDENTIALS),
                                                    100);
        var registryCredentialId = UUID.randomUUID();
        String registryHostname = null;

        if (addRegistryCredentialRequest != null) {
            var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID);
            var entity = registryCredentialService.addRegistryCredential(
                    TEST_NCA_ID, accountEntity,
                    addRegistryCredentialRequest, ProvisionedByEnum.USER,
                    testCommonService.getAuditEventPayloadBuilder());
            registryCredentialId = entity.getKey().getRegistryCredentialId();
            registryHostname = addRegistryCredentialRequest.registryHostname();
        }

        var builder = RequestEntity.patch(
                        URI.create("/v2/nvcf/registry-credentials/" + registryCredentialId))
                .header("Authorization", "Bearer " + token);
        var requestEntity = builder.body(updateRegistryCredentialRequest);
        var responseEntity = testRestTemplate.exchange(requestEntity,
                                                       RegistryCredentialDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        assertThat(responseBody.registryCredential().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.registryCredential().registryHostname())
                .isEqualTo(registryHostname);
        assertThat(responseBody.registryCredential().provisionedBy())
                .isEqualTo(ProvisionedByEnum.USER);

        var credential = registryFunctionMapperService
                .toRegistryCredentialDto(responseBody.registryCredential());
        if (updateRegistryCredentialRequest.secret() != null) {
            assertThat(credential.secret())
                    .isEqualTo(updateRegistryCredentialRequest.secret());
        }
    }
}
