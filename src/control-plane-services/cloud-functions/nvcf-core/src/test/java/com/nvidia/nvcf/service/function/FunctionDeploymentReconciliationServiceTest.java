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
package com.nvidia.nvcf.service.function;

import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.ACTIVE;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.ERROR;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState.RUNNING;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState.SHUTTING_DOWN;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState.STARTING;
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
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;

import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.icms.allocator.IcmsAllocatorService;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentHealthDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.instance.InstanceService;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import com.nvidia.nvcf.util.MockIcmsServer.IcmsInstancesContext;
import com.nvidia.nvcf.util.TestConstants;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
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
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mock;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
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
@ExtendWith(MockitoExtension.class)
class FunctionDeploymentReconciliationServiceTest {

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestQueueService testQueueService;

    @Mock
    private IcmsAllocatorService icmsAllocatorService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private IcmsClient icmsClient;

    @Autowired
    private FunctionDeploymentService functionDeploymentService;

    @Autowired
    private FunctionDeploymentLookupService functionDeploymentLookupService;

    @Autowired
    private InstanceService instanceService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    private FunctionDeploymentReconciliationService functionDeploymentReconciliationService;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockIcmsServer.start(9096, jsonMapper);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @BeforeEach
    void beforeEach() {
        functionDeploymentReconciliationService = new FunctionDeploymentReconciliationService(
                icmsClient, functionDeploymentService, instanceService,
                icmsAllocatorService);
        // reset, not clearInvocations: stubbings must not leak between tests either
        Mockito.reset(icmsAllocatorService);
    }

