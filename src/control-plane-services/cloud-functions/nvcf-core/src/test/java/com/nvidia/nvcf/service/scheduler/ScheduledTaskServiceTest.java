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
package com.nvidia.nvcf.service.scheduler;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_STATUS;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_INSTANCE_MANAGEMENT_TASK;
import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_GPU_SPEC_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.verify;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationKey;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.util.MockIcmsServer;
import io.micrometer.tracing.Span;
import io.micrometer.tracing.Tracer;
import java.time.Duration;
import java.time.Instant;
import java.util.Optional;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.util.ReflectionTestUtils;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@ExtendWith(MockitoExtension.class)
class ScheduledTaskServiceTest {

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private FunctionDeploymentService functionDeploymentService;

    @Autowired
    private FunctionDeploymentLookupService functionDeploymentLookupService;

    @Autowired
    private GracefulCleanDeploymentTask gracefulCleanDeploymentTask;

    @Autowired
    private CleanNatsStreamsTask cleanNatsStreamsTask;

    @Autowired
    private InstanceManagementTask instanceManagementTask;

    @Autowired
    private AccountService accountService;

    @Autowired
    private JsonMapper jsonMapper;

    @Mock
    private Tracer tracer;

    @Mock
    private Span span;

    private ScheduledTaskService scheduledTaskService;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockIcmsServer.start(9096, jsonMapper);
    }

    @BeforeEach
    void beforeEach() {
        scheduledTaskService = new ScheduledTaskService(
                functionLookupService,
                gracefulCleanDeploymentTask,
                cleanNatsStreamsTask,
                Optional.of(1),
                instanceManagementTask,
                functionDeploymentLookupService,
                accountService,
                tracer,
                functionDeploymentService);
    }

    @AfterEach
    void reset() {
        scheduledTaskService.close();
        testCommonService.reset();
    }

    @AfterAll
    void cleanup() {
        MockIcmsServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @Test
    void shouldCleanupErroredDeploymentWhenLastUpdatedAtIsOlderThanThreshold() {
        var gpuSpec = GpuSpecificationEntity.builder()
                .key(GpuSpecificationKey.builder()
                             .ncaId(TEST_NCA_ID)
                             .deploymentId(TEST_DEPLOYMENT_ID)
                             .gpuSpecificationId(TEST_GPU_SPEC_ID)
                             .build())
                .gpu(L40G).instanceType(L40G_INSTANCE_TYPE)
                .maxInstances(10).minInstances(0).maxRequestConcurrency(9)
                .clusters(Set.of("cluster02", "cluster03", "cluster01")).build();
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.ERROR);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID, Set.of(gpuSpec),
                                           Instant.now());

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);
        assertThat(deploymentContext).isPresent();
        deploymentContext.get().deployment()
                .setLastUpdatedAt(Instant.now().minus(Duration.ofDays(10)));
        functionDeploymentService.save(deploymentContext.get());

        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID, TEST_VERSION_ID_1);
        var staleDeploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1);
        assertThat(function).isPresent();
        assertThat(staleDeploymentContext).isPresent();

        var resultFunction = scheduledTaskService.handleFunctionDeployment(
                function.get(), staleDeploymentContext.get(), span);

        assertThat(resultFunction).isSameAs(function.get());
        verify(span).tag(SPAN_TAG_FUNCTION_STATUS, FunctionStatus.ERROR.toString());
        var savedFunction =
                functionLookupService.lookupUsingFunctionIdAndVersionId(TEST_FUNCTION_ID,
                                                                        TEST_VERSION_ID_1);
        assertThat(savedFunction).isPresent();
        assertThat(savedFunction.get().getFunctionStatus()).isEqualTo(FunctionStatus.INACTIVE);
        assertThat(functionDeploymentLookupService.getDeploymentContextByVersionId(
                TEST_VERSION_ID_1)).isEmpty();
    }
}
