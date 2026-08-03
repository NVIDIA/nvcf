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
package com.nvidia.nvct.service.ratelimit;

import static com.nvidia.nvct.util.NvctConstants.UUID_WILDCARD;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static org.assertj.core.api.Assertions.assertThat;
import static org.awaitility.Awaitility.await;
import static org.mockito.Mockito.lenient;

import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.configuration.ratelimit.AccountRateLimiterProperties;
import com.nvidia.nvct.configuration.ratelimit.AccountRateLimiterProperties.AccountRateCappingProperties;
import com.nvidia.nvct.configuration.ratelimit.TaskRateLimiterProperties;
import com.nvidia.nvct.configuration.ratelimit.TaskRateLimiterProperties.TaskRateCappingProperties;
import java.time.Duration;
import java.util.Collections;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.stream.IntStream;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@ExtendWith(MockitoExtension.class)
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class RateLimiterServiceTest {

    @Mock
    private AccountRateLimiterProperties accountProperties;
    @Mock
    private TaskRateLimiterProperties taskProperties;

    private RateLimiterService service;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
    }

    @AfterAll
    void cleanup() {
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void beforeEach() {
        service = new RateLimiterService(accountProperties, taskProperties);
    }

    private static Stream<Arguments> accountRateLimiterUseCases() {
        return Stream.of(
                // Set up default rate limiting policy for accounts with cap of 10000 calls per sec.
                //
                // Allowed Calls: 10 TEST_TASK_ID_1/TEST_NCA_ID + 10 TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 0
                Arguments.of(createAccountOverrides("*", 10000L),
                             Collections.emptyMap(),
                             20,
                             0),

                // Set up default rate limiting policy for accounts with cap of 6 calls per second.
                //
                // Allowed Calls: 6 TEST_TASK_ID_1/TEST_NCA_ID + 6 TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 4 TEST_TASK_ID_1/TEST_NCA_ID + 4 TEST_TASK_ID_2/TEST_NCA_ID_2
                Arguments.of(createAccountOverrides("*", 6L),
                             Collections.emptyMap(),
                             12,
                             8),

                // Set up default rate limiting policy for accounts with cap of 6 calls per second.
                // Override the default cap for TEST_NCA_ID with 4 calls per second.
                //
                // Allowed Calls: 4 TEST_TASK_ID_1/TEST_NCA_ID + 6 TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 6 TEST_TASK_ID_1/TEST_NCA_ID + 4 TEST_TASK_ID_2/TEST_NCA_ID_2
                Arguments.of(createAccountOverrides("*", 6L),
                             Map.of(TEST_NCA_ID, createAccountOverrides(TEST_NCA_ID, 4L)),
                             10,
                             10)
        );
    }

    @ParameterizedTest
    @MethodSource("accountRateLimiterUseCases")
    void verifyAccountRateLimit(
            AccountRateCappingProperties defaultAccountProperties,
            Map<String, AccountRateCappingProperties> accountOverrideIds,
            int expectedSuccess,
            int expectedDecline) {
        applyMocks(defaultAccountProperties, null, accountOverrideIds, null);

        AtomicInteger successful = new AtomicInteger();
        AtomicInteger declined = new AtomicInteger();
        IntStream.range(0, 10).parallel()
                .forEach(i -> {
                    try {
                        service.verifyLimits(TEST_NCA_ID);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                    try {
                        service.verifyLimits(TEST_NCA_ID_2);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                });
        assertThat(successful.get()).isEqualTo(expectedSuccess);
        assertThat(declined.get()).isEqualTo(expectedDecline);
    }

    private static Stream<Arguments> taskRateLimiterUseCases() {
        return Stream.of(
                // Set up default rate limiting policy for tasks with cap of 10000 calls per second.
                //
                // Allowed Calls: 10 TEST_TASK_ID_1/TEST_NCA_ID + 10 TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 0
                Arguments.of(createTaskOverrides(UUID_WILDCARD, 10000L),
                             Collections.emptyMap(),
                             20,
                             0),

                // Set up default rate limiting policy for tasks with cap of 8 calls per second.
                //
                // Allowed Calls: 8 TEST_TASK_ID_1/TEST_NCA_ID + 8 for TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 2 TEST_TASK_ID_1/TEST_NCA_ID + 2 for TEST_TASK_ID_2/TEST_NCA_ID_2
                Arguments.of(createTaskOverrides(UUID_WILDCARD, 8L),
                             Collections.emptyMap(),
                             16,
                             4),

                // Set up default rate limiting policy for tasks with cap of 8 calls per second.
                // Override the default cap for task TEST_TASK_ID_1 with 4 calls per second.
                //
                // Allowed Calls: 4 TEST_TASK_ID_1/TEST_NCA_ID + 8 for TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 6 TEST_TASK_ID_1/TEST_NCA_ID + 2 for TEST_TASK_ID_2/TEST_NCA_ID_2
                Arguments.of(createTaskOverrides(UUID_WILDCARD, 8L),
                             Map.of(TEST_TASK_ID_1, createTaskOverrides(TEST_TASK_ID_1, 4L)),
                             12,
                             8)
        );
    }

    @ParameterizedTest
    @MethodSource("taskRateLimiterUseCases")
    void verifyTaskRateLimit(
            TaskRateCappingProperties defaultTaskProperties,
            Map<UUID, TaskRateCappingProperties> taskOverrideIds,
            int expectedSuccess,
            int expectedDecline) {
        applyMocks(null, defaultTaskProperties, null, taskOverrideIds);

        AtomicInteger successful = new AtomicInteger();
        AtomicInteger declined = new AtomicInteger();
        IntStream.range(0, 10).parallel()
                .forEach(i -> {
                    try {
                        service.verifyLimits(TEST_TASK_ID_1);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                    try {
                        service.verifyLimits(TEST_TASK_ID_2);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                });
        assertThat(successful.get()).isEqualTo(expectedSuccess);
        assertThat(declined.get()).isEqualTo(expectedDecline);
    }

    private static Stream<Arguments> accountAndTaskRateLimiterUseCases() {
        return Stream.of(
                // Set up default rate limiting policy for accounts with cap of 10000 calls per sec.
                // Set up default rate limiting policy for tasks with cap of 10000 calls per second.
                //
                // Allowed Calls: 10 TEST_TASK_ID_1/TEST_NCA_ID + 10 TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 0
                Arguments.of(createAccountOverrides("*", 10000L),
                             createTaskOverrides(UUID_WILDCARD, 10000L),
                             Collections.emptyMap(),
                             Collections.emptyMap(),
                             20,
                             0),

                // Set up default rate limiting policy for accounts with cap of 10000 calls per sec.
                // Set up default rate limiting policy for tasks with cap of 6 calls per second.
                //
                // Allowed Calls: 6 TEST_TASK_ID_1/TEST_NCA_ID + 6 for TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 4 TEST_TASK_ID_1/TEST_NCA_ID + 4 for TEST_TASK_ID_2/TEST_NCA_ID_2
                Arguments.of(createAccountOverrides("*", 10000L),
                             createTaskOverrides(UUID_WILDCARD, 6L),
                             Collections.emptyMap(),
                             Collections.emptyMap(),
                             12,
                             8),

                // Set up default rate limiting policy for accounts with cap of 5 calls per second.
                // Set up default rate limiting policy for tasks with cap of 8 calls per second.
                //
                // Allowed Calls: 5 TEST_TASK_ID_1/TEST_NCA_ID + 5 for TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 5 TEST_TASK_ID_1/TEST_NCA_ID + 5 for TEST_TASK_ID_2/TEST_NCA_ID_2
                Arguments.of(createAccountOverrides("*", 5L),
                             createTaskOverrides(UUID_WILDCARD, 8L),
                             Collections.emptyMap(),
                             Collections.emptyMap(),
                             10,
                             10),

                // Set up default rate limiting policy for accounts with cap of 10 calls per second.
                // Set up default rate limiting policy for tasks with cap of 10 calls per second.
                // Override the default cap for account TEST_NCA_ID with 4 calls per second.
                // Override the default cap for task TEST_TASK_ID_1 with 4 calls per second.
                //
                // Allowed Calls: 4 TEST_TASK_ID_1/TEST_NCA_ID + 10 for TEST_TASK_ID_2/TEST_NCA_ID_2
                // Declined Calls: 6 TEST_TASK_ID_1/TEST_NCA_ID + 0 for TEST_TASK_ID_2/TEST_NCA_ID_2
                Arguments.of(createAccountOverrides("*", 10L),
                             createTaskOverrides(UUID_WILDCARD, 10L),
                             Map.of(TEST_NCA_ID, createAccountOverrides(TEST_NCA_ID, 4L)),
                             Map.of(TEST_TASK_ID_1, createTaskOverrides(TEST_TASK_ID_1, 4L)),
                             14,
                             6)
        );
    }

    @ParameterizedTest
    @MethodSource("accountAndTaskRateLimiterUseCases")
    void verifyAccountAndTaskRateLimit(
            AccountRateCappingProperties defaultAccountProperties,
            TaskRateCappingProperties defaultTaskProperties,
            Map<String, AccountRateCappingProperties> accountOverrideIds,
            Map<UUID, TaskRateCappingProperties> taskOverrideIds,
            int expectedSuccess,
            int expectedDecline) {
        applyMocks(defaultAccountProperties, defaultTaskProperties, accountOverrideIds,
                   taskOverrideIds);

        AtomicInteger successful = new AtomicInteger();
        AtomicInteger declined = new AtomicInteger();
        IntStream.range(0, 10).parallel()
                .forEach(i -> {
                    try {
                        service.verifyLimits(TEST_NCA_ID, TEST_TASK_ID_1);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                    try {
                        service.verifyLimits(TEST_NCA_ID_2, TEST_TASK_ID_2);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                });
        assertThat(successful.get()).isEqualTo(expectedSuccess);
        assertThat(declined.get()).isEqualTo(expectedDecline);
    }

    @Test
    void verifyAccountRateLimitWithFulfillment() {
        applyMocks(
                createAccountOverrides("*", 6),
                createTaskOverrides(UUID_WILDCARD, 6),
                Collections.emptyMap(),
                Collections.emptyMap());

        AtomicInteger successful = new AtomicInteger();
        AtomicInteger declined = new AtomicInteger();
        IntStream.range(0, 10).parallel()
                .forEach(i -> {
                    try {
                        service.verifyLimits(TEST_NCA_ID);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                });

        // Fulfil the buckets
        // Thread.sleep(2000L) analog
        await()
                .pollDelay(Duration.ofSeconds(2))
                .untilAsserted(() -> assertThat(true).isTrue());

        IntStream.range(0, 10).parallel()
                .forEach(i -> {
                    try {
                        service.verifyLimits(TEST_NCA_ID);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                });
        assertThat(successful.get()).isEqualTo(12);
        assertThat(declined.get()).isEqualTo(8);
    }

    private static TaskRateCappingProperties createTaskOverrides(
            UUID taskId,
            long allowedInvocationsPerSecond) {
        return TaskRateCappingProperties
                .builder()
                .taskId(taskId)
                .allowedInvocationsPerSecond(allowedInvocationsPerSecond)
                .build();
    }

    private static AccountRateCappingProperties createAccountOverrides(
            String ncaId,
            long allowedInvocationsPerSecond) {
        return AccountRateCappingProperties
                .builder()
                .ncaId(ncaId)
                .allowedInvocationsPerSecond(allowedInvocationsPerSecond)
                .build();
    }

    private void applyMocks(
            AccountRateCappingProperties defaultAccountProperties,
            TaskRateCappingProperties defaultTaskProperties,
            Map<String, AccountRateCappingProperties> accountOverrideIds,
            Map<UUID, TaskRateCappingProperties> taskOverrideIds) {
        lenient().when(accountProperties.getDefaultRateCappingProperties())
                .thenReturn(defaultAccountProperties);
        lenient().when(taskProperties.getDefaultRateCappingProperties())
                .thenReturn(defaultTaskProperties);
        lenient().when(accountProperties.getOverridesMap()).thenReturn(accountOverrideIds);
        lenient().when(taskProperties.getTaskOverridesMap()).thenReturn(taskOverrideIds);
    }
}