    @Test
    void shouldSkipReconciliationForCleanupStatuses() {
        for (var status : List.of(FunctionStatus.INACTIVE, FunctionStatus.ERROR)) {
            var function = Mockito.mock(FunctionEntity.class);
            Mockito.when(function.getNcaId()).thenReturn(TEST_NCA_ID);
            Mockito.when(function.getFunctionId()).thenReturn(TEST_FUNCTION_ID);
            Mockito.when(function.getFunctionVersionId()).thenReturn(TEST_VERSION_ID_1);
            Mockito.when(function.getFunctionStatus()).thenReturn(status);

            var result = functionDeploymentReconciliationService.reconcile(
                    function, Mockito.mock(FunctionDeploymentContext.class));

            assertThat(result).isSameAs(function);
        }
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockIcmsServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        testQueueService.clearQueues();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    Stream<Arguments> updateStatusArgs() {
        var healthyContexts = List.of(
                IcmsInstancesContext.builder()
                        .instanceState(RUNNING)
                        .instanceCount(3)
                        .gpuSpecId(TEST_GPU_SPEC_ID).
                        build(),
                IcmsInstancesContext.builder()
                        .instanceState(STARTING)
                        .instanceCount(3)
                        .gpuSpecId(TEST_GPU_SPEC_ID)
                        .build()
        );

        var terminatedContexts = List.of(
                IcmsInstancesContext.builder()
                        .instanceState(TERMINATED)
                        .instanceCount(1)
                        .gpuSpecId(TEST_GPU_SPEC_ID)
                        .build()
        );
        return Stream.of(
                // The ICMS mock server setup to return 3 active and 3 pending instances
                // for healthyContexts and terminated response for terminatedContexts
                // 1. DEPLOYING, min instance not met, stays DEPLOYING
                Arguments.of(FunctionStatus.DEPLOYING, healthyContexts, 4,
                             Instant.now(), FunctionStatus.DEPLOYING),
                // 2. ACTIVE, min instance is met, stays ACTIVE
                Arguments.of(FunctionStatus.ACTIVE, healthyContexts, 2,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.ACTIVE),
                // 3. ACTIVE, min instance is not met, transfer to DEGRADING
                Arguments.of(FunctionStatus.ACTIVE, healthyContexts, 4,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.DEGRADING),
                // 4. ACTIVE, no active instances, transfer to DEGRADED
                Arguments.of(FunctionStatus.ACTIVE, terminatedContexts, 4,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.DEGRADED),
                // 5. DEGRADING, min instance is not met, stays DEGRADING
                Arguments.of(FunctionStatus.DEGRADING, healthyContexts, 4,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.DEGRADING),
                // 6. DEGRADING, no active instances, transfer to DEGRADED
                Arguments.of(FunctionStatus.DEGRADING, terminatedContexts, 4,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.DEGRADED),
                // 7. DEGRADING, min instance is met, transfer to ACTIVE
                Arguments.of(FunctionStatus.DEGRADING, healthyContexts, 2,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.ACTIVE),
                // 8. DEGRADED, no active instances, stays DEGRADED
                Arguments.of(FunctionStatus.DEGRADED, terminatedContexts, 4,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.DEGRADED),
                // 9. DEGRADED, min instance is not met, transfer to DEGRADING
                Arguments.of(FunctionStatus.DEGRADED, healthyContexts, 5,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.DEGRADING),
                // 10. DEGRADED, min instance is met, transfer to ACTIVE
                Arguments.of(FunctionStatus.DEGRADED, healthyContexts, 1,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.ACTIVE),
                // 11. INACTIVE stays INACTIVE
                Arguments.of(FunctionStatus.INACTIVE, healthyContexts, 3,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.INACTIVE),
                // 12. ERROR stays ERROR
                Arguments.of(FunctionStatus.ERROR, healthyContexts, 3,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.ERROR),
                // 13. ERROR older than 7 days by createdAt but updated recently stays ERROR
                Arguments.of(FunctionStatus.ERROR, healthyContexts, 0,
                             Instant.now().minus(Duration.ofDays(10)), FunctionStatus.ERROR),
                // 14. DEPLOYING, requirements met, transferring to ACTIVE
                Arguments.of(FunctionStatus.DEPLOYING, healthyContexts, 1,
                             Instant.now().minusSeconds(60), FunctionStatus.ACTIVE),
                // 14. ACTIVE, zero scaled, min instance of 0 is met, stays ACTIVE
                Arguments.of(FunctionStatus.ACTIVE, terminatedContexts, 0,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.ACTIVE),
                // 15. DEPLOYING should not become ERROR after timeout
                Arguments.of(FunctionStatus.DEPLOYING, terminatedContexts, 3,
                             Instant.now().minus(Duration.ofMinutes(35)), FunctionStatus.DEPLOYING),
                // 16. DEPLOYING should stay DEPLOYING within 60 seconds
                Arguments.of(FunctionStatus.DEPLOYING, List.of(), 3,
                             Instant.now().minus(Duration.ofMinutes(1)), FunctionStatus.DEPLOYING),
                // 17. DEPLOYING should not become ERROR after one Day with empty response
                Arguments.of(FunctionStatus.DEPLOYING, List.of(), 3,
                             Instant.now().minus(Duration.ofDays(1)), FunctionStatus.DEPLOYING)
        );
    }

    @ParameterizedTest
    @MethodSource("updateStatusArgs")
    void shouldUpdateRunningFunctionStatus(FunctionStatus existingStatus,
                                           List<IcmsInstancesContext> instancesContexts,
                                           int minInstances, Instant createdAt,
                                           FunctionStatus expectingStatus) {
        MockIcmsServer.start(9096, instancesContexts);

        // before
        var gpuSpec = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                .maxInstances(10).minInstances(minInstances).maxRequestConcurrency(9).clusters(
                        Set.of("cluster02", "cluster03", "cluster01")).build();
        var function = testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                            TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                            existingStatus);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, Set.of(gpuSpec),
                                           createdAt);
        icmsAllocatorService.scheduleNewInstance(
                function, TEST_DEPLOYMENT_ID, gpuSpec, gpuSpec.getMinInstances());

        var func = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(func).isPresent();
        assertThat(deploymentContext).isPresent();

        // act
        functionDeploymentReconciliationService.reconcile(
                func.get(), deploymentContext.get());

