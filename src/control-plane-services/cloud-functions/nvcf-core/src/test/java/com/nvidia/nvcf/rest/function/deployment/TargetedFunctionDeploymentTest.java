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
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.ACTIVE;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_REQUEST_CONCURRENCY;
import static com.nvidia.nvcf.util.NvcfUtils.filterBlankStrings;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.OCI_L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.apache.commons.lang3.StringUtils.EMPTY;
import static org.assertj.core.api.Assertions.assertThat;

import com.nimbusds.oauth2.sdk.util.CollectionUtils;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.io.IOException;
import java.util.List;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
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
import org.springframework.http.ProblemDetail;
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
class TargetedFunctionDeploymentTest {

    private static final String TEST_CLUSTER_NAME_1 = "test_cluster_1";
    private static final String TEST_CLUSTER_NAME_2 = "test_cluster_2";
    private static final String TEST_REGION_NAME_1 = "test_region_1";
    private static final String TEST_REGION_NAME_2 = "test_region_2";
    private static final String TEST_ATTRIBUTE_1 = "test_attribute_1";
    private static final String TEST_ATTRIBUTE_2 = "test_attribute_2";


    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestQueueService testQueueService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockIcmsServer.stop();
        MockCasServer.stop();
        MockEssServer.stop();
        MockNgcContainerRegistryServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockEssServer.clearSecrets();
        testQueueService.clearQueues();
    }

    Stream<Arguments> targetedGpuSpecDeploymentArgs() {
        // All the valid deployments have two specs with unique GPUs. This characteristic
        // is used to match the spec in the request with the corresponding spec in the
        // response.
        var validGpuSpecs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var validGpuSpecsWithBlankStrings = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2, EMPTY))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2, EMPTY))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2, EMPTY))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2, "   "))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2, "    "))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2, "    "))
                        .build());
        var validGpuSpecsWithBackend = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G)
                        .instanceType(OCI_L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithZeroInstanceCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(0).minInstances(0).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithNegativeInstanceCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(-1).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithMinGreaterThanMaxCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(2).minInstances(4).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithMissingInstanceType = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithMissingGpu = List.of(
                GpuSpecificationDto.builder()
                        .backend(GFN)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithMissingMaxInstances = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithMissingMinInstances = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithDefaultMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithZeroMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(0)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithNegativeMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(1)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(-1)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithGreaterThanMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5)
                        .maxRequestConcurrency(MAX_REQUEST_CONCURRENCY + 1)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(1)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var specsWithInvalidInstanceType = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .instanceType("invalid.instance.type")
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .instanceType(OCI_L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var validGpuSpecsOneZeroScale = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());
        var validGpuSpecsBothZeroScale = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .clusters(Set.of(TEST_CLUSTER_NAME_1, TEST_CLUSTER_NAME_2))
                        .regions(Set.of(TEST_REGION_NAME_1, TEST_REGION_NAME_2))
                        .attributes(Set.of(TEST_ATTRIBUTE_1, TEST_ATTRIBUTE_2))
                        .build());

        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecsWithBlankStrings,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecsOneZeroScale,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecsBothZeroScale,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecsWithBackend,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithZeroInstanceCount,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithNegativeInstanceCount,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMinGreaterThanMaxCount,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMissingInstanceType,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMissingGpu,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMissingMaxInstances,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMissingMinInstances,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithDefaultMaxRequestConcurrency,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithZeroMaxRequestConcurrency,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithNegativeMaxRequestConcurrency,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithGreaterThanMaxRequestConcurrency,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithInvalidInstanceType,
                             HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("targetedGpuSpecDeploymentArgs")
    void shouldCreateFunctionDeployment(
            String token, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> specs, HttpStatus expectedStatus)
            throws IOException {
        // Create functions in different accounts.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                             TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var builder = RequestEntity.post("/v2/nvcf/deployments/functions/" + functionId
                                                 + "/versions/" + functionVersionId)
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }

        var requestEntity = builder.body(requestBody);
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployment().functionId()).isEqualTo(TEST_FUNCTION_ID);
        var isZeroScaleFunction = specs.stream().allMatch(spec -> spec.minInstances() == 0);
        if (isZeroScaleFunction) {
            assertThat(responseBody.deployment().functionStatus()).isEqualTo(ACTIVE);
        } else {
            assertThat(responseBody.deployment().functionStatus()).isEqualTo(DEPLOYING);
        }
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSameSizeAs(specs);

        var depSpecsInResponse = responseBody.deployment().deploymentSpecifications();
        depSpecsInResponse.forEach(depSpec -> {
            assertThat(depSpec.instanceType()).isNotBlank();
            assertThat(depSpec.backend()).isIn(specs.getFirst().backend(), specs.get(1).backend());
            assertThat(depSpec.gpu()).isIn(specs.getFirst().gpu(), specs.get(1).gpu());
            assertThat(depSpec.maxRequestConcurrency())
                    .isIn(Objects.requireNonNullElse(specs.getFirst().maxRequestConcurrency(), 1),
                          Objects.requireNonNullElse(specs.get(1).maxRequestConcurrency(), 1));
            assertThat(depSpec.maxInstances())
                    .isIn(specs.getFirst().maxInstances(), specs.get(1).maxInstances());
            assertThat(depSpec.minInstances())
                    .isIn(specs.getFirst().minInstances(), specs.get(1).minInstances());
        });

        specs.forEach(spec -> {
            assertThat(depSpecsInResponse.stream()
                               .filter(dto -> dto.gpu().equals(spec.gpu()))
                               .findFirst()).isPresent();
            var gpuSpecInResponse = depSpecsInResponse.stream()
                    .filter(dto -> dto.gpu().equals(spec.gpu()))  // Tests are setup with unique GPUs.
                    .findFirst().get();

            if (CollectionUtils.isNotEmpty(spec.clusters())) {
                boolean containsBlank = spec.clusters().stream().anyMatch(String::isBlank);
                var size = containsBlank ?
                        filterBlankStrings(spec.clusters()).size() : spec.clusters().size();
                assertThat(gpuSpecInResponse.clusters()).isSubsetOf(spec.clusters()).hasSize(size);
            }

            if (CollectionUtils.isNotEmpty(spec.regions())) {
                boolean containsBlank = spec.regions().stream().anyMatch(String::isBlank);
                var size = containsBlank ?
                        filterBlankStrings(spec.regions()).size() : spec.regions().size();
                assertThat(gpuSpecInResponse.regions()).isSubsetOf(spec.regions()).hasSize(size);
            }

            if (CollectionUtils.isNotEmpty(spec.attributes())) {
                boolean containsBlank = spec.attributes().stream().anyMatch(String::isBlank);
                var size = containsBlank ?
                        filterBlankStrings(spec.attributes()).size() : spec.attributes().size();
                assertThat(gpuSpecInResponse.attributes()).isSubsetOf(spec.attributes()).hasSize(size);
            }
        });

        if (expectedStatus.is2xxSuccessful()) {
            var existingDeploymentResponseEntity = testRestTemplate.exchange(requestEntity,
                                                                             String.class);
            assertThat(existingDeploymentResponseEntity.getStatusCode())
                    .isEqualTo(HttpStatus.BAD_REQUEST);
            var problemDetail = jsonMapper.readValue(existingDeploymentResponseEntity.getBody(),
                                                     ProblemDetail.class);
            var status = isZeroScaleFunction ? ACTIVE : DEPLOYING;
            assertThat(problemDetail.getDetail())
                    .isEqualTo("Function id '%s', version '%s': Status %s, use PATCH endpoint to update gpu-specification"
                                       .formatted(functionId, functionVersionId, status));
        }
    }
}
