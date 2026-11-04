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
package com.nvidia.nvcf.configuration;

import static com.nvidia.nvcf.configuration.ratelimit.RateLimiterPolicy.BUCKET;
import static com.nvidia.nvcf.configuration.ratelimit.RateLimiterPolicy.LEGACY;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.configuration.ratelimit.AccountRateLimiterProperties;
import com.nvidia.nvcf.configuration.ratelimit.FunctionRateLimiterProperties;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@ExtendWith(MockitoExtension.class)
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class RateLimiterPropertiesTest {

    @Autowired
    private AccountRateLimiterProperties accountProperties;
    @Autowired
    private FunctionRateLimiterProperties functionProperties;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
    }

    @AfterAll
    void cleanup() {
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @Test
    void testAccountProperties() {
        assertThat(accountProperties).isNotNull();
        assertThat(accountProperties.getPolicy()).isEqualTo(BUCKET);
        assertThat(accountProperties
                           .getDefaultRateCappingProperties()
                           .getAllowedInvocationsPerSecond()).isEqualTo(200L);
        assertThat(accountProperties
                           .getDefaultRateCappingProperties()
                           .getPolicy()).isEqualTo(BUCKET);
        assertThat(accountProperties
                           .getOverridesMap()).containsOnlyKeys(TEST_NCA_ID, TEST_NCA_ID_2);
        assertThat(accountProperties
                           .getOverridesMap()
                           .get(TEST_NCA_ID)
                           .getPolicy()).isEqualTo(BUCKET);
        assertThat(accountProperties
                           .getOverridesMap()
                           .get(TEST_NCA_ID)
                           .getAllowedInvocationsPerSecond()).isEqualTo(100);
        assertThat(accountProperties
                           .getOverridesMap()
                           .get(TEST_NCA_ID_2)
                           .getPolicy()).isEqualTo(LEGACY);
        assertThat(accountProperties
                           .getOverridesMap()
                           .get(TEST_NCA_ID_2)
                           .getAllowedInvocationsPerSecond()).isEqualTo(150);
    }

    @Test
    void testFunctionProperties() {
        assertThat(functionProperties).isNotNull();
        assertThat(functionProperties
                           .getDefaultRateCappingProperties()
                           .getPolicy()).isEqualTo(BUCKET);
        assertThat(functionProperties
                           .getDefaultRateCappingProperties()
                           .getAllowedInvocationsPerSecond()).isEqualTo(250);
        assertThat(functionProperties.getFunctionOverridesMap()).isEmpty();

        assertThat(functionProperties.getVersionOverridesMap()).containsOnlyKeys(
                TEST_VERSION_ID_1,
                UUID.fromString("d861aa79-eb4b-4407-a8b3-a33a867318e3"));
        assertThat(functionProperties.getVersionOverridesMap().get(TEST_VERSION_ID_1)
                           .getPolicy())
                .isEqualTo(BUCKET);
        assertThat(functionProperties.getVersionOverridesMap().get(TEST_VERSION_ID_1)
                           .getAllowedInvocationsPerSecond())
                .isEqualTo(300);
    }
}