        // validate
        var resultFunction =
                functionLookupService.lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID,
                                                                        TEST_VERSION_ID_1);
        assertThat(resultFunction).isPresent();
        assertThat(resultFunction.get().getFunctionStatus()).isEqualTo(expectingStatus);
    }

    @Test
    void shouldUpdateRunningFunctionStatusWhenRemoteInstanceLookupFails() {
        var failingIcmsClient = Mockito.mock(IcmsClient.class);
        var serviceWithFailingRemoteCall = new FunctionDeploymentReconciliationService(
                failingIcmsClient, functionDeploymentService, instanceService,
                icmsAllocatorService);
        var gpuSpec = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                .maxInstances(10).minInstances(1).maxRequestConcurrency(9)
                .clusters(Set.of("cluster02", "cluster03", "cluster01")).build();
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.ACTIVE);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, Set.of(gpuSpec),
                                           Instant.now().minus(Duration.ofDays(1)));
        Mockito.when(failingIcmsClient.getInstancesByDeploymentId(
                        TEST_NCA_ID, TEST_DEPLOYMENT_ID))
                .thenThrow(new RuntimeException("SIS unavailable"));

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext).isPresent();

        serviceWithFailingRemoteCall.reconcile(function.get(), deploymentContext.get());

        var resultFunction =
                functionLookupService.lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID,
                                                                        TEST_VERSION_ID_1);
        assertThat(resultFunction).isPresent();
        assertThat(resultFunction.get().getFunctionStatus()).isEqualTo(FunctionStatus.DEGRADED);
    }

    @Test
    void shouldKeepRetrievedInstanceStateWhenAllocationFails() {
        // 3 running + 3 starting instances exist, but minInstances is 8, so the task tries to
        // allocate more and the allocator fails, e.g. when the cluster is out of capacity.
        MockIcmsServer.start(9096, List.of(
                IcmsInstancesContext.builder()
                        .instanceState(RUNNING).instanceCount(3)
                        .gpuSpecId(TEST_GPU_SPEC_ID).build(),
                IcmsInstancesContext.builder()
                        .instanceState(STARTING).instanceCount(3)
                        .gpuSpecId(TEST_GPU_SPEC_ID).build()));

        var gpuSpec = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                .maxInstances(10).minInstances(8).maxRequestConcurrency(9)
                .clusters(Set.of("cluster02", "cluster03", "cluster01")).build();
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.ACTIVE);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, Set.of(gpuSpec),
                                           Instant.now().minus(Duration.ofDays(1)));
        Mockito.doThrow(new RuntimeException(
                        "There are no available clusters with capacity for L40 GPU"))
                .when(icmsAllocatorService)
                .scheduleNewInstance(any(), any(), any(), anyInt());

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext).isPresent();

        functionDeploymentReconciliationService.reconcile(
                function.get(), deploymentContext.get());

        // 8 required - 6 starting or running = 2, so allocation really was attempted and
        // really did fail; without this the assertion below would also hold if allocation
        // had never been tried.
        verify(icmsAllocatorService, times(1))
                .scheduleNewInstance(any(), eq(TEST_DEPLOYMENT_ID), any(), eq(2));

        // Failing to add instances says nothing about the ones already running, so the
        // function must not be reported as having lost all of them.
        var resultFunction =
                functionLookupService.lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID,
                                                                        TEST_VERSION_ID_1);
        assertThat(resultFunction).isPresent();
        assertThat(resultFunction.get().getFunctionStatus()).isEqualTo(FunctionStatus.DEGRADING);
    }

    @Test
    void shouldTransitionDeployingFunctionToErrorWhenRemoteInstanceLookupFails() {
        var failingIcmsClient = Mockito.mock(IcmsClient.class);
        var serviceWithFailingRemoteCall = new FunctionDeploymentReconciliationService(
                failingIcmsClient, functionDeploymentService, instanceService,
                icmsAllocatorService);
        var gpuSpec = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                .maxInstances(10).minInstances(1).maxRequestConcurrency(9)
                .clusters(Set.of("cluster02", "cluster03", "cluster01")).build();
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, Set.of(gpuSpec),
                                           Instant.now().minus(Duration.ofDays(1)));
        Mockito.when(failingIcmsClient.getInstancesByDeploymentId(
                        TEST_NCA_ID, TEST_DEPLOYMENT_ID))
                .thenThrow(new UpstreamException("ICMS HTTP 500"));

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext).isPresent();

        serviceWithFailingRemoteCall.reconcile(function.get(), deploymentContext.get());

        var resultFunction =
                functionLookupService.lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID,
                                                                        TEST_VERSION_ID_1);
        assertThat(resultFunction).isPresent();
        assertThat(resultFunction.get().getFunctionStatus()).isEqualTo(FunctionStatus.ERROR);

        var dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(ERROR);
        assertThat(dto.healthInfo()).isNotNull();
        var errors = dto.healthInfo().stream().map(DeploymentHealthDto::error)
                .collect(Collectors.toSet());
        assertThat(errors).hasSize(1);
        assertThat(errors.iterator().next())
                .contains(TEST_FUNCTION_ID.toString())
                .contains(TEST_VERSION_ID_1.toString())
                .contains(TEST_DEPLOYMENT_ID.toString())
                .contains("Failed to update deploying function status: ICMS HTTP 500");
    }

    Stream<Arguments> errorPropagationArgs() {
        var healthyGpu1 = IcmsInstancesContext.builder()
                .instanceState(RUNNING).instanceCount(1).gpuSpecId(TEST_GPU_SPEC_ID).
                build();
        var unhealthyGpu1 = IcmsInstancesContext.builder()
                .instanceState(SHUTTING_DOWN).instanceCount(1).gpuSpecId(TEST_GPU_SPEC_ID).
                build();
        var healthyGpu2 = IcmsInstancesContext.builder()
                .instanceState(RUNNING).instanceCount(1).gpuSpecId(TEST_GPU_SPEC_ID_2).
                build();
        var unhealthyGpu2 = IcmsInstancesContext.builder()
                .instanceState(TERMINATED).instanceCount(1).gpuSpecId(TEST_GPU_SPEC_ID_2).
                build();

        return Stream.of(
                Arguments.of(1, 1, List.of(healthyGpu1, healthyGpu2), ACTIVE, 0),
                Arguments.of(1, 1, List.of(healthyGpu1, unhealthyGpu2), DEPLOYING, 1),
                Arguments.of(1, 1,
                             List.of(healthyGpu1, unhealthyGpu1, healthyGpu2), ACTIVE, 0),
                Arguments.of(1, 1,
                             List.of(healthyGpu1, unhealthyGpu1, unhealthyGpu2), DEPLOYING, 1),
                Arguments.of(1, 1,
                             List.of(healthyGpu1, unhealthyGpu1, healthyGpu2, unhealthyGpu2),
                             ACTIVE, 0),
                Arguments.of(1, 1, List.of(unhealthyGpu1, unhealthyGpu2), ERROR, 2),
                Arguments.of(0, 1, List.of(healthyGpu1, healthyGpu2), ACTIVE, 0),
                Arguments.of(0, 0, List.of(healthyGpu1, healthyGpu2), ACTIVE, 0),
                Arguments.of(0, 0, List.of(healthyGpu1, unhealthyGpu1, healthyGpu2),
                             ACTIVE, 0),
                Arguments.of(0, 0,
                             List.of(healthyGpu1, unhealthyGpu1, healthyGpu2, unhealthyGpu2),
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
        MockIcmsServer.start(9096, instancesContexts);

        // before
        var gpuSpecs = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                     .build())
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(minInstanceT10).maxRequestConcurrency(9)
                        .clusters(Set.of("cluster02", "cluster03", "cluster01")).build(),
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID_2)
                                     .build())
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(minInstanceL40G).maxRequestConcurrency(9)
                        .clusters(Set.of("cluster02", "cluster03", "cluster01")).build());
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs);

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext).isPresent();

        // Enable async task and invoke it synchronously.
        functionDeploymentReconciliationService.reconcile(
                function.get(), deploymentContext.get());

        var dto = testService.getFunctionDeployment(TestConstants.TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(expectedFunctionStatus);
        // check healthy info
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
        // before
        var gpuSpecs = Set.of(
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID)
                                     .build())
                        .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(1).maxRequestConcurrency(9)
                        .clusters(Set.of("cluster02", "cluster03", "cluster01")).build(),
                GpuSpecificationEntity.builder()
                        .key(GpuSpecificationKey.builder()
                                     .ncaId(TEST_NCA_ID)
                                     .deploymentId(TEST_DEPLOYMENT_ID)
                                     .gpuSpecificationId(TEST_GPU_SPEC_ID_2)
                                     .build())
                        .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                        .maxInstances(5).minInstances(1).maxRequestConcurrency(9)
                        .clusters(Set.of("cluster02", "cluster03", "cluster01")).build());

        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs);
        // Set Wiremock server to serve responses with healthy T10s and unhealthy L40Gs.
        var instancesContexts = List.of(
                IcmsInstancesContext.builder()
                        .instanceState(RUNNING)
                        .instanceCount(1)
                        .gpuSpecId(TEST_GPU_SPEC_ID).
                        build(),
                IcmsInstancesContext.builder()
                        .instanceState(TERMINATED)
                        .instanceCount(1)
                        .gpuSpecId(TEST_GPU_SPEC_ID_2)
                        .build()
        );
        MockIcmsServer.start(9096, instancesContexts);

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext = functionDeploymentLookupService
                .getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext).isPresent();

        // Enable async task and invoke it synchronously.
        functionDeploymentReconciliationService.reconcile(
                function.get(), deploymentContext.get());

        // Check the deployment.
        var dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                    TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(DEPLOYING);
        assertThat(dto.healthInfo()).isNotNull();
        var errors = dto.healthInfo().stream().map(DeploymentHealthDto::error)
                .collect(Collectors.toSet());
        assertThat(errors).hasSize(1);
        log.info("Health: '{}'", dto.healthInfo());

        // Disable async task and reset Wiremock server to serve responses with
        // unhealthy T10s and healthy L40Gs.
        instancesContexts = List.of(
                IcmsInstancesContext.builder()
                        .instanceState(SHUTTING_DOWN)
                        .instanceCount(1)
                        .gpuSpecId(TEST_GPU_SPEC_ID).
                        build(),
                IcmsInstancesContext.builder()
                        .instanceState(RUNNING)
                        .instanceCount(1)
                        .gpuSpecId(TEST_GPU_SPEC_ID_2)
                        .build()
        );
        MockIcmsServer.start(9096, instancesContexts);

        function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContextFirst = functionDeploymentLookupService
                .getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContextFirst).isPresent();

        // Re-enable async task and invoke it synchronously.
        functionDeploymentReconciliationService.reconcile(
                function.get(), deploymentContextFirst.get());

        // Check the deployment.
        dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(DEPLOYING);
        assertThat(dto.healthInfo()).isNotNull();
        errors = dto.healthInfo().stream().map(DeploymentHealthDto::error)
                .collect(Collectors.toSet());
        assertThat(errors).hasSize(1);

        // Disable async task and reset Wiremock server to serve responses with
        // healthy T10s and healthy L40Gs.
        instancesContexts = List.of(
                IcmsInstancesContext.builder()
                        .instanceState(RUNNING)
                        .instanceCount(1)
                        .gpuSpecId(TEST_GPU_SPEC_ID).
                        build(),
                IcmsInstancesContext.builder()
                        .instanceState(RUNNING)
                        .instanceCount(1)
                        .gpuSpecId(TEST_GPU_SPEC_ID_2)
                        .build()
        );
        MockIcmsServer.start(9096, instancesContexts);

        function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContextSecond = functionDeploymentLookupService
                .getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContextSecond).isPresent();

        // Re-enable async task and invoke it synchronously.
        functionDeploymentReconciliationService.reconcile(
                function.get(), deploymentContextSecond.get());

        // Check the deployment.
        dto = testService.getFunctionDeployment(TEST_NCA_ID, TEST_FUNCTION_ID,
                                                TEST_VERSION_ID_1);
        assertThat(dto.functionStatus()).isEqualTo(ACTIVE);
        assertThat(dto.healthInfo()).isNull();
    }

    Stream<Arguments> allocateOrTerminateArgs() {
        // Deployment has two gpu specs with min/max = [2, 5]. Allocation counts both RUNNING
        // and STARTING instances; termination counts only RUNNING instances so that pending
        // instances without stable instance IDs are never deleted.
        // InstanceCount order: RUNNING, STARTING, SHUTTING_DOWN, TERMINATED
        // Arguments order: gpu1InstanceCounts, expectedGpu1Create, expectedGpu1Delete,
        // gpu2InstanceCounts, expectedGpu2Create, expectedGpu2Delete
        return Stream.of(
                // 1. Min/max in bounds, no actions
                Arguments.of(List.of(1, 2, 2, 2), 0, 0, List.of(2, 1, 0, 0), 0, 0),
                // 2. Should create 2 only in gpu1
                Arguments.of(List.of(0, 0, 0, 0), 2, 0, List.of(2, 1, 0, 0), 0, 0),
                // 3. Should create 1 only in gpu2
                Arguments.of(List.of(2, 1, 1, 3), 0, 0, List.of(0, 1, 3, 3), 1, 0),
                // 4. Should create 1 in gpu1 and 2 in gpu2
                Arguments.of(List.of(0, 1, 1, 3), 1, 0, List.of(0, 0, 3, 3), 2, 0),
                // 5. Do not terminate gpu1: only 3 instances are RUNNING.
                Arguments.of(List.of(3, 3, 1, 3), 0, 0, List.of(1, 2, 3, 3), 0, 0),
                // 6. Do not terminate gpu2: only 5 instances are RUNNING.
                Arguments.of(List.of(3, 1, 1, 3), 0, 0, List.of(5, 2, 3, 3), 0, 0),
                // 7. Do not terminate either GPU spec when their excess instances are STARTING.
                Arguments.of(List.of(3, 3, 1, 3), 0, 0, List.of(5, 2, 3, 3), 0, 0),
                // 8. Do not terminate gpu1; create 2 instances in gpu2.
                Arguments.of(List.of(3, 3, 1, 3), 0, 0, List.of(0, 0, 3, 3), 2, 0),
                // 9. Create 1 in gpu1; do not terminate gpu2's STARTING instances.
                Arguments.of(List.of(0, 1, 1, 3), 1, 0, List.of(2, 5, 3, 3), 0, 0),
                // 10. Terminate only the excess RUNNING instance in gpu1.
                Arguments.of(List.of(6, 0, 0, 0), 0, 1, List.of(2, 1, 0, 0), 0, 0)
        );
    }

    @MethodSource("allocateOrTerminateArgs")
    @ParameterizedTest
    void shouldAllocateOrTerminate(List<Integer> gpu1InstanceCounts,
                                   int expectedGpu1Create,
                                   int expectedGpu1Delete,
                                   List<Integer> gpu2InstanceCounts,
                                   int expectedGpu2Create,
                                   int expectedGpu2Delete) {
        // before
        GpuSpecificationEntity gpuSpec1 = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(T10).instanceType(T10_INSTANCE_TYPE)
                .maxInstances(5).minInstances(2).maxRequestConcurrency(9)
                .clusters(Set.of("cluster02", "cluster03", "cluster01"))
                .build();
        GpuSpecificationEntity gpuSpec2 = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID_2)
                             .build())
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                .maxInstances(5).minInstances(2).maxRequestConcurrency(9)
                .clusters(Set.of("cluster02", "cluster03", "cluster01"))
                .build();
        var gpuSpecs = Set.of(gpuSpec1, gpuSpec2);

        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.ACTIVE);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, gpuSpecs);

        List<IcmsInstancesContext> instancesContexts = new ArrayList<>();
        instancesContexts.addAll(buildInstancesContexts(gpu1InstanceCounts, TEST_GPU_SPEC_ID));
        instancesContexts.addAll(buildInstancesContexts(gpu2InstanceCounts, TEST_GPU_SPEC_ID_2));
        MockIcmsServer.start(9096, instancesContexts);

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var deploymentContext = functionDeploymentLookupService
                .getDeploymentContextByVersionId(TEST_VERSION_ID_1);

        assertThat(function).isPresent();
        assertThat(deploymentContext).isPresent();

        // Re-enable async task and invoke it synchronously.
        functionDeploymentReconciliationService.reconcile(
                function.get(), deploymentContext.get());

        var deploymentId = deploymentContext.get().deployment().getDeploymentId();
        // If the same method was invoked twice, Mockito cannot find by argument correct method.
        if (expectedGpu1Create > 0 && expectedGpu2Create > 0) {
            verify(icmsAllocatorService, times(2)).scheduleNewInstance(
                    eq(function.get()), eq(deploymentId), any(), anyInt());
        } else if (expectedGpu1Create > 0) {
            verify(icmsAllocatorService, times(1)).scheduleNewInstance(
                    function.get(), deploymentId,
                    gpuSpec1, expectedGpu1Create);
        } else if (expectedGpu2Create > 0) {
            verify(icmsAllocatorService, times(1)).scheduleNewInstance(
                    function.get(), deploymentId,
                    gpuSpec2, expectedGpu2Create);
        } else {
            verify(icmsAllocatorService, never()).scheduleNewInstance(
                    any(), any(), any(), anyInt());
        }

        if (expectedGpu1Delete > 0 && expectedGpu2Delete > 0) {
            verify(icmsAllocatorService, times(2))
                    .deleteInstances(eq(function.get()), anyInt(), any());
        } else if (expectedGpu1Delete > 0) {
            verify(icmsAllocatorService, times(1))
                    .deleteInstances(
                            eq(function.get()), eq(expectedGpu1Delete), any());
        } else if (expectedGpu2Delete > 0) {
            verify(icmsAllocatorService, times(1))
                    .deleteInstances(
                            eq(function.get()), eq(expectedGpu2Delete), any());
        } else {
            verify(icmsAllocatorService, never())
                    .deleteInstances(eq(function.get()), anyInt(), any());
        }
    }

    // List<Integer> counts is an array of how many instances of each status, i.e count[0] shows
    // how many RUNNING instances, count[1]: STARTING, counts[2] SUTTING_DOWN
    // and counts[3] TERMINATING
    private static List<IcmsInstancesContext> buildInstancesContexts(List<Integer> counts,
                                                                     UUID gpuSpecId) {
        return List.of(
                IcmsInstancesContext.builder()
                        .instanceState(RUNNING)
                        .instanceCount(counts.get(0))
                        .gpuSpecId(gpuSpecId).
                        build(),
                IcmsInstancesContext.builder()
                        .instanceState(STARTING)
                        .instanceCount(counts.get(1))
                        .gpuSpecId(gpuSpecId).
                        build(),
                IcmsInstancesContext.builder()
                        .instanceState(SHUTTING_DOWN)
                        .instanceCount(counts.get(2))
                        .gpuSpecId(gpuSpecId).
                        build(),
                IcmsInstancesContext.builder()
                        .instanceState(TERMINATED)
                        .instanceCount(counts.get(3))
                        .gpuSpecId(gpuSpecId)
                        .build()
        );
    }
}
