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
package com.nvidia.nvcf.service.ratelimit;

import static com.nvidia.nvcf.configuration.ratelimit.RateLimiterPolicy.BUCKET;
import static com.nvidia.nvcf.configuration.ratelimit.RateLimiterPolicy.LEGACY;
import static com.nvidia.nvcf.util.NvcfConstants.VERSION_WILDCARD;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.assertj.core.api.Assertions.assertThat;
import static org.awaitility.Awaitility.await;
import static org.mockito.Mockito.lenient;

import com.nvidia.boot.exceptions.TooManyRequestsException;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.configuration.ratelimit.AccountRateLimiterProperties;
import com.nvidia.nvcf.configuration.ratelimit.FunctionRateLimiterProperties;
import com.nvidia.nvcf.configuration.ratelimit.RateLimiterPolicy;
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
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class RateLimiterServiceTest {

    @Mock
    private AccountRateLimiterProperties accountProperties;
    @Mock
    private FunctionRateLimiterProperties functionProperties;

    private RateLimiterService service;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
    }

    @AfterAll
    void cleanup() {
        MockCasServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void beforeEach() {
        service = new RateLimiterService(accountProperties, functionProperties);
    }

    private static Stream<Arguments> getAccountRateLimiterUseCases() {
        return Stream.of(
                // default legacy policy
                Arguments.of(createAccountOverrides(LEGACY, "*", 6),
                             createFunctionOverrides(LEGACY, VERSION_WILDCARD, VERSION_WILDCARD, 6),
                             Collections.emptyMap(),
                             Collections.emptyMap(),
                        /* allowed */ 20,
                        /* declined */ 0),

                // all default
                Arguments.of(createAccountOverrides(BUCKET, "*", 6),
                             createFunctionOverrides(BUCKET, VERSION_WILDCARD, VERSION_WILDCARD, 6),
                             Collections.emptyMap(),
                             Collections.emptyMap(),
                        /* allowed 6 calls for id_1 + 6 calls for id_2 */ 12,
                        /* declined 4 calls for id_1 + 4 calls for id_2 */ 8),

                // one account/function is overridden, the other is default
                Arguments.of(createAccountOverrides(BUCKET, "*", 6),
                             createFunctionOverrides(BUCKET, VERSION_WILDCARD, VERSION_WILDCARD, 6),
                             Map.of(TEST_NCA_ID, createAccountOverrides(BUCKET, TEST_NCA_ID, 4L)),
                             Map.of(TEST_FUNCTION_ID,
                                    createFunctionOverrides(BUCKET, TEST_FUNCTION_ID, null, 4L)),
                        /* allowed 4 calls for id_1 + 6 calls for id_2 */ 10,
                        /* declined 6 calls for id_1 + 4 calls for id_2 */ 10));
    }

    @ParameterizedTest
    @MethodSource("getAccountRateLimiterUseCases")
    void verifyAccountCall(
            AccountRateLimiterProperties.AccountRateCappingProperties defaultAccountProperties,
            FunctionRateLimiterProperties.FunctionCappingProperties defaultFunctionProperties,
            Map<String, AccountRateLimiterProperties.AccountRateCappingProperties> accountOverrideIds,
            Map<UUID, FunctionRateLimiterProperties.FunctionCappingProperties> functionOverrideIds,
            int expectedSuccess,
            int expectedDecline) {
        applyMocks(defaultAccountProperties, defaultFunctionProperties, accountOverrideIds,
                   functionOverrideIds);

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

    @ParameterizedTest
    @MethodSource("getAccountRateLimiterUseCases")
    void verifyFunctionCall(
            AccountRateLimiterProperties.AccountRateCappingProperties defaultAccountProperties,
            FunctionRateLimiterProperties.FunctionCappingProperties defaultFunctionProperties,
            Map<String, AccountRateLimiterProperties.AccountRateCappingProperties> accountOverrideIds,
            Map<UUID, FunctionRateLimiterProperties.FunctionCappingProperties> functionOverrideIds,
            int expectedSuccess,
            int expectedDecline) {
        applyMocks(defaultAccountProperties, defaultFunctionProperties, accountOverrideIds,
                   functionOverrideIds);

        AtomicInteger successful = new AtomicInteger();
        AtomicInteger declined = new AtomicInteger();
        IntStream.range(0, 10).parallel()
                .forEach(i -> {
                    try {
                        service.verifyLimits(TEST_NCA_ID, TEST_FUNCTION_ID);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                    try {
                        service.verifyLimits(TEST_NCA_ID_2, TEST_FUNCTION_ID_2);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                });
        assertThat(successful.get()).isEqualTo(expectedSuccess);
        assertThat(declined.get()).isEqualTo(expectedDecline);
    }

    private static Stream<Arguments> getVersionRateLimiterUseCases() {
        return Stream.of(
                // all default
                Arguments.of(createAccountOverrides(BUCKET, "*", 10),
                             createFunctionOverrides(BUCKET, VERSION_WILDCARD, VERSION_WILDCARD, 6),
                             Collections.emptyMap(),
                             Collections.emptyMap(),
                             TEST_VERSION_ID_1,
                             TEST_VERSION_ID_2,
                        /* allowed 6 calls for id_1 + 6 calls for id_2 */ 12,
                        /* declined 4 calls for id_1 + 4 calls for id_2 */ 8),

                // one version is overridden, the other is default
                Arguments.of(createAccountOverrides(BUCKET, "*", 10),
                             createFunctionOverrides(BUCKET, VERSION_WILDCARD, VERSION_WILDCARD, 6),
                             Collections.emptyMap(),
                             Map.of(TEST_FUNCTION_ID,
                                    createFunctionOverrides(BUCKET, TEST_FUNCTION_ID,
                                                            TEST_VERSION_ID_1,
                                                            4L)),
                             TEST_VERSION_ID_1,
                             TEST_VERSION_ID_2,
                        /* allowed 4 calls for id_1 + 6 calls for id_2 */ 10,
                        /* declined 6 calls for id_1 + 4 calls for id_2 */ 10),
                // version id 2 is null => fallback to function level
                Arguments.of(createAccountOverrides(BUCKET, "*", 10),
                             createFunctionOverrides(BUCKET, VERSION_WILDCARD, VERSION_WILDCARD, 8),
                             Collections.emptyMap(),
                             Map.of(TEST_FUNCTION_ID,
                                    createFunctionOverrides(BUCKET, TEST_FUNCTION_ID,
                                                            TEST_VERSION_ID_1,
                                                            6L),
                                    TEST_FUNCTION_ID_2,
                                    createFunctionOverrides(BUCKET, TEST_FUNCTION_ID_2,
                                                            TEST_VERSION_ID_2, 4L)),
                             TEST_VERSION_ID_1,
                             null,
                        /* allowed 6 calls for id_1 + 4 calls for function id_2 */ 10,
                        /* declined 4 calls for id_1 + 6 calls for id_2 */ 10),
                // version id 2 is null => fallback to default level
                Arguments.of(createAccountOverrides(BUCKET, "*", 10),
                             createFunctionOverrides(BUCKET, VERSION_WILDCARD, VERSION_WILDCARD, 8),
                             Collections.emptyMap(),
                             Map.of(TEST_FUNCTION_ID,
                                    createFunctionOverrides(BUCKET, TEST_FUNCTION_ID,
                                                            TEST_VERSION_ID_1,
                                                            6L)),
                             TEST_VERSION_ID_1,
                             null,
                        /* allowed 6 calls for id_1 + 8 calls for function id_2 */ 14,
                        /* declined 4 calls for id_1 + 2 calls for id_2 */ 6));
    }

    @ParameterizedTest
    @MethodSource("getVersionRateLimiterUseCases")
    void verifyVersionCall(
            AccountRateLimiterProperties.AccountRateCappingProperties defaultAccountProperties,
            FunctionRateLimiterProperties.FunctionCappingProperties defaultFunctionProperties,
            Map<String, AccountRateLimiterProperties.AccountRateCappingProperties> accountOverrideIds,
            Map<UUID, FunctionRateLimiterProperties.FunctionCappingProperties> functionOverrideIds,
            UUID versionId1,
            UUID versionId2,
            int expectedSuccess,
            int expectedDecline) {
        applyMocks(defaultAccountProperties, defaultFunctionProperties, accountOverrideIds,
                   functionOverrideIds);

        AtomicInteger successful = new AtomicInteger();
        AtomicInteger declined = new AtomicInteger();
        IntStream.range(0, 10).parallel()
                .forEach(i -> {
                    try {
                        service.verifyLimits(TEST_NCA_ID, TEST_FUNCTION_ID, versionId1);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                    try {
                        service.verifyLimits(TEST_NCA_ID_2, TEST_FUNCTION_ID_2, versionId2);
                        successful.incrementAndGet();
                    } catch (TooManyRequestsException e) {
                        declined.incrementAndGet();
                    }
                });
        assertThat(successful.get()).isEqualTo(expectedSuccess);
        assertThat(declined.get()).isEqualTo(expectedDecline);
    }

    @Test
    void verifyAccountCallWithFulfilment() {
        applyMocks(
                createAccountOverrides(BUCKET, "*", 6),
                createFunctionOverrides(BUCKET, VERSION_WILDCARD, VERSION_WILDCARD, 6),
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

    private static FunctionRateLimiterProperties.FunctionCappingProperties createFunctionOverrides(
            RateLimiterPolicy policy,
            UUID functionId,
            UUID versionId,
            long allowedInvocationsPerSecond
    ) {
        return FunctionRateLimiterProperties.FunctionCappingProperties
                .builder()
                .policy(policy)
                .functionId(functionId)
                .functionVersionId(versionId)
                .allowedInvocationsPerSecond(allowedInvocationsPerSecond)
                .build();
    }

    private static AccountRateLimiterProperties.AccountRateCappingProperties createAccountOverrides(
            RateLimiterPolicy policy,
            String ncaId,
            long allowedInvocationsPerSecond
    ) {
        return AccountRateLimiterProperties.AccountRateCappingProperties
                .builder()
                .policy(policy)
                .ncaId(ncaId)
                .allowedInvocationsPerSecond(allowedInvocationsPerSecond)
                .build();
    }

    private void applyMocks(
            AccountRateLimiterProperties.AccountRateCappingProperties defaultAccountProperties,
            FunctionRateLimiterProperties.FunctionCappingProperties defaultFunctionProperties,
            Map<String, AccountRateLimiterProperties.AccountRateCappingProperties> accountOverrideIds,
            Map<UUID, FunctionRateLimiterProperties.FunctionCappingProperties> functionOverrideIds) {
        lenient().when(accountProperties.getDefaultRateCappingProperties())
                .thenReturn(defaultAccountProperties);
        lenient().when(functionProperties.getDefaultRateCappingProperties())
                .thenReturn(defaultFunctionProperties);
        lenient().when(accountProperties.getOverridesMap()).thenReturn(accountOverrideIds);
        lenient().when(functionProperties.getFunctionOverridesMap())
                .thenReturn(functionOverrideIds);
        lenient().when(functionProperties.getVersionOverridesMap()).thenReturn(functionOverrideIds);
    }
}
