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
package com.nvidia.nvct.service.registry;

import static com.nvidia.nvct.util.TestConstants.TEST_MODELS;
import static com.nvidia.nvct.util.TestConstants.TEST_MODELS_2;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_4;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_5;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_4;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_5;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.util.MockNvcfServer;
import java.net.URL;
import java.util.UUID;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class RegistryArtifactServiceTest {

    @Autowired
    private RegistryArtifactService registryArtifactService;

    @Autowired
    private TestTaskService testTaskService;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryHostname;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryHostname);
        MockNvcfServer.start(nvcfBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockCasServer.stop();
        MockNvcfServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void setup() {
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        testTaskService.createTaskWithModel(TEST_NCA_ID, TEST_TASK_ID_2, TEST_ICMS_REQ_ID_2,
                                            TEST_MODELS);
        testTaskService.createTaskWithModel(TEST_NCA_ID, TEST_TASK_ID_3, TEST_ICMS_REQ_ID_3,
                                            TEST_MODELS_2);
        testTaskService.createTaskWithModelAndResources(TEST_NCA_ID_2, TEST_TASK_ID_4,
                                                        TEST_ICMS_REQ_ID_4);
        testTaskService.createTaskWithResource(TEST_NCA_ID_2, TEST_TASK_ID_5,
                                               TEST_ICMS_REQ_ID_5);
    }

    @AfterEach
    void afterEach() {
        registryArtifactService.invalidateCache();
        testTaskService.clearAll();
    }

    Stream<Arguments> cacheHandleArgs() {
        return Stream.of(
                Arguments.of(
                        TEST_NCA_ID,
                        TEST_TASK_ID_1,
                        "e3b0c44298fc1c149afbf4c8996fb924"),
                Arguments.of(
                        TEST_NCA_ID,
                        TEST_TASK_ID_2,
                        "a941b29b4d8cdea172401c82cd9cdcee"),
                Arguments.of(
                        TEST_NCA_ID,
                        TEST_TASK_ID_3,
                        "a941b29b4d8cdea172401c82cd9cdcee"),
                Arguments.of(
                        TEST_NCA_ID_2,
                        TEST_TASK_ID_4,
                        "da56d7b9f5b145646f32b571d19ae4bf"),
                Arguments.of(
                        TEST_NCA_ID_2,
                        TEST_TASK_ID_5,
                        "a492d24950a022cebff9f6e6c9dad0f2"));
    }

    @ParameterizedTest
    @MethodSource("cacheHandleArgs")
    void testCacheHandle(String ncaId, UUID taskId, String cacheHandle) {
        assertThat(registryArtifactService.getCacheHandle(ncaId, taskId)).isEqualTo(cacheHandle);
    }
}
