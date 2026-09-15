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
package com.nvidia.nvcf.rest.misc;

import static com.github.tomakehurst.wiremock.client.WireMock.delete;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static org.assertj.core.api.Assertions.assertThat;

import com.github.tomakehurst.wiremock.matching.EqualToPattern;
import com.github.tomakehurst.wiremock.matching.RequestPatternBuilder;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.function.management.dto.GpuDto;
import com.nvidia.nvcf.rest.misc.dto.GoldenImagesResponse;
import com.nvidia.nvcf.rest.misc.dto.GpuPlacementDto;
import com.nvidia.nvcf.rest.misc.dto.GpuUsageDto;
import com.nvidia.nvcf.rest.misc.dto.ListGpuUsageResponse;
import com.nvidia.nvcf.rest.misc.dto.SidecarProperties;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.assertj.core.api.Assertions;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
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
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.core.JacksonException;
import tools.jackson.core.type.TypeReference;
import tools.jackson.databind.json.JsonMapper;
import org.apache.commons.lang3.StringUtils;

@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@TestInstance(Lifecycle.PER_CLASS)
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class XAccountMiscEndpointsControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestQueueService testQueueService;

    @Autowired
    private FunctionsDeploymentRepository deploymentRepository;

    @Autowired
    private SidecarProperties sidecarConfig;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockIcmsServer.stop();
        MockEssServer.stop();
        MockNgcContainerRegistryServer.stop();
        MockCasServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testQueueService.clearQueues();
    }

    Stream<Arguments> args() {
        return Stream.of(
                Arguments.of(null, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100), HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_REGISTER_FUNCTION), 100),
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("admin:" + SCOPE_REGISTER_FUNCTION), 100),
                             HttpStatus.OK));
    }

    @ParameterizedTest
    @MethodSource("args")
    void shouldGetSupportedGpus(String token, HttpStatus expectedStatus)
            throws JacksonException {
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/supportedGpus"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        var responseBody = new JsonMapper().readValue(responseEntity.getBody(),
                                                        new TypeReference<List<GpuDto>>() {
                                                        });
        assertThat(responseBody).isNotEmpty();
        assertThat(responseBody.getFirst()).isInstanceOf(GpuDto.class);
    }

    Stream<Arguments> deleteInstanceArgs() {
        return Stream.of(
                Arguments.of(null, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100), HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DELETE_FUNCTION), 100),
                             HttpStatus.OK));
    }

    @ParameterizedTest
    @MethodSource("deleteInstanceArgs")
    void shouldDeleteInstance(String token, HttpStatus expectedStatus) {
        String instanceId = "abc123";
        var requestEntity = RequestEntity.delete(URI.create("/v2/nvcf/instances/" + instanceId))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        var expectedIcmsRequest = delete(urlPathEqualTo("/v1/si"))
                .withQueryParam("Action",
                                new EqualToPattern("TerminateInstances"))
                .withQueryParam("InstanceId",
                                new EqualToPattern(instanceId))
                .build()
                .getRequest();
        MockIcmsServer.getMockIcmsServer()
                .verify(1, RequestPatternBuilder.like(expectedIcmsRequest));
    }

    Stream<Arguments> gpuUsageArgs() {
        return Stream.of(
                // Arguments.of(null, HttpStatus.UNAUTHORIZED),
                // Arguments.of("nvapi-stg-key", HttpStatus.FORBIDDEN),
                // Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                //                                   List.of(), 100), HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             HttpStatus.OK));
    }

    @ParameterizedTest
    @MethodSource("gpuUsageArgs")
    void shouldListGpuUsageForAccount(String token, HttpStatus expectedStatus) {
        // Create functions with DEPLOYING status in two different accounts.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME_2,
                                             FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                             TEST_NCA_ID_3, TEST_FUNCTION_NAME_3,
                                             FunctionStatus.DEPLOYING);

        // Create deployment specifications for the functions.
        var gpuSpecs1 = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                     .build())
                        .backend("BYOC-OCI-1").gpu("A100_80GB").instanceType("BM.GPU.A100-v2.8")
                        .maxInstances(100).minInstances(2).build(),
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID_2)
                                     .build())
                        .backend("GFN").gpu("L40G").instanceType("gl40g_1.br25_2xlarge")
                        .maxInstances(150).minInstances(2).build());
        var gpuSpecs2 = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(UUID.randomUUID())
                                     .build())
                        .backend("BYOC-OCI-2").gpu("A100_80GB").instanceType("BM.GPU.A100-v2.8")
                        .maxInstances(200).minInstances(2).build(),
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(UUID.randomUUID())
                                     .build())
                        .backend("GFN").gpu("T10").instanceType("g6.full")
                        .maxInstances(250).minInstances(5).build(),
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(UUID.randomUUID())
                                     .build())
                        .backend("nvcf-dgxc-k8s-aws-use1-dev1")
                        .gpu("H100").instanceType("AWS.GPU.H100_4x")
                        .maxInstances(10).minInstances(5).build());
        var gpuSpecs3 = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID_3)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(UUID.randomUUID())
                                     .build())
                        .backend("OCI-1").gpu("A100_80GB").instanceType("BM.GPU.A100-v2.8")
                        .maxInstances(100).minInstances(2).build(),
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID_3)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(UUID.randomUUID())
                                     .build())
                        .backend("GFN").gpu("L40G").instanceType("gl40g_1.br25_2xlarge")
                        .maxInstances(200).minInstances(2).build(),
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID_3)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(UUID.randomUUID())
                                     .build())
                        .backend("GFN").gpu("T10").instanceType("g6.full")
                        .maxInstances(300).minInstances(5).build());

        // Create queues and entries in functions_deployment_v2 table for the functions.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs1);
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_DEPLOYMENT_ID_2, TEST_NCA_ID,
                gpuSpecs2);
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID_3, TEST_VERSION_ID_3, UUID.randomUUID(), TEST_NCA_ID_3,
                gpuSpecs3);

        // Invoke endpoint to get GPU usage in TEST_NCA_ID account
        var requestEntity = RequestEntity
                .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/usage/gpus"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListGpuUsageResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var dtos = responseBody.gpus();
        assertThat(dtos).hasSize(4);
        dtos.forEach(dto -> {
            assertThat(dto.gpu()).isIn("A100_80GB", "T10", "L40G", "H100");
            assertThat(dto.instanceType()).isNotBlank();
            switch (dto.gpu()) {
                case "A100_80GB" -> {
                    assertThat(dto.currentMinUsage()).isEqualTo(4);
                    assertThat(dto.currentMaxUsage()).isEqualTo(300);
                }
                case "T10" -> {
                    assertThat(dto.currentMinUsage()).isEqualTo(5);
                    assertThat(dto.currentMaxUsage()).isEqualTo(250);
                }
                case "L40G" -> {
                    assertThat(dto.currentMinUsage()).isEqualTo(2);
                    assertThat(dto.currentMaxUsage()).isEqualTo(150);
                }
                case "H100" -> {
                    assertThat(dto.currentMinUsage()).isEqualTo(5);
                    assertThat(dto.currentMaxUsage()).isEqualTo(10);
                    assertThat(dto.placements()).isNotEmpty();
                }
                default -> Assertions.fail("Invalid GPU " + dto.gpu());
            }
        });
    }

    @Test
    void gpuUsageWithNoFunctionsDeployed() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100);
        // Invoke endpoint to get GPU usage in TEST_NCA_ID account
        var requestEntity = RequestEntity
                .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/usage/gpus"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListGpuUsageResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var dtos = responseBody.gpus();
        assertThat(dtos).isEmpty();
    }

    Stream<Arguments> gpuUsageFlow() {
        return Stream.of(
                // 1. Old flow, backend is specified
                Arguments.of(Set.of(
                                     GpuSpecificationEntity.builder()
                                             .key(GpuSpecificationKey.builder()
                                                          .ncaId(TEST_NCA_ID)
                                                          .deploymentId(TEST_DEPLOYMENT_ID)
                                                          .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                          .build())
                                             .backend("nvcf-dgxc-k8s-forge-az24-dev6")
                                             .gpu("AD102GL")
                                             .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                                             .maxInstances(1).minInstances(1).build()),
                             1,
                             1,
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1),
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1)),
                // 2. New flow, cluster list is specified
                Arguments.of(Set.of(
                                     GpuSpecificationEntity.builder()
                                             .key(GpuSpecificationKey.builder()
                                                          .ncaId(TEST_NCA_ID)
                                                          .deploymentId(TEST_DEPLOYMENT_ID)
                                                          .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                          .build())
                                             .gpu("AD102GL")
                                             .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                                             .clusters(Set.of(
                                                     "nvcf-dgxc-k8s-forge-az24-dev6",
                                                     "dgxc-k8saas-forge-dev2-az24",
                                                     StringUtils.EMPTY))
                                             .maxInstances(1).minInstances(1).build()),
                             1,
                             1,
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1,
                                    "dgxc-k8saas-forge-dev2-az24", 1),
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1,
                                    "dgxc-k8saas-forge-dev2-az24", 1)),
                // 3. New flow, no constraints
                Arguments.of(Set.of(
                                     GpuSpecificationEntity.builder()
                                             .key(GpuSpecificationKey.builder()
                                                          .ncaId(TEST_NCA_ID)
                                                          .deploymentId(TEST_DEPLOYMENT_ID)
                                                          .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                          .build())
                                             .gpu("AD102GL")
                                             .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                                             .maxInstances(1).minInstances(1).build()),
                             1,
                             1,
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1,
                                    "dgxc-k8saas-forge-dev2-az24", 1,
                                    "dgxc-k8saas-forge-az24-ct1", 1),
                             Map.of("nvcf-dgxc-k8s-forge-az24-dev6", 1,
                                    "dgxc-k8saas-forge-dev2-az24", 1,
                                    "dgxc-k8saas-forge-az24-ct1", 1))
        );
    }

    @ParameterizedTest
    @MethodSource("gpuUsageFlow")
    void gpuUsage(
            Set<GpuSpecificationEntity> gpuSpecs, int totalMaxInstances, int totalMinInstances,
            Map<String, Integer> cluster2MaxInstances,
            Map<String, Integer> cluster2MinInstances) {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100);

        // Create functions with DEPLOYING status in two different accounts.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create queues and entries in functions_deployment_v2 table for the functions.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs);

        // Invoke endpoint to get GPU usage in TEST_NCA_ID account
        var requestEntity = RequestEntity
                .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/usage/gpus"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListGpuUsageResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var dtos = responseBody.gpus();
        assertThat(dtos).hasSize(1);
        GpuUsageDto gpuUsageDto = dtos.stream().findFirst().get();
        assertThat(gpuUsageDto.instanceType()).isEqualTo("DGX-CLOUD.GPU.AD102GL_2x");
        assertThat(gpuUsageDto.currentMaxUsage()).isEqualTo(totalMaxInstances);
        assertThat(gpuUsageDto.currentMinUsage()).isEqualTo(totalMinInstances);
        var clusterList = gpuUsageDto.placements().stream().map(
                GpuPlacementDto::cluster).collect(Collectors.toSet());

        assertThat(clusterList).containsAll(cluster2MaxInstances.keySet());
        gpuUsageDto.placements().forEach(placement -> {
            assertThat(cluster2MaxInstances).containsKey(placement.cluster());
            assertThat(placement.currentMaxUsage())
                    .isEqualTo(cluster2MaxInstances.get(placement.cluster()));
        });

        assertThat(clusterList).containsAll(cluster2MinInstances.keySet());
        gpuUsageDto.placements().forEach(placement -> {
            assertThat(cluster2MinInstances).containsKey(placement.cluster());
            assertThat(placement.currentMaxUsage())
                    .isEqualTo(cluster2MinInstances.get(placement.cluster()));
        });
    }

    @Test
    void gpuUsageWithMixedFlows() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100);

        // Create functions with DEPLOYING status in the same account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME_2,
                                             FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME_3,
                                             FunctionStatus.DEPLOYING);

        // Create deployment specifications for the functions.
        // old flow, backend is specified
        var gpuSpecs1 = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                     .build())
                        .backend("nvcf-dgxc-k8s-forge-az24-dev6").gpu("AD102GL")
                        .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                        .maxInstances(1).minInstances(1).build());

        // new flow, clusters list is specified
        var gpuSpecs2 = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID_2)
                                     .build())
                        .gpu("AD102GL")
                        .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                        .clusters(Set.of(
                                "nvcf-dgxc-k8s-forge-az24-dev6",
                                "dgxc-k8saas-forge-dev2-az24",
                                StringUtils.EMPTY))
                        .maxInstances(1).minInstances(1).build());

        // new flow, no backend, no clusters, should count all available
        var gpuSpecs3 = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(UUID.randomUUID())
                                     .build())
                        .gpu("AD102GL")
                        .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                        .maxInstances(1).minInstances(1).build());

        // Create queues and entries in functions_deployment_v2 table for the functions.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs1);
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_DEPLOYMENT_ID_2, TEST_NCA_ID,
                gpuSpecs2);
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID_3, TEST_VERSION_ID_3, UUID.randomUUID(), TEST_NCA_ID, gpuSpecs3);

        // Invoke endpoint to get GPU usage in TEST_NCA_ID account
        var requestEntity = RequestEntity
                .get(URI.create("/v2/nvcf/accounts/" + TEST_NCA_ID + "/usage/gpus"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, ListGpuUsageResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var dtos = responseBody.gpus();
        assertThat(dtos).hasSize(1);
        GpuUsageDto gpuUsageDto = dtos.stream().findFirst().get();
        assertThat(gpuUsageDto.instanceType()).isEqualTo("DGX-CLOUD.GPU.AD102GL_2x");
        assertThat(gpuUsageDto.currentMaxUsage()).isEqualTo(3);
        assertThat(gpuUsageDto.currentMinUsage()).isEqualTo(3);
        gpuUsageDto.placements().forEach(placement -> {
            if (placement.clusterGroup().equals("nvcf-dgxc-k8s-forge-az24-dev6")) {
                assertThat(placement.currentMaxUsage()).isEqualTo(3);
                assertThat(placement.currentMinUsage()).isEqualTo(3);
            } else if (placement.clusterGroup().equals("dgxc-k8saas-forge-dev2-az24")) {
                assertThat(placement.currentMaxUsage()).isEqualTo(2);
                assertThat(placement.currentMinUsage()).isEqualTo(2);
            } else {
                assertThat(placement.currentMaxUsage()).isEqualTo(1);
                assertThat(placement.currentMinUsage()).isEqualTo(1);
            }
        });
    }

    Stream<Arguments> getGoldenImagesArgs() {
        return Stream.of(
                Arguments.of(null, HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key", HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(), 100), HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             HttpStatus.OK));
    }

    @ParameterizedTest
    @MethodSource("getGoldenImagesArgs")
    void shouldGetGoldenImages(String token, HttpStatus expectedStatus) {
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/sidecars/goldenimages"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, GoldenImagesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.is2xxSuccessful()) {
            var responseBody = responseEntity.getBody();
            assertThat(responseBody).isNotNull();
            var sidecars = responseBody.sidecars();
            assertThat(sidecars.inferenceContainer()).isNotBlank();
            assertThat(sidecars.initContainer()).isNotBlank();
            assertThat(sidecars.utilsContainerImage()).isNotEmpty();
            assertThat(sidecars.otelContainer()).isNotBlank();
            assertThat(sidecars.nicllsContainer()).isNotBlank();
            assertThat(sidecars.essAgentContainer()).isNotBlank();
            assertThat(sidecars.otelCollectorContainer()).isNotBlank();
        }
    }

    @Test
    void shouldGetGoldenImageWithCorrectValues() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(ADMIN_SCOPE_DEPLOY_FUNCTION),
                                                    100);
        var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/sidecars/goldenimages"))
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, GoldenImagesResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        var sidecars = responseBody.sidecars();
        assertThat(sidecars.inferenceContainer()).isEqualTo(sidecarConfig.getInferenceContainer());
        assertThat(sidecars.initContainer()).isEqualTo(sidecarConfig.getInitContainer());
        assertThat(sidecars.utilsContainerImage()).isEqualTo(
                sidecarConfig.getUtilsContainerImage());
        assertThat(sidecars.otelContainer()).isEqualTo(sidecarConfig.getOtelContainer());
        assertThat(sidecars.nicllsContainer()).isEqualTo(sidecarConfig.getNicllsContainer());
        assertThat(sidecars.essAgentContainer()).isEqualTo(sidecarConfig.getEssAgentContainer());
        assertThat(sidecars.otelCollectorContainer()).isEqualTo(
                sidecarConfig.getOtelCollectorContainer());
    }
}
