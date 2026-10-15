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

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SUPPORTED_INSTANCE_TYPES;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_CPU_ARCH;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_DRIVER_VERSION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_MEMORY;
import static com.nvidia.nvcf.util.TestConstants.TEST_HELM_CHART_SERVICE_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_OS;
import static com.nvidia.nvcf.util.TestConstants.TEST_STORAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_SYSTEM_MEMORY;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import com.nvidia.nvcf.util.MockRevalServer;
import java.net.URI;
import java.time.Instant;
import java.util.List;
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
class HelmChartBasedFunctionDeploymentTest {
    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.reval.base-url}")
    private URI revalBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    private MockRevalServer mockRevalServer;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());


        mockRevalServer = new MockRevalServer(revalBaseUrl);
        mockRevalServer.start();

        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        if (mockRevalServer != null) {
            mockRevalServer.stop();
        }
        MockApiKeysServer.stop();
        MockIcmsServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    @Test
    void shouldCreateDeploymentForHelmChartBasedFunction() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DEPLOY_FUNCTION), 100);
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.INACTIVE);

        var objectNode1 = jsonMapper.createObjectNode().put("replicas", 5);
        var objectNode2 = jsonMapper.createObjectNode().put("serviceAccountName", "nvcf");
        var specs = List.of(GpuSpecificationDto.builder()
                                    .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                                    .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                                    .configuration(objectNode1).build(),
                            GpuSpecificationDto.builder()
                                    .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                                    .maxInstances(5).minInstances(2).maxRequestConcurrency(99)
                                    .configuration(objectNode2).build());
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var requestEntity = RequestEntity.post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                        + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployment().functionId()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.deployment().functionStatus())
                .isEqualTo(FunctionStatusEnum.DEPLOYING);
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSameSizeAs(specs);
        var deploymentSpecs = responseBody.deployment().deploymentSpecifications();
        deploymentSpecs.forEach(deploymentSpec -> {
            assertThat(deploymentSpec.maxRequestConcurrency()).isIn(9, 99);
            assertThat(deploymentSpec.configuration()).isNotNull();
            assertThat(deploymentSpec.configuration().toString())
                    .isIn(objectNode1.toString(), objectNode2.toString());
            if (SUPPORTED_INSTANCE_TYPES.contains(deploymentSpec.instanceType())) {
                assertThat(deploymentSpec.cpuArch()).isEqualTo(TEST_CPU_ARCH);
                assertThat(deploymentSpec.os()).isEqualTo(TEST_OS);
                assertThat(deploymentSpec.driverVersion()).isEqualTo(TEST_DRIVER_VERSION);
                assertThat(deploymentSpec.storage()).isEqualTo(TEST_STORAGE);
                assertThat(deploymentSpec.systemMemory()).isEqualTo(TEST_SYSTEM_MEMORY);
                assertThat(deploymentSpec.gpuMemory()).isEqualTo(TEST_GPU_MEMORY);
            }
        });
    }

    @Test
    void shouldFailDeploymentForHelmChartBasedFunctionDueToReVal() {
        // In this case the second GPUSpecification will trigger a failure in the ReVal service
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DEPLOY_FUNCTION), 100);
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.INACTIVE);

        var objectNode1 = jsonMapper.createObjectNode().put("replicas", 5);
        var objectNode2 = jsonMapper.createObjectNode()
                .put("serviceAccountName", "nvcf")
                .put("fail", "fail");
        var specs = List.of(GpuSpecificationDto.builder()
                                    .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                                    .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                                    .configuration(objectNode1).build(),
                            // The Reval mock is configured to fail validation configuration field fail
                            GpuSpecificationDto.builder()
                                    .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                                    .maxInstances(42).minInstances(2).maxRequestConcurrency(99)
                                    .configuration(objectNode2).build());
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var requestEntity = RequestEntity.post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
                        + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void shouldNotFailDeploymentBecauseNcaIdNotEnabledForReval() {
        // In this case the second GPUSpecification will trigger a failure in the ReVal service
        // BUT it will be in another organization where reval is not enabled, so it will pass
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT_2,
                                                    List.of(SCOPE_DEPLOY_FUNCTION), 100);
        testService.createHelmChartBasedFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                TEST_NCA_ID_2, TEST_FUNCTION_NAME_2,
                FunctionStatus.INACTIVE);

        var objectNode1 = jsonMapper.createObjectNode().put("replicas", 5);
        var objectNode2 = jsonMapper.createObjectNode()
                .put("serviceAccountName", "nvcf")
                .put("fail", "fail");
        var specs = List.of(GpuSpecificationDto.builder()
                                    .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                                    .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                                    .configuration(objectNode1).build(),
                            // The Reval mock is configured to fail validation configuration field fail
                            GpuSpecificationDto.builder()
                                    .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                                    .maxInstances(42).minInstances(2).maxRequestConcurrency(99)
                                    .configuration(objectNode2).build());
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var requestEntity = RequestEntity.post("/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID_2
                        + "/versions/" + TEST_VERSION_ID_2)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    void shouldNotCreateDeploymentIfHelmRegistryInvalid() {
        var entity = FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID)
                .functionVersionId(TEST_VERSION_ID_1)
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(TEST_NCA_ID)
                .helmChart("invalid-helm-chart")
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

        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    Stream<Arguments> invalidHelmChartUriArgs() {
        return Stream.of(
                Arguments.of(URI.create("ftp://registry.example.com/chart.tgz")),
                Arguments.of(URI.create("file:///path/to/chart.tgz")),
                Arguments.of(URI.create("ldap://registry.example.com/chart")),
                Arguments.of(URI.create("ssh://registry.example.com/chart.tgz")),
                Arguments.of(URI.create(
                        "helm.stg.ngc.nvidia.com/test-org/charts/test-chart-1.0.0.tgz")),
                Arguments.of(URI.create(
                        "123456789000.dkr.ecr.us-west-2.amazonaws.com/test-repo/test-chart:1.0.0"))
        );
    }

    @ParameterizedTest
    @MethodSource("invalidHelmChartUriArgs")
    void shouldFailWithInvalidHelmChartUri(
            URI helmChart) {
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
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }
}
