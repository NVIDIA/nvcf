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

import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlPathEqualTo;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.ACTIVE;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.ERROR;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState.RUNNING;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState.SHUTTING_DOWN;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState.TERMINATED;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.T10_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentHealthDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.scheduler.InstanceManagementTask;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import com.nvidia.nvcf.util.MockIcmsServer.IcmsInstancesContext;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.SneakyThrows;
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
class DeploymentErrorPropagationTest {

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private FunctionDeploymentLookupService functionDeploymentLookupService;

    @Autowired
    private InstanceManagementTask instanceManagementTask;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestQueueService testQueueService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockIcmsServer.start(9096, jsonMapper);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

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
        MockIcmsServer.getMockIcmsServer().resetAll();
    }

    Stream<Arguments> errorPropagationArgs() {
        var healthyT10 = icmsInstance(TEST_GPU_SPEC_ID, T10_INSTANCE_TYPE, RUNNING);
        var unhealthyT10 = icmsInstance(TEST_GPU_SPEC_ID, T10_INSTANCE_TYPE, SHUTTING_DOWN);
        var healthyL40G = icmsInstance(TEST_GPU_SPEC_ID_2, L40G_INSTANCE_TYPE, RUNNING);
        var unhealthyL40G = icmsInstance(TEST_GPU_SPEC_ID_2, L40G_INSTANCE_TYPE, TERMINATED);

        return Stream.of(
                Arguments.of(1, 1, List.of(healthyT10, healthyL40G), ACTIVE, 0),
                Arguments.of(1, 1, List.of(healthyT10, unhealthyL40G), DEPLOYING, 1),
                Arguments.of(1, 1,
                             List.of(healthyT10, unhealthyT10, healthyL40G), ACTIVE, 0),
                Arguments.of(1, 1,
                             List.of(healthyT10, unhealthyT10, unhealthyL40G), DEPLOYING, 1),
                Arguments.of(1, 1,
                             List.of(healthyT10, unhealthyT10, healthyL40G, unhealthyL40G),
                             ACTIVE, 0),
                Arguments.of(1, 1, List.of(unhealthyT10, unhealthyL40G), ERROR, 2),
                Arguments.of(0, 1, List.of(healthyT10, healthyL40G), ACTIVE, 0),
                Arguments.of(0, 0, List.of(healthyT10, healthyL40G), ACTIVE, 0),
                Arguments.of(0, 0, List.of(healthyT10, unhealthyT10, healthyL40G),
                             ACTIVE, 0),
                Arguments.of(0, 0,
                             List.of(healthyT10, unhealthyT10, healthyL40G, unhealthyL40G),
                             ACTIVE, 0)
        );
    }

    @SneakyThrows
    @MethodSource("errorPropagationArgs")
    @ParameterizedTest
    void shouldPropagateErrors(
            int minInstanceT10,
            int minInstanceL40G,
            List<IcmsInstancesContext> instancesContexts,
            FunctionStatusEnum expectedFunctionStatus,
            int numberOfExpectedHealthInfos) {
        initDeployment(minInstanceT10, minInstanceL40G);
        MockIcmsServer.start(9096, instancesContexts);

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext = functionDeploymentLookupService
                .getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext).isPresent();

        instanceManagementTask.runUnchecked(function.get(), deploymentContext.get());
        assertWorkloadInstancesEndpointUsed();

        var dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(expectedFunctionStatus);
        if (numberOfExpectedHealthInfos > 0) {
            assertThat(dto.healthInfo()).isNotNull();
            var errors = dto.healthInfo().stream().map(DeploymentHealthDto::error)
                    .collect(Collectors.toSet());
            assertThat(errors).hasSize(numberOfExpectedHealthInfos);
        } else {
            assertThat(dto.healthInfo()).isNull();
        }
    }

    @SneakyThrows
    @Test
    void shouldUpdateHealthInfoWhenDeploymentsSucceed() {
        initDeployment(1, 1);
        MockIcmsServer.start(9096, List.of(
                icmsInstance(TEST_GPU_SPEC_ID, T10_INSTANCE_TYPE, RUNNING),
                icmsInstance(TEST_GPU_SPEC_ID_2, L40G_INSTANCE_TYPE, TERMINATED)));

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext = functionDeploymentLookupService
                .getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext).isPresent();

        instanceManagementTask.runUnchecked(function.get(), deploymentContext.get());
        assertWorkloadInstancesEndpointUsed();

        var dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(DEPLOYING);
        assertHealthInfo(dto.healthInfo(), L40G_INSTANCE_TYPE);

        MockIcmsServer.start(9096, List.of(
                icmsInstance(TEST_GPU_SPEC_ID, T10_INSTANCE_TYPE, SHUTTING_DOWN),
                icmsInstance(TEST_GPU_SPEC_ID_2, L40G_INSTANCE_TYPE, RUNNING)));

        function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext2 = functionDeploymentLookupService
                .getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext2).isPresent();

        instanceManagementTask.runUnchecked(function.get(), deploymentContext2.get());
        assertWorkloadInstancesEndpointUsed();

        dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(DEPLOYING);
        assertHealthInfo(dto.healthInfo(), T10_INSTANCE_TYPE);

        MockIcmsServer.start(9096, List.of(
                icmsInstance(TEST_GPU_SPEC_ID, T10_INSTANCE_TYPE, RUNNING),
                icmsInstance(TEST_GPU_SPEC_ID_2, L40G_INSTANCE_TYPE, RUNNING)));

        function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext3 = functionDeploymentLookupService
                .getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext3).isPresent();

        instanceManagementTask.runUnchecked(function.get(), deploymentContext3.get());
        assertWorkloadInstancesEndpointUsed();

        dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(ACTIVE);
        assertThat(dto.healthInfo()).isNull();
    }

    private void initDeployment(int minInstanceT10, int minInstanceL40G) {
        var gpuSpecs = Set.of(
                gpuSpec(TEST_GPU_SPEC_ID, T10, T10_INSTANCE_TYPE, minInstanceT10),
                gpuSpec(TEST_GPU_SPEC_ID_2, L40G, L40G_INSTANCE_TYPE, minInstanceL40G));
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs);
    }

    private static GpuSpecificationEntity gpuSpec(
            UUID gpuSpecId,
            String gpu,
            String instanceType,
            int minInstances) {
        return GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(gpuSpecId)
                             .build())
                .gpu(gpu)
                .instanceType(instanceType)
                .maxInstances(5)
                .minInstances(minInstances)
                .maxRequestConcurrency(9)
                .clusters(Set.of("cluster02", "cluster03", "cluster01"))
                .build();
    }

    private static IcmsInstancesContext icmsInstance(
            UUID gpuSpecId,
            String instanceType,
            MockIcmsServer.InstanceState state) {
        return IcmsInstancesContext.builder()
                .instanceState(state)
                .instanceCount(1)
                .gpuSpecId(gpuSpecId)
                .instanceType(instanceType)
                .build();
    }

    private static void assertHealthInfo(
            List<DeploymentHealthDto> healthInfo,
            String expectedInstanceType) {
        assertThat(healthInfo).isNotNull();
        var errors = healthInfo.stream().map(DeploymentHealthDto::error)
                .collect(Collectors.toSet());
        assertThat(errors).hasSize(1);
        assertThat(errors.iterator().next()).contains(expectedInstanceType);
    }

    private static void assertWorkloadInstancesEndpointUsed() {
        MockIcmsServer.getMockIcmsServer().verify(
                getRequestedFor(urlPathEqualTo(
                        "/v1/si/accounts/%s/workloads/%s/instances"
                                .formatted(TEST_NCA_ID, TEST_DEPLOYMENT_ID))));
    }
}
