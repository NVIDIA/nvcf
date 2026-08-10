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
package com.nvidia.nvcf.rest.account;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.service.registry.RegistryCredentialService.ARTIFACT_TYPE_ENUMS;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_ACCOUNT_SETUP;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACR_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_ARTIFACTORY_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_CONTAINER_REGISTRY_INVALID_FORMAT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DOCKER_CONTAINER_REGISTRY_MISSING_BASE64_CREDS;
import static com.nvidia.nvcf.util.TestConstants.TEST_ECR_PRIVATE_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_ECR_PUBLIC_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_HARBOR_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_HELM_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_MODEL_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestConstants.TEST_VOLCENGINE_REGISTRY_CREDENTIAL;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;

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
import com.nvidia.nvcf.rest.account.dto.CreateAccountRequest;
import com.nvidia.nvcf.rest.account.dto.CreateAccountResponse;
import com.nvidia.nvcf.rest.function.management.dto.SecretDto;
import com.nvidia.nvcf.rest.registry.dto.RegistryCredentialDto;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.client.ClientService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.registry.RegistryCredentialEssService;
import com.nvidia.nvcf.service.registry.RegistryCredentialLookupService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.NvcfConstants;
import java.net.URI;
import java.util.Collection;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
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
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class AccountCreationWithRegistriesTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private AccountService accountService;

    @Autowired
    private ClientService clientService;

    @Autowired
    private RegistryCredentialLookupService registryCredentialLookupService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private RegistryCredentialEssService registryCredentialEssService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @Value("${nvcf.registries.recognized.container.docker.oauth2.base-url}")
    private String dockerHubAuthBaseUrl;

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
        MockDockerRegistryAuthServer.start(dockerHubAuthBaseUrl);
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
        MockNgcContainerRegistryServer.stop();
        MockCasServer.stop();
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
    }

    Stream<Arguments> createAccountArgs() {
        var validRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .build();
        var validRequestBodyWithDockerContainerRegistry = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .build();
        var validRequestBodyWithEcrPrivateRegistry = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_ECR_PRIVATE_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var validRequestBodyWithEcrPublicRegistry = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_ECR_PUBLIC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var validRequestBodyWithVolcegineRegistry = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_VOLCENGINE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var validRequestBodyWithAcrRegistry = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_ACR_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var validRequestBodyWithHarborRegistry = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_HARBOR_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var validRequestBodyWithArtifactoryRegistry = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_ARTIFACTORY_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var missingRequiredContainerRegistryRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_HELM_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var missingRequiredModelRegistryRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .build();
        var missingRequiredHelmRegistryRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var missingRequiredResourceRegistryRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_REGISTRY_CREDENTIAL))
                .build();
        var multipleContainerRegistryRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_DOCKER_CONTAINER_REGISTRY_CREDENTIAL))
                .build();
        var multipleModelRegistryRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL))
                .build();
        var multipleHelmRegistryRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL,
                                             TEST_VOLCENGINE_REGISTRY_CREDENTIAL))
                .build();

        // Good registry-credential followed by a bad one in the list to make sure that the
        // good one gets deleted when account creation fails.
        var missingBase64EncodedCredsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_MODEL_REGISTRY_CREDENTIAL,
                                             TEST_DOCKER_CONTAINER_REGISTRY_MISSING_BASE64_CREDS))
                .build();
        // Good registry-credential followed by a bad one in the list to make sure that the
        // good one gets deleted when account creation fails.
        var invalidFormatCredsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_MODEL_REGISTRY_CREDENTIAL,
                                             TEST_DOCKER_CONTAINER_REGISTRY_INVALID_FORMAT))
                .build();
        var registryWithObjectNodeAsSecretValue = RegistryCredentialDto.builder()
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .registryHostname("stg.nvcr.io")
                .secret(SecretDto.builder()
                                .name("ngc-container-registry-credential")
                                .value(jsonMapper.createObjectNode().put("prop", "value"))
                                .build())
                .build();
        var invalidObjectNodeCredsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(registryWithObjectNodeAsSecretValue,
                                             TEST_NGC_MODEL_REGISTRY_CREDENTIAL))
                .build();

        return Stream.of(
                Arguments.of(null, validRequestBody, HttpStatus.UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100),
                             validRequestBody,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             validRequestBody, HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             validRequestBody, HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             validRequestBodyWithDockerContainerRegistry, HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             validRequestBodyWithEcrPrivateRegistry, HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             validRequestBodyWithEcrPublicRegistry, HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             validRequestBodyWithVolcegineRegistry, HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             validRequestBodyWithAcrRegistry, HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             validRequestBodyWithHarborRegistry, HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             validRequestBodyWithArtifactoryRegistry, HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             missingRequiredContainerRegistryRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             missingRequiredModelRegistryRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             missingRequiredHelmRegistryRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             missingRequiredResourceRegistryRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             multipleContainerRegistryRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             multipleModelRegistryRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             multipleHelmRegistryRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             missingBase64EncodedCredsRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             invalidFormatCredsRequestBody, HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(SCOPE_ACCOUNT_SETUP), 100),
                             invalidObjectNodeCredsRequestBody, HttpStatus.BAD_REQUEST));
    }

    @ParameterizedTest
    @MethodSource("createAccountArgs")
    void shouldCreateAccountWithRegistries(
            Object tokenSupplier,
            CreateAccountRequest requestBody,
            HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        var builder = RequestEntity.post(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            // Confirm that the account was not created.
            assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                    () -> accountService.assertAccountExistsOrThrow(TEST_NCA_ID_3));

            // Confirm that the registry-credentials if created were cleaned up.
            var registryCreds = registryCredentialLookupService
                    .lookupRegistryCredentialByAccount(TEST_NCA_ID_3)
                    .toList();
            assertThat(registryCreds).isEmpty();
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var accountDto = responseBody.account();
        assertThat(accountDto).isNotNull();
        assertThat(accountDto.name()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(accountDto.ncaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(accountDto.adminClientIds().getFirst()).isEqualTo(TEST_CLIENT_3);

        // Verify accounts table in the DB has an entry for TEST_NCA_ID_3.
        var accountEntity = accountService.getAccount(TEST_NCA_ID_3);
        assertThat(accountEntity).isNotNull();
        assertThat(accountEntity.getName()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(accountEntity.getNcaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(accountEntity.getClientIds()).isNotNull();
        assertThat(accountEntity.getClientIds().stream().toList().getFirst()).isEqualTo(TEST_CLIENT_3);

        // Verify clients table in the DB has an entry for TEST_CLIENT_3.
        var clientVo = clientService.lookupClientOrThrow(TEST_CLIENT_3);
        assertThat(clientVo).isNotNull();
        assertThat(clientVo.getName()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(clientVo.getNcaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(clientVo.getClientId()).isEqualTo(TEST_CLIENT_3);

        // Verify new registry_credentials_by_account table in the DB has entries and
        // ESS has secrets in the new path.
        assertThat(
                registryCredentialLookupService.getRegistryCredentialDtos(TEST_NCA_ID_3))
                .hasSize(requestBody.registryCredentials().size());
        var dtos = registryCredentialLookupService.getRegistryCredentialDtos(TEST_NCA_ID_3);
        var actualArtifactTypes = new HashSet<ArtifactTypeEnum>();
        dtos.forEach(dto -> {
            assertThat(dto.registryCredentialId()).isNotNull();
            assertThat(dto.artifactTypes()).isSubsetOf(ARTIFACT_TYPE_ENUMS);
            assertThat(dto.registryName()).isNotBlank();
            assertThat(dto.registryCredentialName()).isNotBlank();
            assertThat(dto.ncaId()).isEqualTo(TEST_NCA_ID_3);
            assertThat(dto.createdAt()).isNotNull();

            var registryId = dto.registryCredentialId();
            var types = dto.artifactTypes();
            var ncaId = dto.ncaId();
            var secret =
                    registryCredentialEssService.getRegistryCredentialSecret(ncaId, registryId);

            assertThat(secret).isPresent();
            assertThat(secret.get().name()).isNotBlank();
            assertThat(secret.get().value().asString()).isNotBlank();
            assertThat(secret.get().value().asString()).isBase64();

            actualArtifactTypes.addAll(types);
        });
        var expectedArtifactTypes = requestBody.registryCredentials().stream()
                .map(RegistryCredentialDto::artifactTypes)
                .flatMap(Collection::stream)
                .collect(Collectors.toSet());
        assertThat(actualArtifactTypes).containsExactlyInAnyOrderElementsOf(expectedArtifactTypes);

        // Verify DB table registry_credentials_by_account has entries and ESS has secrets
        // in the corresponding path.
        assertThat(
                registryCredentialLookupService.getRegistryCredentialDtos(TEST_NCA_ID_3))
                .hasSize(requestBody.registryCredentials().size());
        dtos = registryCredentialLookupService.getRegistryCredentialDtos(TEST_NCA_ID_3);
        actualArtifactTypes.clear();
        dtos.forEach(dto -> {
            assertThat(dto.registryCredentialId()).isNotNull();
            assertThat(dto.artifactTypes()).isSubsetOf(ARTIFACT_TYPE_ENUMS);
            assertThat(dto.registryName()).isNotBlank();
            assertThat(dto.registryCredentialName()).isNotBlank();
            assertThat(dto.ncaId()).isEqualTo(TEST_NCA_ID_3);
            assertThat(dto.createdAt()).isNotNull();

            var registryId = dto.registryCredentialId();
            var ncaId = dto.ncaId();
            var secret =
                    registryCredentialEssService.getRegistryCredentialSecret(ncaId, registryId);
            assertThat(secret).isPresent();
            assertThat(secret.get().name()).isNotBlank();
            assertThat(secret.get().value().asString()).isNotBlank();
            assertThat(secret.get().value().asString()).isBase64();

            actualArtifactTypes.addAll(dto.artifactTypes());
        });
        assertThat(actualArtifactTypes).containsExactlyInAnyOrderElementsOf(expectedArtifactTypes);
    }

    Stream<Arguments> createAccountWithMaxLimitsArgs() {
        var validLimitsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .maxTelemetriesAllowed(10)
                .maxRegistryCredentialsAllowed(15)
                .build();
        var defaultLimitsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .maxTelemetriesAllowed(25)  // Default max
                .maxRegistryCredentialsAllowed(25)  // Default max
                .build();
        var zeroLimitsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .maxTelemetriesAllowed(0)
                .maxRegistryCredentialsAllowed(0)
                .build();
        var exceedsMaxTelemetriesRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .maxTelemetriesAllowed(26)  // Exceeds max
                .maxRegistryCredentialsAllowed(15)
                .build();
        var exceedsMaxRegistryCredentialsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .maxTelemetriesAllowed(10)
                .maxRegistryCredentialsAllowed(26)  // Exceeds max
                .build();
        var negativeTelemetriesRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .maxTelemetriesAllowed(-1)  // Negative value
                .maxRegistryCredentialsAllowed(15)
                .build();
        var negativeRegistryCredentialsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .maxTelemetriesAllowed(10)
                .maxRegistryCredentialsAllowed(-1)  // Negative value
                .build();
        var nullLimitsRequestBody = CreateAccountRequest.builder()
                .adminClientId(TEST_CLIENT_3)
                .name(TEST_ACCOUNT_NAME_3)
                .registryCredentials(List.of(TEST_NGC_CONTAINER_REGISTRY_CREDENTIAL,
                                             TEST_NGC_MODEL_RESOURCE_REGISTRY_CREDENTIAL,
                                             TEST_NGC_HELM_REGISTRY_CREDENTIAL))
                .maxTelemetriesAllowed(null)  // Should use default
                .maxRegistryCredentialsAllowed(null)  // Should use default
                .build();

        return Stream.of(
                Arguments.of(validLimitsRequestBody, HttpStatus.OK, 10, 15),
                Arguments.of(defaultLimitsRequestBody, HttpStatus.OK, 25, 25),
                Arguments.of(nullLimitsRequestBody, HttpStatus.OK,
                             NvcfConstants.DEFAULT_MAX_TELEMETRIES_ALLOWED,
                             NvcfConstants.DEFAULT_MAX_REGISTRY_CREDENTIALS_ALLOWED),
                Arguments.of(zeroLimitsRequestBody, HttpStatus.BAD_REQUEST, null, null),
                Arguments.of(exceedsMaxTelemetriesRequestBody, HttpStatus.BAD_REQUEST, null, null),
                Arguments.of(exceedsMaxRegistryCredentialsRequestBody, HttpStatus.BAD_REQUEST, null,
                             null),
                Arguments.of(negativeTelemetriesRequestBody, HttpStatus.BAD_REQUEST, null, null),
                Arguments.of(negativeRegistryCredentialsRequestBody, HttpStatus.BAD_REQUEST, null,
                             null)
                        );
    }

    @ParameterizedTest
    @MethodSource("createAccountWithMaxLimitsArgs")
    void shouldCreateAccountWithMaxLimits(
            CreateAccountRequest requestBody,
            HttpStatus expectedStatus,
            Integer expectedMaxTelemetries,
            Integer expectedMaxRegistryCredentials) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(SCOPE_ACCOUNT_SETUP), 100);
        var builder = RequestEntity.post(
                        URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID_3))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token);

        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, CreateAccountResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            // Confirm that the account was not created.
            assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                    () -> accountService.assertAccountExistsOrThrow(TEST_NCA_ID_3));
            return;
        }
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var accountDto = responseBody.account();
        assertThat(accountDto).isNotNull();
        assertThat(accountDto.name()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(accountDto.ncaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(accountDto.adminClientIds().getFirst()).isEqualTo(TEST_CLIENT_3);
        assertThat(accountDto.maxTelemetriesAllowed()).isEqualTo(expectedMaxTelemetries);
        assertThat(accountDto.maxRegistryCredentialsAllowed()).isEqualTo(
                expectedMaxRegistryCredentials);

        // Verify accounts table in the DB has the correct limits
        var accountEntity = accountService.getAccount(TEST_NCA_ID_3);
        assertThat(accountEntity).isNotNull();
        assertThat(accountEntity.getName()).isEqualTo(TEST_ACCOUNT_NAME_3);
        assertThat(accountEntity.getNcaId()).isEqualTo(TEST_NCA_ID_3);
        assertThat(accountEntity.getMaxTelemetriesAllowed()).isEqualTo(expectedMaxTelemetries);
        assertThat(accountEntity.getMaxRegistryCredentialsAllowed()).isEqualTo(
                expectedMaxRegistryCredentials);

        // Verify registry credentials were created properly
        assertThat(
                registryCredentialLookupService.getRegistryCredentialDtos(TEST_NCA_ID_3))
                .hasSize(requestBody.registryCredentials().size());
    }

    @Test
    void deleteAndRecreateAccountsWithRegistries() {
        // Create default accounts and corresponding registry credentials.
        testAccountService.createDefaultAccountsClientsAndRegistries(true);

        // Verify accounts were created properly.
        var accounts = accountService.getAccounts();
        assertThat(accounts).isNotEmpty().hasSize(testAccountService.getDefaultAccounts().size());

        // Verify registryCredentials were created properly.
        accounts.forEach(account -> {
            var ncaId = account.getNcaId();
            var regCreds = registryCredentialLookupService.getRegistryCredentialDtos(ncaId);
            regCreds.forEach(regCred -> {
                var registryId = regCred.registryCredentialId();
                var secret =
                        registryCredentialEssService.getRegistryCredentialSecret(ncaId, registryId);
                assertThat(secret).isPresent();
                assertThat(secret.get().value().asString()).isBase64();
            });
            assertThat(registryCredentialLookupService.getRegistryCredentialDtos(ncaId))
                    .hasSize(3);
        });

        // Delete all the accounts and the corresponding registry credentials.
        accounts.forEach(account -> {
            var ncaId = account.getNcaId();
            accountService.deleteAccount(ncaId, testCommonService.getAuditEventPayloadBuilder());
            assertThat(registryCredentialLookupService.getRegistryCredentialDtos(ncaId))
                    .isEmpty();
        });

        // Recreate accounts and the registry credentials.
        testAccountService.createDefaultAccountsClientsAndRegistries(true);
    }
}
