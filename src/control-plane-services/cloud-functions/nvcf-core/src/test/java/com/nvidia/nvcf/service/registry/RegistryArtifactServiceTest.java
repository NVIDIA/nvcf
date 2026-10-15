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
package com.nvidia.nvcf.service.registry;

import static com.nvidia.nvcf.rest.function.management.TestManagementService.Trait.CONTAINER_BASED;
import static com.nvidia.nvcf.rest.function.management.TestManagementService.Trait.USING_MODELS;
import static com.nvidia.nvcf.rest.function.management.TestManagementService.Trait.USING_RESOURCES;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.function.management.TestManagementService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockEssServer;
import java.util.EnumSet;
import java.util.UUID;
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

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class RegistryArtifactServiceTest {

    @Autowired
    private RegistryArtifactService registryArtifactService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestManagementService testManagementService;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryHostname;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryHostname);
    }

    @AfterAll
    void cleanup() {
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void setup() {
        // Functions will be created in individual test methods
    }

    @AfterEach
    void afterEach() {
        testCommonService.reset();
    }

    Stream<Arguments> cacheHandleArgs() {
        return Stream.of(
                // Function with no artifacts
                Arguments.of(
                        TEST_FUNCTION_ID,
                        TEST_VERSION_ID_1,
                        EnumSet.of(CONTAINER_BASED),
                        "empty"),
                // Function with models only
                Arguments.of(
                        TEST_FUNCTION_ID_2,
                        TEST_VERSION_ID_1,
                        EnumSet.of(CONTAINER_BASED, USING_MODELS),
                        "models-only"),
                // Different function with same models (should have same hash)
                Arguments.of(
                        TEST_FUNCTION_ID_2,
                        TEST_VERSION_ID_2,
                        EnumSet.of(CONTAINER_BASED, USING_MODELS),
                        "models-only"),
                // Function with resources only
                Arguments.of(
                        TEST_FUNCTION_ID_3,
                        TEST_VERSION_ID_1,
                        EnumSet.of(CONTAINER_BASED, USING_RESOURCES),
                        "resources-only"),
                // Function with both models and resources
                Arguments.of(
                        TEST_FUNCTION_ID,
                        TEST_VERSION_ID_2,
                        EnumSet.of(CONTAINER_BASED, USING_MODELS, USING_RESOURCES),
                        "models-and-resources")
        );
    }

    @ParameterizedTest
    @MethodSource("cacheHandleArgs")
    void shouldGenerateConsistentCacheHandle(UUID functionId,
                                             UUID versionId,
                                             EnumSet<TestManagementService.Trait> traits,
                                             String expectedHashGroup) {
        // Create a function with the specified artifacts
        testManagementService.createFunctionEntityWithTraits(
                functionId, versionId, TEST_NCA_ID, TEST_FUNCTION_NAME, traits);

        // Get the cache handle multiple times
        String firstCall = registryArtifactService.getCacheHandle(functionId, versionId);
        String secondCall = registryArtifactService.getCacheHandle(functionId, versionId);
        String thirdCall = registryArtifactService.getCacheHandle(functionId, versionId);

        // All calls should return the same value (consistency and caching)
        assertThat(firstCall)
                .isNotNull()
                .hasSize(32) // Cache handle should be 32 characters
                .isEqualTo(secondCall)
                .isEqualTo(thirdCall);
    }

    @Test
    void shouldGenerateSameCacheHandleForSameArtifacts() {
        // Create two different function versions with the same models
        testManagementService.createFunctionEntityWithTraits(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_1, TEST_NCA_ID, TEST_FUNCTION_NAME,
                EnumSet.of(CONTAINER_BASED, USING_MODELS));
        testManagementService.createFunctionEntityWithTraits(
                TEST_FUNCTION_ID_2, TEST_VERSION_ID_2, TEST_NCA_ID, TEST_FUNCTION_NAME,
                EnumSet.of(CONTAINER_BASED, USING_MODELS));

        // Get cache handles for both
        var cacheHandle1 = registryArtifactService.getCacheHandle(TEST_FUNCTION_ID_2, TEST_VERSION_ID_1);
        var cacheHandle2 = registryArtifactService.getCacheHandle(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2);

        // Both should have identical cache handles since they use the same models
        assertThat(cacheHandle1)
                .isEqualTo(cacheHandle2)
                .as("Two different function versions with identical models should produce the same cacheHandle");
    }
}
