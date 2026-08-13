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

import static com.nvidia.nvcf.util.TestConstants.L40G;
import static com.nvidia.nvcf.util.TestConstants.T10;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.GpuSpecificationsRepository;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockEssServer;
import com.nvidia.nvcf.util.MockIcmsServer;
import java.util.UUID;
import java.util.stream.Stream;
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
class FunctionLookupServiceEnrichDeploymentTest {

    @Autowired
    private TestDeploymentService testService;
    @Autowired
    private TestAccountService testAccountService;
    @Autowired
    private TestCommonService testCommonService;
    @Autowired
    private GpuSpecificationsRepository gpuSpecificationRepository;
    @Autowired
    private JsonMapper jsonMapper;
    @Autowired
    private FunctionDeploymentLookupService functionDeploymentLookupService;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;
    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;
    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;
    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

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
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
    }

    @Test
    void lookupDeploymentWithGpuSpecsReturnsDeploymentWithListFromGpuSpecificationsTable() {
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID,
                                           TEST_NCA_ID);

        assertThat(countGpuSpecsByDeploymentId(TEST_NCA_ID, TEST_DEPLOYMENT_ID)).isEqualTo(2);

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(TEST_VERSION_ID_1)
                        .orElseThrow();

        assertThat(deploymentContext.deployment()).isNotNull();
        assertThat(deploymentContext.deployment().getDeploymentId()).isEqualTo(TEST_DEPLOYMENT_ID);
        assertThat(deploymentContext.gpuSpecs()).hasSize(2);
        assertThat(
                deploymentContext.gpuSpecs().stream().map(GpuSpecificationEntity::getGpu).toList())
                .containsExactlyInAnyOrder(T10, L40G);
    }

    @Test
    void findGpuSpecsByDeploymentIdReturnsListFromRepository() {
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_NCA_ID,
                                             TEST_FUNCTION_NAME);
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1, TEST_DEPLOYMENT_ID,
                                           TEST_NCA_ID);

        var list = functionDeploymentLookupService.findGpuSpecsByNcaIdAndDeploymentId(
                TEST_NCA_ID, TEST_DEPLOYMENT_ID);

        assertThat(list).hasSize(2);
        assertThat(list.stream().map(
                GpuSpecificationEntity::getGpu).toList()).containsExactlyInAnyOrder(T10, L40G);
    }

    private long countGpuSpecsByDeploymentId(String ncaId, UUID deploymentId) {
        try (Stream<?> stream = gpuSpecificationRepository.findAllByKeyNcaIdAndKeyDeploymentId(
                ncaId, deploymentId)) {
            return stream.count();
        }
    }
}
