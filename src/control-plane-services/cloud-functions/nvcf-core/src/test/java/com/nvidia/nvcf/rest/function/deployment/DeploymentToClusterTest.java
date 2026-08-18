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

import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.INACTIVE;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.COMPLETE;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_CLUSTER_GROUP;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_GPU;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_GPUS;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_INSTANCE_TYPES;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.MISSING_INSTANCE_TYPE_DEFAULT;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.WITHOUT_ERROR_BODY_500;
import static com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState.WITH_ERROR_BODY_400;
import static com.nvidia.nvcf.util.MockIcmsServer.InstancesHealthState.HEALTHY;
import static com.nvidia.nvcf.util.TestConstants.GFN;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.SUPPORTED_INSTANCE_TYPES;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_4;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_5;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;
import static org.assertj.core.api.Assertions.fail;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.GpuSpecificationDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import com.nvidia.nvcf.util.MockIcmsServer.ClusterGroupsResponseState;
import com.nvidia.nvcf.util.MockIcmsServer.IcmsRequestHealthContext;
import com.nvidia.nvcf.util.MockIcmsServer.TestGpu;
import java.net.URISyntaxException;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class DeploymentToClusterTest {

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private FunctionDeploymentService deploymentService;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestCommonService testCommonService;

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

    private AuditEventPayload.Builder auditEventPayloadBuilder;

    @BeforeAll
    void beforeAll() throws URISyntaxException {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockIcmsServer.start(9096, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
        auditEventPayloadBuilder = testCommonService.getAuditEventPayloadBuilder();
    }

    @BeforeEach
    void init() {
        // Create test function
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME);
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
    }

    Stream<Arguments> clusterGroupDeploymentArgs() {
        return Stream.of(
                Arguments.of(COMPLETE, DEPLOYING),
                Arguments.of(MISSING_CLUSTER_GROUP, INACTIVE),
                Arguments.of(MISSING_GPUS, INACTIVE),
                Arguments.of(MISSING_GPU, INACTIVE),
                Arguments.of(MISSING_INSTANCE_TYPES, INACTIVE),
                Arguments.of(MISSING_INSTANCE_TYPE_DEFAULT, INACTIVE),
                Arguments.of(WITH_ERROR_BODY_400, INACTIVE),
                Arguments.of(WITHOUT_ERROR_BODY_500, INACTIVE)
                        );
    }

    Stream<Arguments> functionsByCacheArgs() {
        return Stream.of(
                Arguments.of(TEST_FUNCTION_ID, TEST_VERSION_ID_1, 92969580584L),
                Arguments.of(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, 36L),
                Arguments.of(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3, 232476667482L),
                Arguments.of(TEST_FUNCTION_ID, TEST_VERSION_ID_4, 0L),
                Arguments.of(TEST_FUNCTION_ID, TEST_VERSION_ID_5, 92969580584L)
                        );
    }

    @ParameterizedTest
    @MethodSource("clusterGroupDeploymentArgs")
    void testDeploymentToClusterGroup(
            ClusterGroupsResponseState clusterGroupResponseState,
            FunctionStatusEnum functionStatus) {
        // Set Wiremock server to serve responses with healthy T10s and L40Gs.
        var healthContexts = List.of(
                IcmsRequestHealthContext.builder().gpu(TestGpu.GFN_T10)
                        .instanceHealthState(HEALTHY).build(),
                IcmsRequestHealthContext.builder().gpu(TestGpu.GFN_L40G)
                        .instanceHealthState(HEALTHY).build()
                                    );
        MockIcmsServer.start(9096, jsonMapper, healthContexts, clusterGroupResponseState);

        // Deploy test function to GFN ClusterGroup. Create deployment specs without
        // instance-types and availability zones.
        var specs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(9).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99).build());
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();

        if (functionStatus == DEPLOYING) {
            // Confirm deployment specs contain instance-type.
            deploymentService.createFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                       TEST_VERSION_ID_1, requestBody,
                                                       auditEventPayloadBuilder,
                                                       x -> true);
            var dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                        TEST_VERSION_ID_1);
            assertThat(dto.functionStatus()).isEqualTo(functionStatus);
            assertThat(dto.deploymentSpecifications().stream()
                               .map(GpuSpecificationDto::instanceType)
                               .collect(Collectors.toSet()))
                    .containsExactlyInAnyOrderElementsOf(
                            Set.of(T10_INSTANCE_TYPE, L40G_INSTANCE_TYPE));
        } else {
            var message = switch (clusterGroupResponseState) {
                case MISSING_CLUSTER_GROUP -> "Invalid Backend 'GFN' specified";
                case MISSING_GPUS -> "GPUs missing for Backend 'GFN'";
                case MISSING_GPU -> "Invalid GPU 'T10' specified";
                case WITH_ERROR_BODY_400 -> "pretend bad deployment spec";
                case WITHOUT_ERROR_BODY_500 -> "Failed to get response from 'ICMS' after retries.";
                case MISSING_INSTANCE_TYPES, MISSING_INSTANCE_TYPE_DEFAULT ->
                        "Failed to deploy, reverting state to 'INACTIVE'";
                default -> "Not needed";
            };

            switch (clusterGroupResponseState) {
                case MISSING_CLUSTER_GROUP:
                case MISSING_GPUS:
                case MISSING_GPU:
                case WITH_ERROR_BODY_400:
                    assertThatExceptionOfType(BadRequestException.class)
                            .isThrownBy(() -> deploymentService
                                    .createFunctionDeployment(TEST_NCA_ID,
                                                              TEST_FUNCTION_ID,
                                                              TEST_VERSION_ID_1,
                                                              requestBody,
                                                              auditEventPayloadBuilder,
                                                              x -> true))
                            .withMessageContaining(message);

                    break;

                case WITHOUT_ERROR_BODY_500:
                case MISSING_INSTANCE_TYPES:
                case MISSING_INSTANCE_TYPE_DEFAULT:
                    assertThatExceptionOfType(UpstreamException.class)
                            .isThrownBy(() -> deploymentService
                                    .createFunctionDeployment(TEST_NCA_ID,
                                                              TEST_FUNCTION_ID,
                                                              TEST_VERSION_ID_1,
                                                              requestBody,
                                                              auditEventPayloadBuilder,
                                                              x -> true))
                            .withMessageContaining(message);

                    break;

                default:
                    fail("Invalid state");
            }
        }
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        assertThat(function).isNotNull();
        assertThat(function).isPresent();
        assertThat(function.get().getFunctionStatus().name()).isEqualTo(functionStatus.name());
    }

    @ParameterizedTest
    @MethodSource("functionsByCacheArgs")
    void testCacheSize(
            UUID functionId,
            UUID functionVersionId,
            long expectedCacheSize) {

        MockIcmsServer.start(9096, jsonMapper);
        // Function with small model
        testService.createTestFunctionEntityWithModel(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                      TEST_NCA_ID, TEST_FUNCTION_NAME_2,
                                                      TestDeploymentService.smallModel(TEST_VERSION_ID_2));
        // Function with model and resources
        testService.createTestFunctionEntityWithModelAndResource(TEST_FUNCTION_ID_3,
                                                                 TEST_VERSION_ID_3,
                                                                 TEST_NCA_ID, TEST_FUNCTION_NAME_3);

        // Function with no model
        testService.createTestFunctionEntityWithModel(TEST_FUNCTION_ID, TEST_VERSION_ID_4,
                                                      TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                      null);

        // Function with model as files
        testService.createTestFunctionEntityWithModel(TEST_FUNCTION_ID, TEST_VERSION_ID_5,
                                                      TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                      TestDeploymentService.defaultModels(TEST_VERSION_ID_5));

        // Deploy test function to GFN ClusterGroup. Create deployment specs without
        // instance-types and availability zones.
        var specs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(9).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99).build());
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();

        deploymentService.createFunctionDeployment(TEST_NCA_ID, functionId,
                                                   functionVersionId, requestBody,
                                                   auditEventPayloadBuilder,
                                                   x-> true);
        assertThat(MockIcmsServer.getCacheSize()).isEqualTo(expectedCacheSize);
        MockIcmsServer.stop();
    }

    @Test
    void failedIcmsRequestShouldNotBreakFlow() {
        // Set Wiremock server to serve responses with healthy T10s and L40Gs.
        var healthContexts = List.of(
                IcmsRequestHealthContext.builder().gpu(TestGpu.GFN_T10)
                        .instanceHealthState(HEALTHY).build(),
                IcmsRequestHealthContext.builder().gpu(TestGpu.GFN_L40G)
                        .instanceHealthState(HEALTHY).build()
                                    );
        MockIcmsServer.start(9096, jsonMapper, healthContexts, COMPLETE, false);
        // Deploy test function to GFN ClusterGroup. Create deployment specs without
        // instance-types and availability zones.
        var specs = List.of(
                GpuSpecificationDto.builder()
                        .gpu(T10).backend(GFN)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(9).build(),
                GpuSpecificationDto.builder()
                        .gpu(L40G).backend(GFN)
                        .maxInstances(5).minInstances(2).maxRequestConcurrency(99).build());
        var requestBody = FunctionDeploymentRequest.builder()
                .deploymentSpecifications(specs).build();

        // Confirm deployment specs contain instance-type.
        deploymentService.createFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                   TEST_VERSION_ID_1, requestBody,
                                                   auditEventPayloadBuilder,
                                                   x -> true);
        var dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(DEPLOYING);
        assertThat(dto.deploymentSpecifications().stream()
                           .map(GpuSpecificationDto::instanceType)
                           .collect(Collectors.toSet()))
                .containsExactlyInAnyOrderElementsOf(
                        Set.of(T10_INSTANCE_TYPE, L40G_INSTANCE_TYPE));

        dto.deploymentSpecifications().forEach(deploymentSpec -> {
            if (SUPPORTED_INSTANCE_TYPES.contains(deploymentSpec.instanceType())) {
                assertThat(deploymentSpec.cpuArch()).isNullOrEmpty();
                assertThat(deploymentSpec.os()).isNullOrEmpty();
                assertThat(deploymentSpec.driverVersion()).isNullOrEmpty();
                assertThat(deploymentSpec.storage()).isNullOrEmpty();
            }
        });
    }
}
