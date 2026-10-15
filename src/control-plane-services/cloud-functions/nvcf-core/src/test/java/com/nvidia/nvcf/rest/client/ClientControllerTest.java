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
package com.nvidia.nvcf.rest.client;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_REGISTER_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_ACCOUNT_SETUP;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.NOT_FOUND;
import static org.springframework.http.HttpStatus.OK;
import static org.springframework.http.HttpStatus.UNAUTHORIZED;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.client.dto.ClientDetailsResponse;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import java.net.URI;
import java.util.List;
import java.util.function.Supplier;
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
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ClientControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

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
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();

        MockApiKeysServer.stop();
        MockEssServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void beforeEach() {
        testAccountService.createDefaultAccountsClientsAndRegistries();
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockApiKeysServer.resetToDefault();
    }

    Stream<Arguments> getClientDetailsArgs() {
        return Stream.of(
                // ========== Positive Cases ==========
                // JWT with correct scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(SCOPE_ACCOUNT_SETUP), 100),
                        TEST_CLIENT_ID,
                        TEST_NCA_ID,
                        OK),
                // apikey with correct scope
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(),
                                              List.of(SCOPE_ACCOUNT_SETUP));
                    return "nvapi-stg-some-key";
                }, TEST_CLIENT_ID, TEST_NCA_ID, OK),
                // JWT for second client with correct scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(SCOPE_ACCOUNT_SETUP), 100),
                        TEST_CLIENT_ID_2,
                        TEST_NCA_ID_2,
                        OK),
                // apikey for second client with correct scope
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID_2, TEST_OWNER_ID, List.of(),
                                              List.of(SCOPE_ACCOUNT_SETUP));
                    return "nvapi-stg-some-key-2";
                }, TEST_CLIENT_ID_2, TEST_NCA_ID_2, OK),

                // ========== Negative Cases ==========
                // No auth token
                Arguments.of(null, TEST_CLIENT_ID, TEST_NCA_ID, UNAUTHORIZED),
                // JWT with no scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT, List.of(), 100),
                        TEST_CLIENT_ID,
                        TEST_NCA_ID,
                        FORBIDDEN),
                // apikey with no scope
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(), List.of());
                    return "nvapi-stg-some-key";
                }, TEST_CLIENT_ID, TEST_NCA_ID, FORBIDDEN),
                // JWT with wrong scope
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(SCOPE_REGISTER_FUNCTION), 100),
                        TEST_CLIENT_ID,
                        TEST_NCA_ID,
                        FORBIDDEN),
                // apikey with wrong scope
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(),
                                              List.of(SCOPE_INVOKE_FUNCTION));
                    return "nvapi-stg-wrong-scope";
                }, TEST_CLIENT_ID, TEST_NCA_ID, FORBIDDEN),
                // Invalid client ID with JWT
                Arguments.of(
                        MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                        List.of(SCOPE_ACCOUNT_SETUP), 100),
                        "INVALID_CLIENT_ID",
                        TEST_NCA_ID,
                        NOT_FOUND),
                // Invalid client ID with apikey
                Arguments.of((Supplier<String>) () -> {
                    MockApiKeysServer.setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(),
                                              List.of(SCOPE_ACCOUNT_SETUP));
                    return "nvapi-stg-invalid-client";
                }, "INVALID_CLIENT_ID", TEST_NCA_ID, NOT_FOUND)
        );
    }

    @ParameterizedTest
    @MethodSource("getClientDetailsArgs")
    void shouldGetClientDetails(Object tokenSupplier,
                                String clientId,
                                String ncaId,
                                HttpStatus expectedStatus) {
        var token = getToken(tokenSupplier);
        var builder = RequestEntity.get(URI.create("/v2/nvcf/clients/" + clientId));
        if (token != null) {
            builder = builder.header("Authorization", "Bearer " + token);
        }
        var requestEntity = builder.build();
        var responseEntity =
                testRestTemplate.exchange(requestEntity, ClientDetailsResponse.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        
        // Only check response body for successful cases
        if (expectedStatus.isError()) {
            return;
        }

        var responseBody = responseEntity.getBody();
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.client()).isNotNull();
        assertThat(responseBody.client().clientId()).isEqualTo(clientId);
        assertThat(responseBody.client().ncaId()).isEqualTo(ncaId);
        assertThat(responseBody.client().name()).isNotBlank();
    }

}
