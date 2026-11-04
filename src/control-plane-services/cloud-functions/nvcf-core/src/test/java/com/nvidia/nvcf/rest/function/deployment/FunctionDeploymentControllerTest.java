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
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.util.MockApiKeysServer.resetToDefault;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_REQUEST_CONCURRENCY;
import static com.nvidia.nvcf.util.TestConstants.A10G;
import static com.nvidia.nvcf.util.TestConstants.FAKE_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.FAKE_VERSION_ID;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.OCI_L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DELETE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_DEPLOY_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SUPPORTED_INSTANCE_TYPES;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CPU_ARCH;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_DRIVER_VERSION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_MEMORY;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_OS;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_STORAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_SYSTEM_MEMORY;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestUtil.buildHelmValidationPolicyDto;
import static com.nvidia.nvcf.util.TestUtil.createAutoscalingConfigDto;
import static com.nvidia.nvcf.util.TestUtil.createHealthUdt;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.GpuSpecificationsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateFunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.io.IOException;
import java.net.URI;
import java.time.Instant;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
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
import org.springframework.http.ProblemDetail;
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
class FunctionDeploymentControllerTest {

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
    private FunctionLookupService functionLookupService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestQueueService testQueueService;

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

    @Autowired
    private FunctionDeploymentLookupService functionDeploymentLookupService;
    @Autowired
    private FunctionsDeploymentRepository functionsDeploymentRepository;
    @Autowired
    private GpuSpecificationsRepository gpuSpecificationsRepository;

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
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var validGpuSpecsWithBackend = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN)
                        .instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(2).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithZeroInstanceCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(0).minInstances(0).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithNegativeInstanceCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(-1).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMinGreaterThanMaxCount = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(2).minInstances(4).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMissingInstanceType = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMissingGpu = List.of(
                GpuSpecificationDto.builder()
                        .backend(GFN)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMissingMaxInstances = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .minInstances(2).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithMissingMinInstances = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var specsWithDefaultMaxRequestConcurrency = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(7)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .backend(GFN)
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(9)
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
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .instanceType(OCI_L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var validGpuSpecsOneZeroScale = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(0).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var validGpuSpecsBothZeroScale = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(0).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(0).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var validSpecsDuplicate = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE).backend(GFN)
                        .maxInstances(8).minInstances(7).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var invalidSpecsBothClustersBackend = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .backend(GFN).clusters(Set.of("cluster01", "cluster02"))
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .backend(GFN).clusters(Set.of("cluster01", "cluster02"))
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());
        var invalidSpecWithNonEmptyConfigurations = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .clusters(Set.of("cluster01", "cluster02"))
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .configuration(jsonMapper.createObjectNode().put("foo", "bar"))
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
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
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE).backend(GFN)
                        .clusters(Set.of("cluster01"))
                        .maxInstances(5).minInstances(0).maxRequestConcurrency(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());

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
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validSpecsDuplicate,
                             HttpStatus.OK),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             invalidSpecsBothClustersBackend,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             invalidSpecWithNonEmptyConfigurations,
                             HttpStatus.BAD_REQUEST),
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             invalidSpecWithHelmValidationPolicy,
                             HttpStatus.BAD_REQUEST)
        );
    }

    Stream<Arguments> authDeploymentArgs() {
        var validGpuSpecs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(8).minInstances(6).maxRequestConcurrency(9)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN).instanceType(L40G_INSTANCE_TYPE).maxInstances(9)
                        .minInstances(7).maxRequestConcurrency(99)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build());

        var jwtCases = Stream.of(
                // JWT Auth - Known function and version with proper scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.OK),
                // JWT Auth - Known function and version with admin scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                // JWT Auth - Known function missing scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(), 100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                // JWT Auth - Known function in different account
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                // JWT Auth - Known function with non-existent version
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION),
                                                             100),
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                // JWT Auth - Fake function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION),
                                                             100),
                             FAKE_FUNCTION_ID,
                             FAKE_VERSION_ID,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                // Missing auth for a known function
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             validGpuSpecs, HttpStatus.UNAUTHORIZED),
                // Missing auth for a fake function
                Arguments.of(null, FAKE_FUNCTION_ID, TEST_VERSION_ID_1,
                             validGpuSpecs, HttpStatus.UNAUTHORIZED)
        );
        
        var apiKeyCases = Stream.of(
                // apikey Auth - Missing scopes
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                // apikey Auth - Incorrect scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of("read:function"));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                // apikey Auth - Non-existent NCA ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID_3, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                // apikey Auth - Function in different account (account exists but function not in it)
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_2,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                // apikey Auth - non-existent function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             FAKE_FUNCTION_ID,
                             FAKE_VERSION_ID,
                             validGpuSpecs,
                             HttpStatus.NOT_FOUND),
                // apikey Auth - No resource entries in policy
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                // apikey Auth - Incorrect resource type in policy
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("authorized-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN),
                // apikey Auth - Authorized with account-functions wildcard
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.OK),
                // apikey Auth - Authorized for specific function with wildcard versions
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.OK),
                // apikey Auth - Authorized for specific function and specific version
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.OK),
                // apikey Auth - Authorized for different function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             validGpuSpecs,
                             HttpStatus.FORBIDDEN)
        );
        
        return Stream.concat(jwtCases, apiKeyCases);
    }

    @ParameterizedTest
    @MethodSource({"authDeploymentArgs", "gpuSpecDeploymentArgs"})
    void shouldCreateFunctionDeployment(
            Object tokenSupplier, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> specs, HttpStatus expectedStatus)
            throws IOException {
        // Create functions in different accounts.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                             TEST_NCA_ID_2, TEST_FUNCTION_NAME_2);

        var token = getToken(tokenSupplier);
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
            assertThat(responseBody.deployment().functionStatus()).isEqualTo(
                    FunctionStatusEnum.ACTIVE);
        } else {
            assertThat(responseBody.deployment().functionStatus()).isEqualTo(
                    FunctionStatusEnum.DEPLOYING);
        }
        assertThat(responseBody.deployment().functionVersionId()).isEqualTo(TEST_VERSION_ID_1);
        assertThat(responseBody.deployment().deploymentId()).isNotNull();
        var deploymentId = responseBody.deployment().deploymentId();
        assertThat(responseBody.deployment().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSameSizeAs(specs);

        var depSpecs = responseBody.deployment().deploymentSpecifications();
        var gpuSpecIds = new HashSet<>();
        depSpecs.forEach(depSpec -> {
            assertThat(depSpec.gpuSpecificationId()).isNotNull();
            assertThat(depSpec.gpuSpecificationId()).isNotEqualTo(deploymentId);
            assertThat(gpuSpecIds).doesNotContain(depSpec.gpuSpecificationId());
            gpuSpecIds.add(depSpec.gpuSpecificationId());
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

        if (expectedStatus.is2xxSuccessful()) {
            var existingDeploymentResponseEntity = testRestTemplate.exchange(requestEntity,
                                                                             String.class);
            assertThat(existingDeploymentResponseEntity.getStatusCode())
                    .isEqualTo(HttpStatus.BAD_REQUEST);
            var problemDetail = jsonMapper.readValue(existingDeploymentResponseEntity.getBody(),
                                                     ProblemDetail.class);
            var status = isZeroScaleFunction ? FunctionStatusEnum.ACTIVE : DEPLOYING;
            assertThat(problemDetail.getDetail()).isEqualTo(
                    ("Function id '%s', version '%s': Status %s, use PATCH endpoint to update "
                            + "gpu-specification")
                            .formatted(functionId, functionVersionId, status));
            assertThat(responseBody.deployment().functionName()).isEqualTo(TEST_FUNCTION_NAME);
            assertThat(responseBody.deployment().createdAt()).isNotNull();
            assertThat(responseBody.deployment().lastUpdatedAt()).isNotNull();
        }
    }

    @ParameterizedTest
    @MethodSource("authDeploymentArgs")
    void shouldDeleteFunctionDeployment(
            Object tokenSupplier, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> ignoredSpecs, HttpStatus expectedStatus) {
        // Create functions in different accounts with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                             TEST_NCA_ID_2, TEST_FUNCTION_NAME_2,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table for the two functions.
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, UUID.randomUUID(),
                                           UUID.randomUUID());
        testService.createDeploymentEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                           TEST_DEPLOYMENT_ID_2, TEST_NCA_ID_2, UUID.randomUUID(),
                                           UUID.randomUUID());

        var token = getToken(tokenSupplier);
        // Delete function deployment
        var builder = RequestEntity.delete("/v2/nvcf/deployments/functions/" + functionId
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
        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        if (expectedStatus.isError()) {
            assertThat(function).isNotNull();
            assertThat(function).isPresent();
            assertThat(function.get().getFunctionStatus()).isEqualTo(FunctionStatus.DEPLOYING);
            assertThat(deploymentContext).isNotNull();
            assertThat(deploymentContext).isPresent();
            var deploymentEntity = deploymentContext.get().deployment();
            assertThat(deploymentEntity.getKey().getFunctionVersionId()).isEqualTo(
                    TEST_VERSION_ID_1);
            assertThat(deploymentEntity.getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(deploymentEntity.getNcaId()).isEqualTo(TEST_NCA_ID);
            assertThat(deploymentContext.get().gpuSpecs()).hasSize(2);
            return;
        }

        assertThat(function).isNotNull();
        assertThat(function).isPresent();
        assertThat(function.get().getFunctionStatus()).isEqualTo(FunctionStatus.INACTIVE);
        assertThat(deploymentContext).isEmpty();

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

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Delete function deployment
        var requestEntity = RequestEntity.delete(
                        "/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
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
            assertThat(deploymentEntity.get().getKey().getFunctionVersionId()).isEqualTo(
                    TEST_VERSION_ID_1);
            assertThat(deploymentEntity.get().getFunctionId()).isEqualTo(TEST_FUNCTION_ID);
            assertThat(deploymentEntity.get().getNcaId()).isEqualTo(TEST_NCA_ID);
            assertThat(deploymentContextOpt.orElseThrow().gpuSpecs()).hasSize(2);
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
            Object tokenSupplier, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> ignoredSpecs, HttpStatus expectedStatus) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table.
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = getToken(tokenSupplier);
        // Get function deployment
        var builder = RequestEntity
                .get("/v2/nvcf/deployments"
                             + "/functions/" + functionId
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
        assertThat(responseBody.deployment().deploymentId()).isEqualTo(TEST_DEPLOYMENT_ID);
        assertThat(responseBody.deployment().functionStatus()).isEqualTo(DEPLOYING);
        assertThat(responseBody.deployment().ncaId()).isEqualTo(TEST_NCA_ID);
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
    @MethodSource({"authDeploymentArgs", "gpuSpecDeploymentArgs"})
    void shouldUpdateFunctionDeployment(
            Object tokenSupplier, UUID functionId, UUID functionVersionId,
            List<GpuSpecificationDto> specs, HttpStatus expectedStatus) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
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
                .put("/v2/nvcf/deployments"
                             + "/functions/" + functionId
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
        assertThat(responseBody.deployment().ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(responseBody.deployment().deploymentSpecifications()).hasSize(2);
        assertThat(responseBody.deployment().lastUpdatedAt())
                .isNotEqualTo(responseBody.deployment().createdAt());

        var updatedSpecs = responseBody.deployment().deploymentSpecifications();
        assertThat(updatedSpecs.getFirst().backend()).isEqualTo(GFN);
        assertThat(updatedSpecs.getFirst().gpu()).isIn(T10, L40G);
        assertThat(updatedSpecs.getFirst().maxInstances()).isIn(9, 8);
        assertThat(updatedSpecs.getFirst().minInstances()).isIn(7, 6, 2, 0);
        assertThat(updatedSpecs.getFirst().maxRequestConcurrency()).isIn(specs.stream().map(
                GpuSpecificationDto::maxRequestConcurrency).collect(Collectors.toSet()));
        assertThat(updatedSpecs.get(1).gpu()).isIn(T10, L40G);
        assertThat(updatedSpecs.get(1).maxInstances()).isIn(9, 8);
        assertThat(updatedSpecs.get(1).minInstances()).isIn(7, 6, 2, 0);
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
            // legacy update should not change autoscaler config
            assertThat(updatedSpec.autoscalingConfiguration()).isNull();
        });
    }

    Stream<Arguments> authUpdateSpecArgs() {
        var validSpec =
                UpdateGpuSpecificationRequest.builder().minInstances(0).maxInstances(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build();

        var jwtCases = Stream.of(
                // JWT Auth - Known function and version with proper scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_DEPLOY_FUNCTION),
                                                             100),
                             TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID,
                             validSpec,
                             HttpStatus.OK),
                // JWT Auth - Known function and version with admin scope
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(ADMIN_SCOPE_DEPLOY_FUNCTION),
                                                             100),
                             TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID,
                             validSpec,
                             HttpStatus.FORBIDDEN),
                // JWT Auth - Known function missing scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(), 100),
                             TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID,
                             validSpec,
                             HttpStatus.FORBIDDEN),
                // Missing auth for a known function
                Arguments.of(null,
                             TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID,
                             validSpec,
                             HttpStatus.UNAUTHORIZED)
        );

        var apiKeyCases = Stream.of(
                // apikey Auth - Missing scopes
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             },
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validSpec,
                             HttpStatus.FORBIDDEN),
                // apikey Auth - No resource entries in policy
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validSpec,
                             HttpStatus.FORBIDDEN),
                // apikey Auth - Incorrect resource type
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("authorized-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validSpec,
                             HttpStatus.FORBIDDEN),
                // apikey Auth - Authorized with account-functions wildcard
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("account-functions", "*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validSpec,
                             HttpStatus.OK),
                // apikey Auth - Authorized for specific function with wildcard versions
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                     TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validSpec,
                             HttpStatus.OK),
                // apikey Auth - Authorized for specific function and version
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1)),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validSpec,
                             HttpStatus.OK),
                // apikey Auth - Authorized for different function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                     TEST_FUNCTION_ID_2 + "/*")),
                                             List.of(SCOPE_DEPLOY_FUNCTION));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID, validSpec,
                             HttpStatus.FORBIDDEN)
        );

        return Stream.concat(jwtCases, apiKeyCases);
    }

    Stream<Arguments> gpuUpdateSpecArgs() {
        var validSpec =
                UpdateGpuSpecificationRequest.builder().minInstances(0).maxInstances(10)
                        .autoscalingConfiguration(createAutoscalingConfigDto()).build();
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DEPLOY_FUNCTION),
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
                Arguments.of(token, TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID,
                        UpdateGpuSpecificationRequest.builder()
                                .minInstances(10).maxInstances(0)
                                .build(), HttpStatus.BAD_REQUEST),
                // 7. negative
                Arguments.of(token, TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID,
                        UpdateGpuSpecificationRequest.builder()
                                .minInstances(-10).maxInstances(-5)
                                .build(), HttpStatus.BAD_REQUEST),
                // 8. negative empty
                Arguments.of(token, TEST_DEPLOYMENT_ID, TEST_GPU_SPEC_ID,
                        UpdateGpuSpecificationRequest.builder().build(),
                        HttpStatus.BAD_REQUEST),
                // 9. positive only one field is specified
                Arguments.of(token, TEST_DEPLOYMENT_ID,
                             TEST_GPU_SPEC_ID, UpdateGpuSpecificationRequest.builder()
                                     .minInstances(2).build(),
                             HttpStatus.OK),
                // 10. only min is being updated but it is more than max that was specified when
                //     deployment as initially created
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
    @MethodSource({"authUpdateSpecArgs", "gpuUpdateSpecArgs"})
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

        // Update function deployment
        var token = getToken(tokenSupplier);
        var builder = RequestEntity
                .patch("/v2/nvcf"
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

    Stream<Arguments> updateDegradedDeploymentSpecsArgs() {
        return Stream.of(
                // 1. Should change minInstance of DEGRADING function
                Arguments.of(FunctionStatus.DEGRADING, 8, 10, 9, 7, 10, 9, HttpStatus.OK),
                // 2. Should change maxInstance of DEGRADING function
                Arguments.of(FunctionStatus.DEGRADING, 8, 10, 9, 8, 11, 9, HttpStatus.OK),
                // 3. Should be able to change maxRequestConcurrency of DEGRADING function
                Arguments.of(FunctionStatus.DEGRADING, 8, 10, 9, 8, 10, 10, HttpStatus.OK),
                // 4. Should change minInstance of DEGRADED function
                Arguments.of(FunctionStatus.DEGRADED, 8, 10, 9, 7, 10, 9, HttpStatus.OK),
                // 5. Should change maxInstance of DEGRADED function
                Arguments.of(FunctionStatus.DEGRADED, 8, 10, 9, 8, 11, 9, HttpStatus.OK),
                // 6. Should be able to change maxRequestConcurrency of DEGRADED function
                Arguments.of(FunctionStatus.DEGRADED, 8, 10, 9, 8, 10, 10, HttpStatus.OK),
                // 7. Should be able to switch DEGRADED function to zero scaled
                Arguments.of(FunctionStatus.DEGRADED, 8, 10, 9, 0, 11, 10, HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("updateDegradedDeploymentSpecsArgs")
    void shouldUpdateFunctionDeploymentForDegraded(
            FunctionStatus initialStatus,
            int initialMinInstances,
            int initialMaxInstances,
            int initialMaxRequestConcurrency,
            int updatedMinInstances,
            int updatedMaxInstances,
            int updatedMaxRequestConcurrency,
            HttpStatus expectedHttpStatus) {
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             initialStatus);
        var initialGpuSpecs = Set.of(GpuSpecificationEntity.builder()
                                             .key(GpuSpecificationKey.builder()
                                                          .ncaId(TEST_NCA_ID)
                                                          .deploymentId(TEST_DEPLOYMENT_ID)
                                                          .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                          .build())
                                             .gpu(A10G)
                                             .instanceType(L40G_INSTANCE_TYPE)
                                             .backend(GFN)
                                             .maxInstances(initialMaxInstances)
                                             .minInstances(initialMinInstances)
                                             .maxRequestConcurrency(initialMaxRequestConcurrency)
                                             .build());

        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID,
                                           TEST_NCA_ID, initialGpuSpecs);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        var updatedGpuSpecs = List.of(GpuSpecificationDto.builder()
                                              .gpu(A10G)
                                              .instanceType(L40G_INSTANCE_TYPE)
                                              .backend(GFN)
                                              .maxInstances(updatedMaxInstances)
                                              .minInstances(updatedMinInstances)
                                              .maxRequestConcurrency(updatedMaxRequestConcurrency)
                                              .build());

        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(updatedGpuSpecs).build();
        var requestEntity = RequestEntity
                .put("/v2/nvcf/deployments"
                             + "/functions/" + TEST_FUNCTION_ID
                             + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedHttpStatus);
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
                                .maxInstances(12).minInstances(10).maxRequestConcurrency(99)
                                .build())),
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
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table using the following specs:
        //    - T10, GFN, 4, 4
        //    - L40G, GFN, 5, 5
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Update function deployment using GPU/Backend specs that are different from the
        // ones that were used when the function was originally deployed.
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(updateSpecs).build();
        var requestEntity = RequestEntity
                .put("/v2/nvcf/deployments"
                             + "/functions/" + TEST_FUNCTION_ID
                             + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    Stream<Arguments> emptyGpuSpecIdArgs() {
        return Stream.of(
                // Update request without gpu spec id with single gpu spec
                Arguments.of(Set.of(GpuSpecificationEntity.builder()
                                            .key(GpuSpecificationKey.builder()
                                                         .ncaId(TEST_NCA_ID)
                                                         .deploymentId(TEST_DEPLOYMENT_ID)
                                                         .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                         .build())
                                            .gpu(T10)
                                            .instanceType(T10_INSTANCE_TYPE)
                                            .maxInstances(4).minInstances(4)
                                            .maxRequestConcurrency(9).build()),
                             List.of(GpuSpecificationDto.builder()
                                             .maxInstances(10).minInstances(8)
                                             .maxRequestConcurrency(9).build()),
                             HttpStatus.OK),
                // Update request with multiple specs without gpu spec id
                Arguments.of(Set.of(GpuSpecificationEntity.builder()
                                            .key(GpuSpecificationKey.builder()
                                                         .ncaId(TEST_NCA_ID)
                                                         .deploymentId(TEST_DEPLOYMENT_ID)
                                                         .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                                         .build())
                                            .gpu(T10)
                                            .instanceType(T10_INSTANCE_TYPE)
                                            .maxInstances(4).minInstances(4)
                                            .maxRequestConcurrency(9).build(),
                                    GpuSpecificationEntity.builder()
                                            .key(GpuSpecificationKey.builder()
                                                         .ncaId(TEST_NCA_ID)
                                                         .deploymentId(TEST_DEPLOYMENT_ID)
                                                         .gpuSpecificationId(TEST_GPU_SPEC_ID_2)
                                                         .build())
                                            .gpu(T10)
                                            .instanceType(T10_INSTANCE_TYPE)
                                            .maxInstances(4).minInstances(4)
                                            .maxRequestConcurrency(9).build()),
                             List.of(GpuSpecificationDto.builder()
                                             .maxInstances(10).minInstances(8)
                                             .maxRequestConcurrency(9).build(),
                                     GpuSpecificationDto.builder()
                                             .maxInstances(10).minInstances(8)
                                             .maxRequestConcurrency(9).build()),
                             HttpStatus.BAD_REQUEST)
        );
    }

    @ParameterizedTest
    @MethodSource("emptyGpuSpecIdArgs")
    void shouldUpdateFunctionDeploymentWithSingleGpuSpec(Set<GpuSpecificationEntity> createSpecs,
                                                         List<GpuSpecificationDto> updateSpecs,
                                                         HttpStatus expectedStatus) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table using the following specs:
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID,
                createSpecs);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(updateSpecs).build();
        var requestEntity = RequestEntity
                .put("/v2/nvcf/deployments"
                             + "/functions/" + TEST_FUNCTION_ID
                             + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }

    @Test
    void deleteDeployedFunctionIncludingQueuesAndWorkers() {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table using the following specs:
        //    - T10, GFN, 4, 4
        //    - L40G, GFN, 5, 5
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DELETE_FUNCTION),
                                                    100);

        var requestEntity =
                RequestEntity.delete(URI.create("/v2/nvcf/functions/" + TEST_FUNCTION_ID
                                                        + "/versions/" + TEST_VERSION_ID_1))
                        .header("Authorization", "Bearer " + token)
                        .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        // Verify function is deleted.
        var entity = functionsRepository
                .getByFunctionVersionId(TEST_VERSION_ID_1);
        assertThat(entity).isEmpty();

        // Verify deployment is deleted when the function is deleted.
        var deploymentContext =
                functionDeploymentLookupService
                        .getDeploymentContextByDeploymentId(TEST_DEPLOYMENT_ID);
        assertThat(deploymentContext).isEmpty();

        // Queue Deletion is async
        // Cannot verify if workers were deleted.
    }

    @Test
    void emptyMaxConcurrencyShouldNotUpdateDb() {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table using the following specs:
        //    - T10, GFN, 4, 4
        //    - L40G, GFN, 5, 5
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        List<GpuSpecificationDto> updateSpecs = List.of(
                GpuSpecificationDto.builder()
                        .gpuSpecificationId(TEST_GPU_SPEC_ID)
                        .gpu(T10).backend(GFN)
                        .maxInstances(4).minInstances(4).instanceType(T10_INSTANCE_TYPE).build(),
                GpuSpecificationDto.builder()
                        .gpuSpecificationId(TEST_GPU_SPEC_ID_2)
                        .gpu(L40G).backend(GFN)
                        .maxInstances(5).minInstances(5).instanceType(L40G_INSTANCE_TYPE).build());

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Update function deployment using GPU/Backend specs that are different from the
        // ones that were used when the function was originally deployed.
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(updateSpecs).build();
        var requestEntity = RequestEntity
                .put("/v2/nvcf/deployments"
                             + "/functions/" + TEST_FUNCTION_ID
                             + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);
        assertThat(deploymentContext).isNotNull();
        assertThat(deploymentContext).isPresent();
        assertThat(deploymentContext.get().gpuSpecs().stream()
                           .map(GpuSpecificationEntity::getMaxRequestConcurrency)
                           .collect(Collectors.toSet()))
                .containsAll(List.of(9, 99));
    }

    /**
     * We should not allow to deploy a function if it had a deployment if it has a non-empty
     * invocation queue. It was gracefully deleted and still has deployment record in DB.
     * BadRequest should be thrown in such case.
     */
    @Test
    void shouldNotDeployGracefullyDeletedDeployment() {
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

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                    List.of(SCOPE_DEPLOY_FUNCTION, SCOPE_DELETE_FUNCTION),
                                                    100);

        var deployRequestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();
        var builder = RequestEntity.post(
                        "/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID
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
                        "/v2/nvcf/deployments/functions/" + TEST_FUNCTION_ID + "/versions/"
                                + TEST_VERSION_ID_1 + "?graceful=true")
                .header("Authorization", "Bearer " + token)
                .build();
        var deleteResponseEntity = testRestTemplate.exchange(
                deleteRequestEntity, FunctionResponse.class);
        assertThat(deleteResponseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        // Verify resources and properties.
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);
        assertThat(deploymentContext).isPresent();

        // Try to deploy function and get 400
        var deployAgainResponseEntity = testRestTemplate.exchange(
                deployRequestEntity, DeploymentResponse.class);
        assertThat(deployAgainResponseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void nullFunctionDeploymentSpecs() {
        // Create function with INACTIVE status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.INACTIVE);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Deploy function using null deployment specs
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(null).build();
        var requestEntity = RequestEntity
                .post("/v2/nvcf/deployments"
                              + "/functions/" + TEST_FUNCTION_ID
                              + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    @Test
    void nullUpdateFunctionDeploymentSpecs() {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                FunctionStatus.DEPLOYING);
        // Create entries in functions_deployment_v2 table using the following specs:
        //    - T10, GFN, 4, 4
        //    - L40G, GFN, 5, 5
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Update existing function deployment using null deployment specs.
        var requestBody = UpdateFunctionDeploymentRequest.builder()
                .deploymentSpecifications(null).build();
        var requestEntity = RequestEntity
                .put("/v2/nvcf/deployments"
                             + "/functions/" + TEST_FUNCTION_ID
                             + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
    }

    Stream<Arguments> updateValidationArgs() {
        return Stream.of(
                // 1. Backend: Origin GFN, update empty: BAD_REQUEST
                Arguments.of(GFN, Collections.emptySet(), "",
                             Collections.emptySet(), HttpStatus.BAD_REQUEST),
                // 2. Backend: Origin GFN, update GFN: OK
                Arguments.of(GFN, Collections.emptySet(), GFN,
                             Collections.emptySet(), HttpStatus.OK),
                // 3. Backend: Origin empty, update GFN: BAD_REQUEST
                Arguments.of("", Collections.emptySet(), GFN,
                             Collections.emptySet(), HttpStatus.BAD_REQUEST),
                // 4. Clusters: Origin set, update empty: BAD_REQUEST
                Arguments.of(GFN, Set.of("cluster01", "cluster02"), GFN,
                             Collections.emptySet(), HttpStatus.BAD_REQUEST),
                // 5. Clusters: Origin set, update set: OK
                Arguments.of(GFN, Set.of("cluster01", "cluster02"), GFN,
                             Set.of("cluster02", "cluster01"), HttpStatus.OK),
                // 6. Clusters: Origin empty, update set: BAD_REQUEST
                Arguments.of(GFN, Collections.emptySet(), GFN,
                             Set.of("cluster02", "cluster01"), HttpStatus.BAD_REQUEST),
                // 7. Clusters: Origin set, update set - mismatch: BAD_REQUEST
                Arguments.of(GFN, Set.of("cluster01", "cluster02"), GFN,
                             Set.of("cluster02", "cluster03"), HttpStatus.BAD_REQUEST),
                // 8. Clusters: Origin set, update set - update overlap: OK
                Arguments.of(GFN, Set.of("cluster01", "cluster02"), GFN,
                             Set.of("cluster02", "cluster03", "cluster01"), HttpStatus.BAD_REQUEST),
                // 9. all empty: OK
                Arguments.of("", Collections.emptySet(), "",
                             Collections.emptySet(), HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("updateValidationArgs")
    void shouldValidateUpdateSpec(String originBackend, Set<String> originClusters,
                                  String updateBackend, Set<String> updateClusters,
                                  HttpStatus expectedResponse) {
        // Create function with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);

        var gpuSpecificationDtoBuilder1 =
                UpdateGpuSpecificationDto.builder()
                        .gpuSpecificationId(TEST_GPU_SPEC_ID)
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9);
        var gpuSpecificationDtoBuilder2 =
                UpdateGpuSpecificationDto.builder()
                        .gpuSpecificationId(TEST_GPU_SPEC_ID_2)
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(5).maxRequestConcurrency(9);
        if (StringUtils.isNotBlank(updateBackend)) {
            gpuSpecificationDtoBuilder1.backend(updateBackend);
            gpuSpecificationDtoBuilder2.backend(updateBackend);
        }
        if (!updateClusters.isEmpty()) {
            gpuSpecificationDtoBuilder1.clusters(updateClusters);
            gpuSpecificationDtoBuilder2.clusters(updateClusters);
        }
        var specs = List.of(gpuSpecificationDtoBuilder1.build(),
                            gpuSpecificationDtoBuilder2.build());

        // Create entries in functions_deployment_v2 table using the following specs:
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID,
                                           TEST_NCA_ID,
                                           originBackend, originClusters);

        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);
        // Update existing function deployment using null deployment specs.
        var requestBody = UpdateFunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();

        var requestEntity = RequestEntity
                .put("/v2/nvcf/deployments"
                             + "/functions/" + TEST_FUNCTION_ID
                             + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedResponse);
    }

    @Test
    void shouldNotInsertOnUpdateWithoutChanges() {
        var token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT, List.of(SCOPE_DEPLOY_FUNCTION),
                                                    100);

        // Create functions with DEPLOYING status in the same account.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        var gpuSpecs = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                     .build())
                        .backend("nvcf-dgxc-k8s-forge-az24-dev6").gpu("AD102GL")
                        .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                        .maxInstances(1).minInstances(1).build());
        testService.createDeploymentEntity(
                TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs);
        var deploymentEntity = functionDeploymentLookupService
                .getDeploymentContextByDeploymentId(TEST_DEPLOYMENT_ID).orElseThrow().deployment();

        // Update existing function deployment using null deployment specs.
        var requestBody = UpdateFunctionDeploymentRequest.builder()
                .deploymentSpecifications(
                        List.of(UpdateGpuSpecificationDto.builder()
                                        .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                        .backend("nvcf-dgxc-k8s-forge-az24-dev6")
                                        .gpu("AD102GL")
                                        .instanceType("DGX-CLOUD.GPU.AD102GL_2x")
                                        .maxInstances(1)
                                        .minInstances(1)
                                        .build())).build();
        var requestEntity = RequestEntity
                .put("/v2/nvcf/deployments"
                             + "/functions/" + TEST_FUNCTION_ID
                             + "/versions/" + TEST_VERSION_ID_1)
                .contentType(MediaType.APPLICATION_JSON)
                .header("Authorization", "Bearer " + token)
                .body(requestBody);

        var responseEntity = testRestTemplate.exchange(requestEntity, DeploymentResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);

        var deployment = responseEntity.getBody().deployment();
        assertThat(deployment.lastUpdatedAt()).isEqualTo(deploymentEntity.getLastUpdatedAt());
        assertThat(deployment.deploymentId()).isEqualTo(TEST_DEPLOYMENT_ID);
    }

    @Test
    void shouldNotCreateDeploymentIfContainerRegistryInvalid() {
        var entity = FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID)
                .functionVersionId(TEST_VERSION_ID_1)
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(TEST_NCA_ID)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage("invalid")
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
