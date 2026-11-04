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
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_LIST_FUNCTIONS;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_REQUEST_CONCURRENCY;
import static com.nvidia.nvcf.util.TestConstants.A10G;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.OCI;
import static com.nvidia.nvcf.util.TestConstants.OCI_L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SUPPORTED_INSTANCE_TYPES;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CPU_ARCH;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_DRIVER_VERSION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_MEMORY;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_OS;
import static com.nvidia.nvcf.util.TestConstants.TEST_STORAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_SYSTEM_MEMORY;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestUtil.buildHelmValidationPolicyDto;
import static com.nvidia.nvcf.util.TestUtil.createAutoscalingConfigDto;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentDto;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.ListDeploymentsResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.util.List;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
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
class XAccountFunctionDeploymentControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private FunctionDeploymentLookupService functionDeploymentLookupService;

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
        testQueueService.clearQueues();
        resetToDefault();
    }

    Stream<Arguments> gpuSpecDeploymentArgs() {
        var validGpuSpecs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var validGpuSpecsOneZeroScale = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var validGpuSpecsBothZeroScale = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var validGpuSpecsWithBackend = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(OCI)
                        .instanceType(OCI_L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithZeroInstanceCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(0).minInstances(0).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithNegativeInstanceCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(-1).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMinGreaterThanMaxCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(2).minInstances(4).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMissingInstanceType = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMissingGpu = List.of(
                GpuSpecificationDto.builder()
                        .instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMissingMaxInstances = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .minInstances(2).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMissingMinInstances = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithDefaultMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithZeroMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(0)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithNegativeMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(1)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(-1)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithGreaterThanMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5)
                        .maxRequestConcurrency(MAX_REQUEST_CONCURRENCY + 1)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(1)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithInvalidInstanceType = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .instanceType("invalid.instance.type")
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(OCI)
                        .instanceType(OCI_L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var validSpecsDuplicate = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var invalidSpecWithNonEmptyConfigurations = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .clusters(Set.of("cluster01", "cluster02"))
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .configuration(jsonMapper.createObjectNode().put("foo", "bar"))
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var invalidSpecWithHelmValidationPolicy = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .clusters(Set.of("cluster01"))
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .helmValidationPolicy(buildHelmValidationPolicyDto())
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(OCI)
                        .clusters(Set.of("cluster01"))
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());

        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecsOneZeroScale,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecsBothZeroScale,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecsWithBackend,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithZeroInstanceCount,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithNegativeInstanceCount,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMinGreaterThanMaxCount,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMissingInstanceType,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMissingGpu,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMissingMaxInstances,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithMissingMinInstances,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithDefaultMaxRequestConcurrency,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithZeroMaxRequestConcurrency,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithNegativeMaxRequestConcurrency,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithGreaterThanMaxRequestConcurrency,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             specsWithInvalidInstanceType,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validSpecsDuplicate,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             invalidSpecWithNonEmptyConfigurations,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             invalidSpecWithHelmValidationPolicy,
                             HttpStatus.BAD_REQUEST)
        );
    }

    Stream<Arguments> authDeploymentArgs() {
        var validGpuSpecs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10)
                        .backend(GFN).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(8).minInstances(6).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G)
                        .backend(GFN).instanceType(L40G_INSTANCE_TYPE).maxInstances(9)
                        .minInstances(7).maxRequestConcurrency(100)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());

        return Stream.of(
                Arguments.of(null, TEST_NCA_ID, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             validGpuSpecs, HttpStatus.UNAUTHORIZED),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("deploy_function"), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource({"authDeploymentArgs", "gpuSpecDeploymentArgs"})
    void shouldCreateFunctionDeployment(
            Object tokenSupplier, String ncaId, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> specs, HttpStatus expectedStatus) {
        // Create functions.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                             TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        var token = getToken(tokenSupplier);
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var builder = RequestEntity.post("/v2/nvcf/accounts/" + ncaId
                                                 + "/deployments/functions/" + functionId
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
        assertThat(responseBody.deployment().functionId()).isEqualTo(functionId);
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(functionVersionId);
        var isZeroScaleFunction = specs.stream().allMatch(spec -> spec.minInstances() == 0);
        var status = isZeroScaleFunction ? FunctionStatusEnum.ACTIVE : DEPLOYING;
        assertThat(responseBody.deployment().functionStatus()).isEqualTo(status);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(ncaId);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSameSizeAs(specs);

        var depSpecs = responseBody.deployment().deploymentSpecifications();
        depSpecs.forEach(depSpec -> {
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
            if (SUPPORTED_INSTANCE_TYPES.contains(depSpec.instanceType())) {
                assertThat(depSpec.cpuArch()).isEqualTo(TEST_CPU_ARCH);
                assertThat(depSpec.os()).isEqualTo(TEST_OS);
                assertThat(depSpec.driverVersion()).isEqualTo(TEST_DRIVER_VERSION);
                assertThat(depSpec.storage()).isEqualTo(TEST_STORAGE);
                assertThat(depSpec.systemMemory()).isEqualTo(TEST_SYSTEM_MEMORY);
                assertThat(depSpec.gpuMemory()).isEqualTo(TEST_GPU_MEMORY);
            }
            assertThat(depSpec.autoscalingConfiguration()).isNotNull();
            assertThat(jsonMapper.convertValue(depSpec.autoscalingConfiguration(),
                                               AutoscalingConfigurationDto.class))
                    .isEqualTo(createAutoscalingConfigDto());
        });
    }

    @ParameterizedTest
    @MethodSource("authDeploymentArgs")
    void shouldDeleteFunctionDeployment(
            Object tokenSupplier, String ncaId, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> ignoredSpecs, HttpStatus expectedStatus) {
        // Create functions with DEPLOYING status.
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID_2, TEST_FUNCTION_NAME_2,
                FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID,
                UUID.randomUUID(), UUID.randomUUID());
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_DEPLOYMENT_ID_2, TEST_NCA_ID_2,
                UUID.randomUUID(), UUID.randomUUID());

        var token = getToken(tokenSupplier);
        // Delete function deployment
        var builder = RequestEntity.delete("/v2/nvcf/accounts/" + ncaId
                                                   + "/deployments/functions/" + functionId
                                                   + "/versions/" + functionVersionId);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);

        // Verify resources and properties.
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContextOpt =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);
        var deploymentEntity = deploymentContextOpt.map(FunctionDeploymentContext::deployment);

        if (expectedStatus.isError()) {
            assertThat(function).isNotNull();
            assertThat(function).isPresent();
            assertThat(function.get().getFunctionStatus()).isEqualTo(FunctionStatus.DEPLOYING);
            assertThat(deploymentEntity).isNotNull();
            assertThat(deploymentEntity).isPresent();
            assertThat(deploymentEntity.get().getKey().getFunctionVersionId())
                    .isEqualTo(TEST_VERSION_ID_1);
            assertThat(deploymentEntity.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(deploymentEntity.get().getNcaId()).isEqualTo(TEST_NCA_ID);
            assertThat(deploymentContextOpt.orElseThrow().gpuSpecs()).hasSize(2);
            return;
        }

        assertThat(function).isNotNull();
        assertThat(function).isPresent();
        assertThat(function.get().getFunctionStatus()).isEqualTo(FunctionStatus.INACTIVE);
        assertThat(deploymentEntity).isEmpty();

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(functionId);
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
    }

    Stream<Arguments> gracefulDeleteDeploymentArgs() {
        return Stream.of(
                Arguments.of("on"),
                Arguments.of("yes"),
                Arguments.of("true"),
                Arguments.of("1"),
                Arguments.of("0"),
                Arguments.of("false"),
                Arguments.of("no"),
                Arguments.of("off")
        );
    }
    @ParameterizedTest
    @MethodSource("gracefulDeleteDeploymentArgs")
    void deleteFunctionDeploymentUsingGracefulQueryParam(String graceful) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create entry in functions_deployment_v2 table for the function.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        testQueueService.addInvocationToQueue(TEST_FUNCTION_ID, TEST_VERSION_ID_1);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Delete function deployment
        var requestEntity = RequestEntity.delete(
                "/v2/nvcf/accounts/" + TEST_NCA_ID
                        + "/deployments/functions/" + TEST_FUNCTION_ID
                        + "/versions/" + TEST_VERSION_ID_1 + "?graceful=" + graceful)
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, FunctionResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Verify resources and properties.
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContextOpt =
                functionDeploymentLookupService
                        .getDeploymentContextByDeploymentId(TEST_DEPLOYMENT_ID);
        var deploymentEntity = deploymentContextOpt.map(FunctionDeploymentContext::deployment);

        var trueValues = Set.of("true", "on", "yes", "1");

        if (trueValues.contains(graceful)) {
            assertThat(function).isNotNull();
            assertThat(function).isPresent();
            assertThat(function.get().getFunctionStatus()).isEqualTo(FunctionStatus.INACTIVE);
            assertThat(deploymentEntity).isNotNull();
            assertThat(deploymentEntity).isPresent();
            assertThat(deploymentEntity.get().getKey().getFunctionVersionId())
                    .isEqualTo(TEST_VERSION_ID_1);
            assertThat(deploymentEntity.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(deploymentEntity.get().getNcaId()).isEqualTo(TEST_NCA_ID);
            assertThat(deploymentContextOpt.get().gpuSpecs()).hasSize(2);
        } else {
            assertThat(function).isNotNull();
            assertThat(function).isPresent();
            assertThat(function.get().getFunctionStatus()).isEqualTo(FunctionStatus.INACTIVE);
            assertThat(deploymentEntity).isEmpty();
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.function().id()).isEqualTo(TEST_FUNCTION_ID);
        assertThat(responseBody.function().status()).isEqualTo(FunctionStatusEnum.INACTIVE);
    }

    @ParameterizedTest
    @MethodSource("authDeploymentArgs")
    void shouldListFunctionDeployment(
            Object tokenSupplier, String ncaId, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> ignoredSpecs, HttpStatus expectedStatus) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = getToken(tokenSupplier);
        // Get function deployment
        var builder = RequestEntity
                .get("/v2/nvcf/accounts/" + ncaId
                             + "/deployments/functions/" + functionId
                             + "/versions/" + functionVersionId);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployment().functionId()).isEqualTo(functionId);
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(functionVersionId);
        assertThat(responseBody.deployment().functionStatus()).isEqualTo(DEPLOYING);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(ncaId);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSize(2);
        responseBody.deployment().deploymentSpecifications().forEach(deploymentSpec -> {
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

    @ParameterizedTest
    @MethodSource("authDeploymentArgs")
    void shouldUpdateFunctionDeployment(
            Object tokenSupplier, String ncaId, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> specs, HttpStatus expectedStatus) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table using the following specs:
        //    - T10, GFN, 4, 4
        //    - L40G, GFN, 5, 5
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // The above call creates a function deployment with two GPU specs with gpu-spec-ids
        // TEST_GPU_SPEC_ID(for T10) and TEST_GPU_SPEC_ID_2(for L40G). The gpu-spec DTOs that are
        // passed in do not have gpu-spec-id set. Set TEST_GPU_SPEC_ID and TEST_GPU_SPEC_ID_2 in
        // the DTOs that are passed in. By setting the gpu-spec-id selectively just for this
        // deprecated endpoint, we don't set gpu-spec-ids unnecessarily for other endpoints. It
        // is plain wrong to specify gpu-spec-id when creating a new deployment. When we delete
        // the legacy Update Function Deployment endpoint, this test will get deleted.
        var gpuSpecificationDtosWithIds = List.of(
                specs.get(0).toBuilder().gpuSpecificationId(TEST_GPU_SPEC_ID).build(),
                specs.get(1).toBuilder().gpuSpecificationId(TEST_GPU_SPEC_ID_2).build()
        );

        var token = getToken(tokenSupplier);
        // Update function deployment
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(gpuSpecificationDtosWithIds).build();

        var builder = RequestEntity
                .put("/v2/nvcf/accounts/" + ncaId
                             + "/deployments/functions/" + functionId
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
        assertThat(responseBody.deployment().functionId()).isEqualTo(functionId);
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(functionVersionId);
        assertThat(responseBody.deployment().functionStatus()).isEqualTo(DEPLOYING);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(ncaId);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSize(2);

        var updatedSpecs = responseBody.deployment().deploymentSpecifications();
        assertThat(updatedSpecs.getFirst().backend()).isEqualTo(GFN);
        assertThat(updatedSpecs.getFirst().gpu()).isIn(T10, L40G);
        assertThat(updatedSpecs.getFirst().maxInstances()).isIn(9, 8);
        assertThat(updatedSpecs.getFirst().minInstances()).isIn(7, 6);
        assertThat(updatedSpecs.getFirst().maxRequestConcurrency()).isIn(specs.stream().map(
                GpuSpecificationDto::maxRequestConcurrency).collect(Collectors.toSet()));
        assertThat(updatedSpecs.get(1).gpu()).isIn(T10, L40G);
        assertThat(updatedSpecs.get(1).maxInstances()).isIn(9, 8);
        assertThat(updatedSpecs.get(1).minInstances()).isIn(7, 6);
        assertThat(updatedSpecs.get(1).maxRequestConcurrency()).isIn(specs.stream().map(
                GpuSpecificationDto::maxRequestConcurrency).collect(Collectors.toSet()));

        updatedSpecs.forEach(updatedSpec -> {
            if (SUPPORTED_INSTANCE_TYPES.contains(updatedSpec.instanceType())) {
                assertThat(updatedSpec.cpuArch()).isEqualTo(TEST_CPU_ARCH);
                assertThat(updatedSpec.os()).isEqualTo(TEST_OS);
                assertThat(updatedSpec.driverVersion()).isEqualTo(TEST_DRIVER_VERSION);
                assertThat(updatedSpec.storage()).isEqualTo(TEST_STORAGE);
                assertThat(updatedSpec.systemMemory()).isEqualTo(TEST_SYSTEM_MEMORY);
                assertThat(updatedSpec.gpuMemory()).isEqualTo(TEST_GPU_MEMORY);
            }
            // autoscaler config should not be updated by deprecated method
            assertThat(updatedSpec.autoscalingConfiguration()).isNull();
        });
    }

    Stream<Arguments> authUpdateGpuSpecificationArgs() {
        var validGpuSpec = UpdateGpuSpecificationRequest.builder()
                .minInstances(0).maxInstances(10).build();

        return Stream.of(
                Arguments.of(null,
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validGpuSpec,
                             HttpStatus.UNAUTHORIZED),
                Arguments.of("nvapi-stg-key",
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validGpuSpec,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validGpuSpec,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_LIST_FUNCTIONS), 100),
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validGpuSpec,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of("deploy_function"), 100),
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validGpuSpec,
                             HttpStatus.FORBIDDEN),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validGpuSpec,
                             HttpStatus.OK)
        );
    }

    Stream<Arguments> gpuUpdateSpecArgs() {
        var validSpec =
                UpdateGpuSpecificationRequest.builder().minInstances(0).maxInstances(10).build();
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(ADMIN_SCOPE_DEPLOY_FUNCTION),
                                                    100);
        return Stream.of(
                // 1. Valid update spec
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID, validSpec, HttpStatus.OK),
                // 2. invalid deployment id
                Arguments.of(token, UUID.randomUUID(),
                             TEST_GPU_SPEC_ID, validSpec, HttpStatus.NOT_FOUND),
                // 3. invalid gpu spec id
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             UUID.randomUUID(), validSpec, HttpStatus.NOT_FOUND),
                // 4. empty deployment id
                Arguments.of(token, null,
                             TEST_GPU_SPEC_ID, validSpec, HttpStatus.BAD_REQUEST),
                // 5. empty gpu spec id
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             null, validSpec, HttpStatus.BAD_REQUEST),
                // 6. max < min
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID, UpdateGpuSpecificationRequest.builder()
                                     .minInstances(10).maxInstances(0)
                                     .build(), HttpStatus.BAD_REQUEST),
                // 7. negative
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID, UpdateGpuSpecificationRequest.builder()
                                     .minInstances(-10).maxInstances(-5)
                                     .build(), HttpStatus.BAD_REQUEST),
                // 8. negative empty
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID, UpdateGpuSpecificationRequest.builder().build(),
                             HttpStatus.BAD_REQUEST),
                // 9. positive only one field is specified - min <= max
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID, UpdateGpuSpecificationRequest.builder()
                                     .minInstances(3).build(),
                             HttpStatus.OK),
                // 9. positive only one field is specified - min < max
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID, UpdateGpuSpecificationRequest.builder()
                                     .minInstances(100).build(),
                             HttpStatus.BAD_REQUEST),
                // 11. positive only autoscaler config
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID, UpdateGpuSpecificationRequest.builder()
                                     .autoscalingConfiguration(createAutoscalingConfigDto())
                                     .build(),
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource({"authUpdateGpuSpecificationArgs", "gpuUpdateSpecArgs"})
    void shouldUpdateGpuSpecification(
            Object tokenSupplier, UUID deploymentId,
            UUID gpuSpecId, UpdateGpuSpecificationRequest requestBody, HttpStatus expectedStatus) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table using the following specs:
        //    - T10, GFN, 4, 4
        //    - L40G, GFN, 5, 5
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = getToken(tokenSupplier);
        var builder = RequestEntity
                .patch("/v2/nvcf/accounts/" + TEST_NCA_ID
                             + "/deployments/" + deploymentId
                             + "/gpu-specifications/" + gpuSpecId)
                .contentType(MediaType.APPLICATION_JSON);
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }

        var requestEntity = builder.body(requestBody);
        var responseEntity =
                testRestTemplate.exchange(requestEntity, UpdateGpuSpecificationResponse.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);

        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();

        var updatedSpec = responseBody.gpuSpecification();
        assertThat(updatedSpec).isNotNull();
        assertThat(updatedSpec.cpuArch()).isEqualTo(TEST_CPU_ARCH);
        assertThat(updatedSpec.os()).isEqualTo(TEST_OS);
        assertThat(updatedSpec.driverVersion()).isEqualTo(TEST_DRIVER_VERSION);
        assertThat(updatedSpec.storage()).isEqualTo(TEST_STORAGE);
        assertThat(updatedSpec.systemMemory()).isEqualTo(TEST_SYSTEM_MEMORY);
        assertThat(updatedSpec.gpuMemory()).isEqualTo(TEST_GPU_MEMORY);
        if (requestBody.minInstances() != null) {
            assertThat(updatedSpec.minInstances()).isEqualTo(requestBody.minInstances());
        }
        if (requestBody.maxInstances() != null) {
            assertThat(updatedSpec.maxInstances()).isEqualTo(requestBody.maxInstances());
        }
        if (requestBody.autoscalingConfiguration() != null) {
            assertThat(updatedSpec.autoscalingConfiguration()).isNotNull();
            assertThat(jsonMapper.convertValue(updatedSpec.autoscalingConfiguration(),
                                               AutoscalingConfigurationDto.class))
                    .isEqualTo(createAutoscalingConfigDto());
        }
    }

    // GPU Specs that do not match with the ones that are specified in the original function
    // deployment.
    Stream<Arguments> mismatchDeploymentSpecsArgs() {
        return Stream.of(
                Arguments.of(List.of(
                        GpuSpecificationDto.builder()
                                .gpu(A10G).backend(GFN)
                                .maxInstances(10).minInstances(8).maxRequestConcurrency(9).build(),
                        GpuSpecificationDto.builder()
                                .gpu(L40G).backend(GFN)
                                .maxInstances(12).minInstances(10)
                                .maxRequestConcurrency(99).build())),
                Arguments.of(List.of(
                        GpuSpecificationDto.builder()
                                .gpu(T10).backend(GFN)
                                .maxInstances(10).minInstances(8).maxRequestConcurrency(9).build()))
        );
    }

    @ParameterizedTest
    @MethodSource("mismatchDeploymentSpecsArgs")
    void shouldNotUpdateFunctionDeployment(List<GpuSpecificationDto> updateSpecs) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table using the following specs:
        //    - T10, GFN, 4, 4
        //    - L40G, GFN, 5, 5
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(ADMIN_SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Update function deployment using GPU/Backend specs that are different from the
        // ones that were used when the function was originally deployed.
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(updateSpecs).build();
        var requestEntity = RequestEntity
                .put("/v2/nvcf/accounts/" + TEST_NCA_ID
                             + "/deployments/functions/" + TEST_FUNCTION_ID
                             + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    Stream<Arguments> getAllFunctionDeploymentsArgs() {
        return Stream.of(
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             0, HttpStatus.OK, List.of()),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             1, HttpStatus.OK, List.of(TEST_FUNCTION_ID)),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100),
                             2, HttpStatus.OK, List.of(TEST_FUNCTION_ID, TEST_FUNCTION_ID_2)),
                Arguments.of(null, 0, HttpStatus.UNAUTHORIZED, List.of()),
                Arguments.of("nvapi-stg-key", 0, HttpStatus.FORBIDDEN, List.of()),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                             1, HttpStatus.FORBIDDEN, List.of())
        );
    }
    @ParameterizedTest
    @MethodSource("getAllFunctionDeploymentsArgs")
    void shouldGetAllFunctionDeploymentForSameAccount(
            Object tokenSupplier, int createdDeployments,
            HttpStatus expectedStatus, List<UUID> expectedFunctions) {
        if (createdDeployments == 1) {
            // Create function with DEPLOYING status.
            testService.createTestFunctionEntity(
                    TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                    FunctionStatus.DEPLOYING);

            // Create entries in functions_deployment_v2 table.
            testService.createDeploymentEntity(
                    TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);
        } else if (createdDeployments == 2) {
            testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                 TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                 FunctionStatus.DEPLOYING);
            testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                 TEST_NCA_ID, TEST_FUNCTION_NAME_2,
                                                 FunctionStatus.DEPLOYING);

            // Create entries in functions_deployment_v2 table.
            testService.createDeploymentEntity(
                    TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID,
                    UUID.randomUUID(), UUID.randomUUID());
            testService.createDeploymentEntity(
                    TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_DEPLOYMENT_ID_2, TEST_NCA_ID,
                    UUID.randomUUID(), UUID.randomUUID());
        }

        var token = getToken(tokenSupplier);
        // Get function deployments
        var builder = RequestEntity
                .get("/v2/nvcf/accounts/" + TEST_NCA_ID
                             + "/deployments");
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListDeploymentsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        
        var actualFunctionIds = responseBody.deployments().stream()
                .map(FunctionDeploymentDto::functionId)
                .toList();
        assertThat(actualFunctionIds).containsExactlyInAnyOrderElementsOf(expectedFunctions);

        for (var deployment : responseBody.deployments()) {
            assertThat(deployment.functionStatus()).isEqualTo(DEPLOYING);
            assertThat(deployment.ncaId()).isEqualTo(TEST_NCA_ID);
            assertThat(deployment.deploymentSpecifications()).hasSize(2);
        }
    }

    @Test
    void shouldNotFailListOnNullDeployment() {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.DEPLOYING);
        // Create entries in functions_deployment_v2 table.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // Get function deployment
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100);
        var builder = RequestEntity
                .get("/v2/nvcf/accounts/" + TEST_NCA_ID
                             + "/deployments")
                .header("Authorization", "Bearer " + token);
        var requestEntity = builder.build();
        var responseEntity = testRestTemplate
                .exchange(requestEntity, ListDeploymentsResponse.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployments().size()).isEqualTo(1);
    }

    @Test
    void shouldGetAllFunctionDeploymentForDifferentAccount() {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // Get deployments for NCA_ID, which has 1 deployment
        var builder = RequestEntity
                .get("/v2/nvcf/accounts/" + TEST_NCA_ID
                             + "/deployments");
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(ADMIN_SCOPE_DEPLOY_FUNCTION),
                                                    100);
        builder = builder.header("Authorization", "Bearer " + token);
        var requestEntity = builder.build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ListDeploymentsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployments()).hasSize(1);
        for (var deployment:responseBody.deployments()) {
            assertThat(deployment.functionStatus()).isEqualTo(DEPLOYING);
            assertThat(deployment.ncaId()).isEqualTo(TEST_NCA_ID);
            assertThat(deployment.deploymentSpecifications()).hasSize(2);
        }

        // Get deployments for NCA_ID_2, which has 0 deployment
        builder = RequestEntity
                .get("/v2/nvcf/accounts/" + TEST_NCA_ID_2
                             + "/deployments");
        builder = builder.header("Authorization", "Bearer " + token);
        requestEntity = builder.build();
        responseEntity = testRestTemplate.exchange(requestEntity, ListDeploymentsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.deployments()).isEmpty();
    }

    /**
     * We should not allow to deploy a function if it had a deployment if it has a non-empty
     * invocation queue. It was gracefully deleted and still has deployment record in DB.
     * BadRequest should be thrown in such case.
     */
    @Test
    public void shouldNotDeployGracefullyDeletedDeployment(){
        // Create functions and deploy it.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);

        var specs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99).build());

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                    List.of(ADMIN_SCOPE_DEPLOY_FUNCTION), 100);

        var deployRequestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var builder = RequestEntity.post("/v2/nvcf/accounts/" + TEST_NCA_ID
                                                 + "/deployments/functions/" + TEST_FUNCTION_ID
                                                 + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON);
            builder = builder.header("Authorization", "Bearer " + token);

        var deployRequestEntity = builder.body(deployRequestBody);
        var deployResponseEntity = testRestTemplate.exchange(
                deployRequestEntity, DeploymentResponse.class);

        assertThat(deployResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        testQueueService.addInvocationToQueue(TEST_FUNCTION_ID, TEST_VERSION_ID_1);

        // Gracefully delete deployment
        var deleteRequestEntity = RequestEntity.delete(
                "/v2/nvcf/accounts/" + TEST_NCA_ID
                        + "/deployments/functions/" + TEST_FUNCTION_ID
                        + "/versions/" + TEST_VERSION_ID_1 + "?graceful=true")
                .header("Authorization", "Bearer " + token)
                .build();
        var deleteResponseEntity = testRestTemplate.exchange(
                deleteRequestEntity, FunctionResponse.class);
        assertThat(deleteResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Verify resources and properties.
        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);
        assertThat(deploymentContext).isNotEmpty();

        // Try to deploy function and get 400
        var deployAgainResponseEntity = testRestTemplate.exchange(
                deployRequestEntity, DeploymentResponse.class);
        assertThat(deployAgainResponseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }
}
