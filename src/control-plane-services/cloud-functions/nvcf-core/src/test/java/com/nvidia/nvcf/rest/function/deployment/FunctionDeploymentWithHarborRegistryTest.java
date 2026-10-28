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
package com.nvidia.nvcf.rest.function.deployment;

import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_CONTAINER_IMAGE_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_NOT_EXISTS;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_PERMISSION_DENIED;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_WITH_DIGEST;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_HELM_CHART_WITH_TAG;
import static com.nvidia.boot.mock.BootTestConstants.TEST_HARBOR_REGISTRY;
import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_MODEL_URL_WITH_CANARY_HOST;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_RESOURCE_URL_WITH_CANARY_HOST_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_THIRD_PARTY_CONTAINER_SECRET_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.harbor.MockHarborAuthServer;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.boot.mock.oci.MockOciRegistryServer;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.ResourceUdt;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.rest.registry.dto.AddRegistryCredentialRequest;
import com.nvidia.nvcf.rest.registry.dto.ProvisionedByEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.registry.RegistryCredentialService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import com.nvidia.nvcf.util.MockRevalServer;
import java.net.URI;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
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
class FunctionDeploymentWithHarborRegistryTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private RegistryCredentialService registryCredentialService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestQueueService testQueueService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.reval.base-url}")
    private URI revalBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @Value("${nvcf.registries.recognized.container.harbor.hostname}")
    private String harborRegistryBaseUrl;
    @Value("${nvcf.registries.recognized.container.harbor.oauth2.base-url}")
    private String harborRegistryAuthBaseUrl;

    private MockRevalServer mockRevalServer;
    private MockOciRegistryServer mockOciRegistryServer;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        mockRevalServer = new MockRevalServer(revalBaseUrl);
        mockRevalServer.start();
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        MockEssServer.start(essBaseUrl);
        mockOciRegistryServer = new MockOciRegistryServer();
        mockOciRegistryServer.start(harborRegistryBaseUrl);
        MockHarborAuthServer.start(harborRegistryAuthBaseUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();

        var addRegistryCredentialRequestForHarbor = AddRegistryCredentialRequest.builder()
                .registryHostname(TEST_HARBOR_REGISTRY)
                .artifactTypes(Set.of(ArtifactTypeEnum.CONTAINER, ArtifactTypeEnum.HELM))
                .secret(TEST_THIRD_PARTY_CONTAINER_SECRET_1)
                .build();
        var accountEntity = testAccountService.getAccountByNcaId(TEST_NCA_ID);
        registryCredentialService.addRegistryCredential(
                TEST_NCA_ID, accountEntity,
                addRegistryCredentialRequestForHarbor, ProvisionedByEnum.USER,
                testCommonService.getAuditEventPayloadBuilder());
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        mockRevalServer.stop();
        MockApiKeysServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockEssServer.stop();
        MockHarborAuthServer.stop();
        mockOciRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testQueueService.clearQueues();
    }

    Stream<Arguments> containerBasedFunctionDeploymentArgs() {
        return Stream.of(
                Arguments.of(TEST_HARBOR_CONTAINER_IMAGE_WITH_TAG, HttpStatus.OK),
                Arguments.of(TEST_HARBOR_CONTAINER_IMAGE_WITH_DIGEST, HttpStatus.OK),
                Arguments.of(TEST_HARBOR_CONTAINER_IMAGE_NOT_EXISTS, HttpStatus.NOT_FOUND),
                Arguments.of(TEST_HARBOR_CONTAINER_IMAGE_PERMISSION_DENIED,
                             HttpStatus.FORBIDDEN));
    }

    @ParameterizedTest
    @MethodSource("containerBasedFunctionDeploymentArgs")
    void shouldCreateContainerBasedFunctionDeployment(
            URI containerImage,
            HttpStatus expectedStatus) {
        var entity = FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID)
                .functionVersionId(TEST_VERSION_ID_1)
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(TEST_NCA_ID)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(containerImage.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .modelSpecs(Map.of("model-1",
                                   "{\"url\":\"" + TEST_MODEL_URL_WITH_CANARY_HOST + "\"}"))
                .resources(Set.of(
                        ResourceUdt.builder()
                                .name("resource-1")
                                .url(TEST_RESOURCE_URL_WITH_CANARY_HOST_1)
                                .build()))
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .createdAt(Instant.now())
                .build();
        functionsRepository.save(entity);

        var specs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10).build());
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var builder = RequestEntity.post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                                                 + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON);
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DEPLOY_FUNCTION), 100);
        builder = builder.header("Authorization", "Bearer " + token);

        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }

    Stream<Arguments> helmBasedFunctionDeploymentArgs() {
        return Stream.of(
                Arguments.of(TEST_HARBOR_HELM_CHART_WITH_TAG, HttpStatus.OK),
                Arguments.of(TEST_HARBOR_HELM_CHART_WITH_DIGEST, HttpStatus.OK),
                Arguments.of(TEST_HARBOR_HELM_CHART_NOT_EXISTS, HttpStatus.NOT_FOUND),
                Arguments.of(TEST_HARBOR_HELM_CHART_PERMISSION_DENIED, HttpStatus.FORBIDDEN));
    }

    @ParameterizedTest
    @MethodSource("helmBasedFunctionDeploymentArgs")
    void shouldCreateHelmChartBasedFunctionDeployment(
            URI helmChart,
            HttpStatus expectedStatus) {
        var entity = FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID)
                .functionVersionId(TEST_VERSION_ID_1)
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(TEST_NCA_ID)
                .helmChart(helmChart.toString())
                .helmChartServiceName(TEST_HELM_CHART_SERVICE_NAME)
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .createdAt(Instant.now())
                .build();
        functionsRepository.save(entity);

        var specs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10).build());
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var builder = RequestEntity.post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                                                 + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON);
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DEPLOY_FUNCTION), 100);
        builder = builder.header("Authorization", "Bearer " + token);

        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }
}
