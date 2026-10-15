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
package com.nvidia.nvcf.configuration.filters;

import static com.nvidia.nvcf.util.MockApiKeysServer.setApiKeyValidationResponse;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_QUEUE_DETAILS;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import jakarta.annotation.Nullable;
import java.util.List;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ExceptionHandlerFilterTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestCommonService testCommonService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockApiKeysServer.start(apiKeysBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockApiKeysServer.stop();
        MockEssServer.stop();

        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockApiKeysServer.resetToDefault();
    }

    Stream<Arguments> provideAuthTokens() {
        return Stream.of(
                // apikey auth with a valid token but breaking policy
                Arguments.of((Supplier<String>) () -> {
                            setApiKeyValidationResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                    List.of(new Resource("account-functions", "*")),
                                    List.of(ADMIN_SCOPE_QUEUE_DETAILS), false);
                            return "nvapi-stg-valid";
                        }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                        HttpStatus.FORBIDDEN),
                // apikey auth with an invalid token
                Arguments.of("invalid", TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                        HttpStatus.UNAUTHORIZED)
        );
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokens")
    void shouldGetQueueDepthUsingFunctionVersionId(
            @Nullable Object tokenSupplier,
            @Nullable UUID functionId,
            @Nullable UUID functionVersionId,
            HttpStatus expectedStatus) {
        String token = getToken(tokenSupplier);

        var url = "/v2/nvcf/accounts/" + TEST_NCA_ID + "/queues/functions";
        if (functionId != null) {
            url += "/" + functionId;
        }
        if (functionVersionId != null) {
            url += "/versions/" + functionVersionId;
        }

        var requestEntity = RequestEntity.get(url)
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
    }
}
