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
package com.nvidia.nvcf.rest.queue;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.ACTIVE;
import static com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum.DEPLOYING;
import static com.nvidia.nvcf.rest.queue.TestQueueService.TEST_VERSION_ID_DIFF_ACCOUNT;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.NvcfConstants.ADMIN_SCOPE_QUEUE_DETAILS;
import static com.nvidia.nvcf.util.TestConstants.FAKE_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_ADMIN_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NAME_V1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.LocalGrpcPort;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.invocation.TestWorker;
import com.nvidia.nvcf.rest.queue.dto.GetPositionInQueueResponse;
import com.nvidia.nvcf.rest.queue.dto.GetQueuesResponse;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import io.nats.client.Connection;
import jakarta.annotation.Nullable;
import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
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
import tools.jackson.databind.json.JsonMapper;

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class XAccountQueueControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestQueueService testQueueService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private GrpcTokenService grpcTokenService;

    @Autowired
    private Connection natsConnection;

    @LocalGrpcPort
    private int grpcServerPort;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

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

        testAccountService.createDefaultAccountsClientsAndRegistries();
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

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockApiKeysServer.resetToDefault();
        testQueueService.clearQueues();
    }

    Stream<Arguments> provideAuthTokens() {
        return Stream.of(
                // JWT auth with a known function and known version
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.OK),
                // JWT auth with a known function with no version id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             null,
                             HttpStatus.OK),
                // Queue depth for public function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_PUBLIC_FUNCTION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             HttpStatus.OK),
                // bad nca id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             "badNcaId",
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                // JWT auth with a no function id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             null,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                // JWT auth with a known function and no scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                // JWT auth with a function defined in TEST_NCA_ID_2 account and TEST_NCA_ID
                // as an authorized party.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID_2,
                             TEST_VERSION_ID_DIFF_ACCOUNT,
                             HttpStatus.OK),
                // JWT auth with someone else's function and no ID
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             null,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                // JWT auth with a fake function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             FAKE_FUNCTION_ID,
                             UUID.randomUUID(),
                             HttpStatus.NOT_FOUND),
                // api-key auth with a known function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                Arguments.of(null,
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             HttpStatus.UNAUTHORIZED),
                // missing auth for a known function and no ID
                Arguments.of(null,
                             TEST_NCA_ID,
                             null,
                             TEST_VERSION_ID_2,
                             HttpStatus.UNAUTHORIZED),
                // missing auth for a fake function
                Arguments.of(null,
                             TEST_NCA_ID,
                             FAKE_FUNCTION_ID,
                             UUID.randomUUID(),
                             HttpStatus.UNAUTHORIZED)
        );
    }

    Stream<Arguments> provideAuthTokensForPositionInQueue() {
        return Stream.of(
                // JWT auth with a known function and known version
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.OK),
                // JWT auth with a known function with known version2
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_2,
                             HttpStatus.OK),
                // JWT auth for public function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_NCA_ID,
                             TEST_PUBLIC_FUNCTION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             HttpStatus.OK),
                // bad nca id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                             100),
                             "badNcaId",
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                // JWT auth with a known function and no scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                // api-key auth with a known function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(ADMIN_SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_NCA_ID,
                             TEST_FUNCTION_ID,
                             TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN)
        );
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokens")
    void shouldGetQueueDepthUsingFunctionId(
            @Nullable Object tokenSupplier,
            String ncaId,
            @Nullable UUID functionId,
            @Nullable UUID functionVersionId,
            HttpStatus expectedStatus)
            throws IOException {
        String token = getToken(tokenSupplier);
        final int numInvocations = 4;
        testQueueService.initializeResourcesAndState();
        testQueueService.invokeFunctionsToBuildQueueDepth(numInvocations, functionId);

        var url = "/v2/nvcf/accounts/" + ncaId + "/queues/functions/";
        if (functionId != null) {
            url += "/" + functionId;
        }
        var requestEntity = RequestEntity.get(url)
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        var responseBody = jsonMapper.readValue(responseEntity.getBody(),
                                                GetQueuesResponse.class);

        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functionId()).isEqualTo(functionId);
        assertThat(responseBody.queues()).isNotEmpty();
        // lazy special case for the cross account function
        if (TEST_FUNCTION_ID_2.equals(functionId)) {
            assertThat(responseBody.queues()).hasSize(1);
            assertThat(responseBody.queues().getFirst().functionVersionId())
                    .isEqualTo(TEST_VERSION_ID_DIFF_ACCOUNT);
            assertThat(responseBody.queues().getFirst().functionStatus())
                    .isEqualTo(ACTIVE);
            assertThat(responseBody.queues().getFirst().functionName())
                    .isEqualTo(TEST_FUNCTION_NAME_2);
            assertThat(responseBody.queues().getFirst().queueDepth()).isEqualTo(4);
            return;
        }

        if (TEST_PUBLIC_FUNCTION_ID_1.equals(functionId)) {
            assertThat(responseBody.queues()).hasSize(1);
            assertThat(responseBody.queues().getFirst().functionVersionId())
                    .isEqualTo(TEST_PUBLIC_FUNCTION_VERSION_ID_1);
            assertThat(responseBody.queues().getFirst().functionStatus())
                    .isEqualTo(ACTIVE);
            assertThat(responseBody.queues().getFirst().functionName())
                    .isEqualTo(TEST_PUBLIC_FUNCTION_NAME_V1);
            assertThat(responseBody.queues().getFirst().queueDepth()).isEqualTo(4);
            return;
        }

        assertThat(responseBody.queues()).hasSize(2);
        assertThat(responseBody.queues().getFirst().functionVersionId())
                .isIn(TEST_VERSION_ID_1, TEST_VERSION_ID_2);
        assertThat(responseBody.queues().getFirst().functionStatus())
                .isIn(ACTIVE, DEPLOYING);
        assertThat(responseBody.queues().getFirst().functionName())
                .isIn(TEST_FUNCTION_NAME, TEST_FUNCTION_NAME_2);
        assertThat(responseBody.queues().getFirst().queueDepth()).isEqualTo(4);
        assertThat(responseBody.queues().get(1).functionVersionId())
                .isIn(TEST_VERSION_ID_1, TEST_VERSION_ID_2);
        assertThat(responseBody.queues().get(1).functionStatus())
                .isIn(ACTIVE, DEPLOYING);
        assertThat(responseBody.queues().get(1).functionName())
                .isIn(TEST_FUNCTION_NAME, TEST_FUNCTION_NAME_2);
        assertThat(responseBody.queues().get(1).queueDepth()).isEqualTo(4);
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokens")
    void shouldGetQueueDepthUsingFunctionVersionId(
            @Nullable Object tokenSupplier,
            String ncaId,
            @Nullable UUID functionId,
            @Nullable UUID functionVersionId,
            HttpStatus expectedStatus)
            throws IOException {
        String token = getToken(tokenSupplier);
        final int numInvocations = 4;
        testQueueService.initializeResourcesAndState();
        testQueueService.invokeFunctionsToBuildQueueDepth(numInvocations, functionId);

        var url = "/v2/nvcf/accounts/" + ncaId + "/queues/functions";
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
        if (expectedStatus.isError()) {
            return;
        }
        var responseBody = jsonMapper.readValue(responseEntity.getBody(),
                                                GetQueuesResponse.class);

        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functionId()).isEqualTo(functionId);
        assertThat(responseBody.queues()).isNotEmpty();

        // lazy special case for the cross account function
        if (TEST_FUNCTION_ID_2.equals(functionId)) {
            assertThat(responseBody.queues()).hasSize(1);
            assertThat(responseBody.queues().getFirst().functionVersionId())
                    .isEqualTo(TEST_VERSION_ID_DIFF_ACCOUNT);
            assertThat(responseBody.queues().getFirst().functionStatus())
                    .isEqualTo(ACTIVE);
            assertThat(responseBody.queues().getFirst().functionName())
                    .isEqualTo(TEST_FUNCTION_NAME_2);
            assertThat(responseBody.queues().getFirst().queueDepth()).isEqualTo(4);
            return;
        }

        if (TEST_PUBLIC_FUNCTION_ID_1.equals(functionId)) {
            assertThat(responseBody.queues()).hasSize(1);
            assertThat(responseBody.queues().getFirst().functionVersionId())
                    .isEqualTo(TEST_PUBLIC_FUNCTION_VERSION_ID_1);
            assertThat(responseBody.queues().getFirst().functionStatus())
                    .isEqualTo(ACTIVE);
            assertThat(responseBody.queues().getFirst().functionName())
                    .isEqualTo(TEST_PUBLIC_FUNCTION_NAME_V1);
            assertThat(responseBody.queues().getFirst().queueDepth()).isEqualTo(4);
            return;
        }

        if (functionVersionId != null) {
            assertThat(responseBody.queues()).hasSize(1);
            assertThat(responseBody.queues().getFirst().functionVersionId()).isEqualTo(
                    functionVersionId);
            assertThat(responseBody.queues().getFirst().functionStatus()).isEqualTo(ACTIVE);
            assertThat(responseBody.queues().getFirst().functionName()).isEqualTo(
                    TEST_FUNCTION_NAME);
            assertThat(responseBody.queues().getFirst().queueDepth()).isEqualTo(4);
        } else {
            assertThat(responseBody.queues()).hasSize(2);
            assertThat(responseBody.queues().getFirst().queueDepth()).isEqualTo(4);
            assertThat(responseBody.queues().get(1).queueDepth()).isEqualTo(4);
        }
    }

    @SneakyThrows
    private void testGetPositionInQueue(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            UUID reqId,
            String token,
            int expectedPosition,
            HttpStatus expectedStatus) {
        var url = "/v2/nvcf/accounts/" + ncaId + "/queues/" + reqId + "/position";
        var requestEntity = RequestEntity.get(url)
                .header("Authorization", "Bearer " + token)
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);

        assertThat(responseEntity.getStatusCode()).isEqualTo(expectedStatus);
        if (expectedStatus.isError()) {
            return;
        }
        var responseBody = jsonMapper.readValue(responseEntity.getBody(),
                                                GetPositionInQueueResponse.class);
        assertThat(responseBody).isNotNull();
        assertThat(responseBody.functionId()).isEqualTo(functionId);
        assertThat(responseBody.functionVersionId()).isEqualTo(functionVersionId);
        assertThat(responseBody.positionInQueue()).isEqualTo(expectedPosition);
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokensForPositionInQueue")
    void getPositionInQueueForAdmin(
            @Nullable Object tokenSupplier,
            String ncaId,
            @Nullable UUID functionId,
            @Nullable UUID functionVersionId,
            HttpStatus expectedStatus
    ) {
        testQueueService.setupForPositionInQueue(functionId, functionVersionId);
        final int numInvocations = 4;
        List<UUID> requestIds = new ArrayList<>();
        for (int i = 0; i < numInvocations; i++) {
            var reqId = testQueueService.addInvocationToQueue(functionId, functionVersionId);

            requestIds.add(reqId);
        }
        // Consume the first message from this stream without acknowledgment
        var testWorker = new TestWorker(functionId, functionVersionId, grpcServerPort,
                                        () -> grpcTokenService.issueToken(functionId,
                                                                          functionVersionId,
                                                                          TokenType.WORKER),
                                        natsConnection,
                                        (worker, message) -> {
                                        });
        testWorker.getRunningWorkerTask().join();

        String token = getToken(tokenSupplier);
        // Assert for each request ID, Total request in stream = 4, Consumer took 1 request
        // First request is pending in consumer group, so it is in progress and position will be 0
        testGetPositionInQueue(ncaId, functionId, functionVersionId, requestIds.getFirst(),
                               token, 0, expectedStatus);
        // Remaining all are in Pending state and will be at their (actual position in stream - 1)
        // as first request is in Xpending, so it will be reduced from the actual position
        testGetPositionInQueue(ncaId, functionId, functionVersionId, requestIds.get(1),
                               token, 1, expectedStatus);
        testGetPositionInQueue(ncaId, functionId, functionVersionId, requestIds.get(2),
                               token, 2, expectedStatus);
        testGetPositionInQueue(ncaId, functionId, functionVersionId, requestIds.get(3),
                               token, 3, expectedStatus);
    }

    @Test
    void getPositionInQueueForAdminInvalidRequestId() {
        String token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_ADMIN_SUBJECT,
                                                       List.of(ADMIN_SCOPE_QUEUE_DETAILS),
                                                       100);

        // Assert invalid UUID case, there won't be any position only NOT_FOUND error
        testGetPositionInQueue(TEST_NCA_ID, TEST_FUNCTION_ID, TEST_VERSION_ID_1, UUID.randomUUID(),
                               token, -10, HttpStatus.NOT_FOUND);
    }
}
