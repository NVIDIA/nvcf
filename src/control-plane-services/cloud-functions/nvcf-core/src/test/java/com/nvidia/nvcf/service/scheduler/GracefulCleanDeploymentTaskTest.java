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

import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.util.List;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class GracefulCleanDeploymentTaskTest {

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestQueueService testQueueService;

    @Autowired
    private FunctionsDeploymentRepository deploymentRepository;

    @Autowired
    private GracefulCleanDeploymentTask gracefulCleanDeploymentTask;

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

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockIcmsServer.start(9096, jsonMapper);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
        testAccountService.createDefaultAccountsClientsAndRegistries();
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

    @SneakyThrows
    @Test
    void gracefullyDeletingDeploymentTestAssumingQueueDrained() {
        // Create functions with ACTIVE status.
        var entity1 = testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                           TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                           FunctionStatus.ACTIVE);
        var entity2 = testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                                           TEST_NCA_ID, TEST_FUNCTION_NAME_2,
                                                           FunctionStatus.ACTIVE);

        // Create entries in functions_deployment_v2 table for the two functions.
        testService.createDeploymentEntity(TEST_FUNCTION_ID,
                                           TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID, TEST_NCA_ID);
        testService.createDeploymentEntity(TEST_FUNCTION_ID_2,
                                           TEST_VERSION_ID_2, TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        // Verify entries in the deployment table
        var deploymentEntity1 = deploymentRepository.getByKeyFunctionVersionId(TEST_VERSION_ID_1);
        assertThat(deploymentEntity1).isPresent();
        var deploymentEntity2 = deploymentRepository.getByKeyFunctionVersionId(TEST_VERSION_ID_2);
        assertThat(deploymentEntity2).isPresent();

        // Change status of TEST_VERSION_ID_1 function to INACTIVE.
        entity1.setFunctionStatus(FunctionStatus.INACTIVE);
        entity1 = functionsRepository.insert(entity1);

        var deploymentContext1 = new FunctionDeploymentContext(deploymentEntity1.get(), List.of());
        var deploymentContext2 = new FunctionDeploymentContext(deploymentEntity2.get(), List.of());

        gracefulCleanDeploymentTask.runUnchecked(entity1, deploymentContext1);
        gracefulCleanDeploymentTask.runUnchecked(entity2, deploymentContext2);

        // Verify entry in the deployment table is deleted.
        deploymentEntity1 = deploymentRepository.getByKeyFunctionVersionId(TEST_VERSION_ID_1);
        assertThat(deploymentEntity1).isEmpty();
        // Verify active deployment still exists
        deploymentEntity2 = deploymentRepository.getByKeyFunctionVersionId(TEST_VERSION_ID_2);
        assertThat(deploymentEntity2).isPresent();
    }
}
