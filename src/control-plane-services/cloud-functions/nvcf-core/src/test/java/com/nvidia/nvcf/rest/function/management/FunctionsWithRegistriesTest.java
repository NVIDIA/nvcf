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
package com.nvidia.nvcf.rest.function.management;

import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_CONTAINER_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ACR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ARTIFACTORY_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_DOCKER_CONTAINER_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_CONTAINER_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_DIGEST_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_CONTAINER_IMAGE_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_CONTAINER_REGISTRY;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_HELM_CHART_TAG_NOT_FOUND;
import static com.nvidia.boot.mock.BootTestConstants.TEST_VOLCENGINE_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.registries.service.registry.client.ecr.EcrRegistryUtils.ECR_PUBLIC_REGISTRY_HOSTNAME;
import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.function.management.dto.ApiBodyFormatEnum.PREDICT_V2;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_IMAGE_UNKNOWN_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ECR_PRIVATE_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_ECR_PUBLIC_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_ENDPOINT;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_URI;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_UNKNOWN_REGISTRY;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_MISSING_PROTOCOL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_UNKNOWN_REGISTRY_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_MISSING_PROTOCOL_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_UNKNOWN_REGISTRY_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VOLCENGINE_SECRET_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.mock.artifactory.MockArtifactoryAuthServer;
import com.nvidia.boot.mock.azure.MockAcrAuthServer;
import com.nvidia.boot.mock.docker.MockDockerRegistryAuthServer;
import com.nvidia.boot.mock.docker.MockDockerRegistryServer;
import com.nvidia.boot.mock.ecr.MockEcrPrivateRegistryServer;
import com.nvidia.boot.mock.ecr.MockEcrPublicRegistryServer;
import com.nvidia.boot.mock.harbor.MockHarborAuthServer;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.boot.mock.oci.MockOciRegistryServer;
import com.nvidia.boot.mock.volcengine.MockVolcengineRegistryServer;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.management.dto.ArtifactDto;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.HealthDto;
import com.nvidia.nvcf.rest.function.management.dto.ProtocolEnum;
import com.nvidia.nvcf.rest.registry.dto.AddRegistryCredentialRequest;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import com.nvidia.nvcf.service.registry.RegistryCredentialService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
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

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class FunctionsWithRegistriesTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private RegistryCredentialService registryCredentialService;

    @Autowired
    protected JsonMapper jsonMapper;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Autowired
    private FunctionMapperService functionMapperService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @Value("${nvcf.registries.recognized.container.docker.oauth2.base-url}")
    private String dockerAuthBaseUrl;

    @Value("${nvcf.registries.recognized.container.docker.hostname}")
    private String dockerBaseUrl;

    @Value("${nvcf.registries.recognized.container.ecr.hostname}")
    private String ecrBaseUrl;

    @Value("${nvcf.registries.recognized.container.ecr-public.hostname}")
    private String ecrPublicBaseUrl;

    @Value("${nvcf.registries.recognized.container.volcengine.hostname}")
    private String volcengineBaseUrl;

    @Value("${nvcf.registries.recognized.container.acr.hostname}")
    private String acrBaseUrl;

    @Value("${nvcf.registries.recognized.container.acr.oauth2.base-url}")
    private String acrAuthBaseUrl;

    private MockOciRegistryServer mockAcrRegistryServer;

    @Value("${nvcf.registries.recognized.container.artifactory.hostname}")
    private String artifactoryBaseUrl;

    @Value("${nvcf.registries.recognized.container.artifactory.oauth2.base-url}")
    private String artifactoryAuthBaseUrl;

    private MockOciRegistryServer mockArtifactoryRegistryServer;

    @Value("${nvcf.registries.recognized.container.harbor.hostname}")
    private String harborBaseUrl;

    @Value("${nvcf.registries.recognized.container.harbor.oauth2.base-url}")
    private String harborAuthBaseUrl;

    private MockOciRegistryServer mockHarborRegistryServer;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        MockDockerRegistryServer.start(dockerBaseUrl);
        MockDockerRegistryAuthServer.start(dockerAuthBaseUrl);
        MockEcrPrivateRegistryServer.start(ecrBaseUrl);
        MockEcrPublicRegistryServer.start(ecrPublicBaseUrl);
        MockVolcengineRegistryServer.start(volcengineBaseUrl);
        MockAcrAuthServer.start(acrAuthBaseUrl);
        mockAcrRegistryServer = new MockOciRegistryServer();
        mockAcrRegistryServer.start(acrBaseUrl);
        MockArtifactoryAuthServer.start(artifactoryAuthBaseUrl);
        mockArtifactoryRegistryServer = new MockOciRegistryServer();
        mockArtifactoryRegistryServer.start(artifactoryBaseUrl);
        mockHarborRegistryServer = new MockOciRegistryServer();
        mockHarborRegistryServer.start(harborBaseUrl);
        MockHarborAuthServer.start(harborAuthBaseUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();

        var addRegistryCredentialRequestsDockerContainer = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_DOCKER_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER))
                .secret(TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var addRegistryCredentialRequestsEcrPrivate = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ECR_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                .secret(TEST_ECR_PRIVATE_SECRET_1)
                .build();
        var addRegistryCredentialRequestsEcrPublic = AddRegistryCredentialRequest.builder()
                .registryHostname(ECR_PUBLIC_REGISTRY_HOSTNAME)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                .secret(TEST_ECR_PUBLIC_SECRET_1)
                .build();
        var addRegistryCredentialRequestsVolcengine = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_VOLCENGINE_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                .secret(TEST_VOLCENGINE_SECRET_1)
                .build();
        var addRegistryCredentialRequestsAcr = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ACR_CONTAINER_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                .secret(TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var addRegistryCredentialRequestsArtifactory = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_ARTIFACTORY_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                .secret(TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var addRegistryCredentialRequestsHarbor = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_HARBOR_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                .secret(TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID);
        registryCredentialService.addRegistryCredential(
                TEST_NCA_ID, accountEntity,
                addRegistryCredentialRequestsDockerContainer, ProvisionedByEnum.USER,
                testCommonService.getAuditEventPayloadBuilder());
        registryCredentialService.addRegistryCredential(
                TEST_NCA_ID, accountEntity,
                addRegistryCredentialRequestsEcrPrivate, ProvisionedByEnum.USER,
                testCommonService.getAuditEventPayloadBuilder());
        registryCredentialService.addRegistryCredential(
                TEST_NCA_ID, accountEntity,
                addRegistryCredentialRequestsEcrPublic, ProvisionedByEnum.USER,
                testCommonService.getAuditEventPayloadBuilder());
        registryCredentialService.addRegistryCredential(
                TEST_NCA_ID, accountEntity,
                addRegistryCredentialRequestsVolcengine, ProvisionedByEnum.USER,
                testCommonService.getAuditEventPayloadBuilder());
        registryCredentialService.addRegistryCredential(
                TEST_NCA_ID, accountEntity,
                addRegistryCredentialRequestsAcr, ProvisionedByEnum.USER,
                testCommonService.getAuditEventPayloadBuilder());
        registryCredentialService.addRegistryCredential(
                TEST_NCA_ID, accountEntity,
                addRegistryCredentialRequestsArtifactory, ProvisionedByEnum.USER,
                testCommonService.getAuditEventPayloadBuilder());
        registryCredentialService.addRegistryCredential(
                TEST_NCA_ID, accountEntity,
                addRegistryCredentialRequestsHarbor, ProvisionedByEnum.USER,
                testCommonService.getAuditEventPayloadBuilder());
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockDockerRegistryServer.stop();
        MockDockerRegistryAuthServer.stop();
        MockEcrPrivateRegistryServer.stop();
        MockEcrPublicRegistryServer.stop();
        MockVolcengineRegistryServer.stop();
        MockAcrAuthServer.stop();
        mockAcrRegistryServer.stop();
        MockArtifactoryAuthServer.stop();
        mockArtifactoryRegistryServer.stop();
        MockHarborAuthServer.stop();
        mockHarborRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        resetToDefault();
    }

    Stream<Arguments> functionCreateArgs() {
        return Stream.of(

                /* --- Docker ---*/
                // existing docker image
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_DOCKER_CONTAINER_IMAGE,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // docker image with digest
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_DOCKER_CONTAINER_IMAGE_WITH_DIGEST,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // docker image does not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_DOCKER_CONTAINER_IMAGE_NOT_EXISTS,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),
                // docker image no permission
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_DOCKER_CONTAINER_IMAGE_PERMISSION_DENIED,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.FORBIDDEN),

                /* --- NGC ---*/
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                            SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             null,
                             TEST_MODEL_URL_UNKNOWN_REGISTRY_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                // When the protocol missing, we will auto attach https:// to make it works by default.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                            SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             null,
                             TEST_MODEL_URL_MISSING_PROTOCOL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                            SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_UNKNOWN_REGISTRY_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                            SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_NGC_CONTAINER_IMAGE,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_MISSING_PROTOCOL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),

                /* --- ECR ---*/
                // existing ECR container image
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_CONTAINER_IMAGE_WITH_TAG,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // existing ECR container image with digest
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_CONTAINER_IMAGE_WITH_DIGEST,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // ECR container image tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_CONTAINER_IMAGE_TAG_NOT_FOUND,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),
                // ECR container image digest not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_CONTAINER_IMAGE_DIGEST_NOT_FOUND,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),
                // ECR container image permission denied
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_CONTAINER_IMAGE_PERMISSION_DENIED,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.FORBIDDEN),

                // existing ECR helm chart
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ECR_HELM_CHART_WITH_TAG,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // existing ECR helm chart with digest
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ECR_HELM_CHART_WITH_DIGEST,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // ECR helm chart tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ECR_HELM_CHART_TAG_NOT_FOUND,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),
                // ECR helm chart digest not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ECR_HELM_CHART_DIGEST_NOT_FOUND,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),

                /* --- ECR Public ---*/
                // existing ECR public container image
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_TAG,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // existing ECR public container image with digest
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_PUBLIC_CONTAINER_IMAGE_WITH_DIGEST,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // ECR public container image tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_PUBLIC_CONTAINER_IMAGE_TAG_NOT_FOUND,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),
                // ECR public container image digest not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_PUBLIC_CONTAINER_IMAGE_DIGEST_NOT_FOUND,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),
                // ECR public container image permission denied
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ECR_PUBLIC_CONTAINER_IMAGE_PERMISSION_DENIED,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.FORBIDDEN),

                // existing ECR public helm chart
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ECR_PUBLIC_HELM_CHART_WITH_TAG,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // existing ECR public helm chart with digest
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ECR_PUBLIC_HELM_CHART_WITH_DIGEST,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // ECR public helm chart tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ECR_PUBLIC_HELM_CHART_TAG_NOT_FOUND,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),
                // ECR public helm chart digest not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ECR_PUBLIC_HELM_CHART_DIGEST_NOT_FOUND,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.BAD_REQUEST),

                /* --- Volcengine ---*/
                // existing volcengine container image
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_VOLCENGINE_CONTAINER_IMAGE_WITH_TAG,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // volcengine container image tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_VOLCENGINE_CONTAINER_IMAGE_TAG_NOT_FOUND,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),

                // existing volcengine helm chart
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_VOLCENGINE_HELM_CHART_WITH_TAG,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // volcengine helm chart tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_VOLCENGINE_HELM_CHART_TAG_NOT_FOUND,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),

                /* --- ACR ---*/
                // existing ACR container image
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ACR_CONTAINER_IMAGE_WITH_TAG,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // ACR container image tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ACR_CONTAINER_IMAGE_NOT_EXISTS,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),

                // existing ACR helm chart
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ACR_HELM_CHART_WITH_TAG,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // ACR helm chart tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ACR_HELM_CHART_NOT_EXISTS,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),

                /* --- Artifactory ---*/
                // existing artifactory container image
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ARTIFACTORY_CONTAINER_IMAGE_WITH_TAG,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // Artifactory container image tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_ARTIFACTORY_CONTAINER_IMAGE_NOT_EXISTS,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),

                // existing artifactory helm chart
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ARTIFACTORY_HELM_CHART_WITH_TAG,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // artifactory helm chart tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_ARTIFACTORY_HELM_CHART_NOT_EXISTS,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),

                /* --- Harbor ---*/
                // existing harbor container image
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_HARBOR_CONTAINER_IMAGE_WITH_TAG,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_HARBOR_CONTAINER_IMAGE_WITH_DIGEST,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // harbor container image tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_HARBOR_CONTAINER_IMAGE_NOT_EXISTS,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),
                // harbor container image mismatch with credentials
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             TEST_HARBOR_CONTAINER_IMAGE_PERMISSION_DENIED,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.FORBIDDEN),
                // existing harbor helm chart
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_HARBOR_HELM_CHART_WITH_TAG,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_HARBOR_HELM_CHART_WITH_DIGEST,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.OK),
                // harbor helm chart tag not exist
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_HARBOR_HELM_CHART_NOT_EXISTS,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.NOT_FOUND),
                // harbor helm chart mismatch with credentials
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION),
                                                             100),
                             null,
                             TEST_HARBOR_HELM_CHART_PERMISSION_DENIED,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             null,
                             HttpStatus.FORBIDDEN),

                /* --- Unknown Registry ---*/
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                            SCOPE_LIST_FUNCTIONS),
                                                             100),
                             TEST_CONTAINER_IMAGE_UNKNOWN_REGISTRY,
                             null,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION,
                                                            SCOPE_LIST_FUNCTIONS),
                                                             100),
                             null,
                             TEST_HELM_CHART_UNKNOWN_REGISTRY,
                             TEST_MODEL_URL_1,
                             TEST_RESOURCE_URL_1,
                             TEST_HEALTH_ENDPOINT,
                             TEST_TAGS,
                             HttpStatus.BAD_REQUEST)
                        );
    }

    @ParameterizedTest
    @MethodSource("functionCreateArgs")
    void shouldCreateFunction(
            String token, URI containerImage, URI helmChartUrl, String modelUri,
            String resourceUri, String healthUri, Set<String> tags, HttpStatus expectedStatus) {
        var model1Name = "model-1";
        var model1Version = "1.0";
        var modelDtos = List.of(FunctionModelDto.builder().name(model1Name)
                                        .version(model1Version).uri(URI.create(modelUri)).build());
        HealthDto healthDto = null;
        if (healthUri != null) {
            healthDto = HealthDto.builder()
                    .expectedStatusCode(EXPECTED_STATUS_CODE)
                    .timeout(HEALTH_TIMEOUT)
                    .port(TEST_INFERENCE_PORT)
                    .protocol(ProtocolEnum.HTTP)
                    .uri(URI.create(healthUri))
                    .build();
        }
        var requestBodyBuilder = CreateFunctionRequest.builder()
                .name(TEST_FUNCTION_NAME)
                .apiBodyFormat(PREDICT_V2)
                .inferenceUrl(TEST_INFERENCE_URL)
                .inferencePort(TEST_INFERENCE_PORT)
                .models(modelDtos)
                .tags(tags)
                .description(TEST_DESCRIPTION)
                .health(healthDto);

        if (containerImage != null) {
            requestBodyBuilder
                    .containerArgs(TEST_CONTAINER_ARGS)
                    .containerImage(containerImage);
        } else {
            requestBodyBuilder
                    .helmChart(helmChartUrl)
                    .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME);
        }

        var requestBody = requestBodyBuilder.build();
        Set<ArtifactDto> resourceDtos = null;
        var resource1Name = "resource-1";
        var resource1Version = "1.0";
        if (resourceUri != null) {
            resourceDtos = Set.of(
                    ArtifactDto.builder().name(resource1Name)
                            .version(resource1Version)
                            .uri(URI.create(resourceUri))
                            .build());
            requestBody.setResources(resourceDtos);
        }
        var requestEntity = RequestEntity.post(URI.create("/v2/nvcf/functions"))
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, CreateFunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isNotNull();
        assertThat(responseBody.function().name()).isEqualTo(TEST_FUNCTION_NAME);
        assertThat(responseBody.function().versionId()).isNotNull();
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
        assertThat(responseBody.function().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.function().apiBodyFormat()).isEqualTo(PREDICT_V2);
        if (containerImage != null) {
            assertThat(responseBody.function().containerArgs()).isEqualTo(TEST_CONTAINER_ARGS);
            assertThat(responseBody.function().containerImage()).isEqualTo(containerImage);
        } else {
            assertThat(responseBody.function().helmChart()).isEqualTo(helmChartUrl);
            assertThat(responseBody.function().helmChartServiceName()).isEqualTo(
                    TEST_HELM_CHART_SERVICE_NAME);
        }

        assertThat(responseBody.function().containerEnvironment()).isNull();
        assertThat(responseBody.function().createdAt()).isNotNull();
        assertThat(responseBody.function().inferenceUrl()).isEqualTo(TEST_INFERENCE_URL);
        assertThat(responseBody.function().inferencePort()).isEqualTo(TEST_INFERENCE_PORT);
        assertThat(responseBody.function().functionType()).isEqualTo(FunctionTypeEnum.DEFAULT);
        assertThat(responseBody.function().models()).isNotNull().hasSize(1);
        assertThat(responseBody.function().models()).isEqualTo(modelDtos);
        assertThat(responseBody.function().resources()).isNotNull().hasSize(1);
        assertThat(responseBody.function().resources()).isEqualTo(resourceDtos);
        assertThat(responseBody.function().tags()).isEqualTo(tags);
        assertThat(responseBody.function().description()).isEqualTo(TEST_DESCRIPTION);
        assertThat(responseBody.function().health()).isNotNull();
        assertThat(responseBody.function().health().getProtocol()).isEqualTo(
                ProtocolEnum.HTTP);
        assertThat(responseBody.function().healthUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().health().getUri()).isEqualTo(TEST_HEALTH_URI);
        assertThat(responseBody.function().health().getTimeout()).isEqualTo(HEALTH_TIMEOUT);
        assertThat(responseBody.function().health().getExpectedStatusCode()).isEqualTo(
                EXPECTED_STATUS_CODE);

        var functionId = responseBody.function().id();
        var versionId = responseBody.function().versionId();
        var entity = functionsRepository
                .getByFunctionVersionId(versionId)
                .orElseThrow(() -> new NotFoundException("Function not found"));
        assertThat(entity).isNotNull();
        assertThat(entity.getUtilsContainerImage())
                .isNotBlank()
                .isEqualTo(GO);
        var savedModels = functionMapperService.toFunctionModels(entity.getModelSpecs());
        assertThat(savedModels).hasSize(1);
        assertThat(savedModels.get(0).getName()).isEqualTo(model1Name);
        assertThat(savedModels.get(0).getVersion()).isEqualTo(model1Version);
        assertThat(savedModels.get(0).getUri()).isEqualTo(URI.create(modelUri));
        assertThat(entity.getResources()).hasSize(1);
        assertThat(entity.getResources()).contains(
                ResourceUdt.builder()
                        .url(resourceUri)
                        .name(resource1Name)
                        .version(resource1Version)
                        .build()
                                                  );
        assertThat(entity.getFunctionType()).isEqualTo(FunctionType.DEFAULT);
    }
}
