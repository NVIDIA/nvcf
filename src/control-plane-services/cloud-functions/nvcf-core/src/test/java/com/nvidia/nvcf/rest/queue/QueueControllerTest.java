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
import static com.nvidia.nvcf.rest.queue.TestQueueService.TEST_VERSION_ID_DIFF_ACCOUNT;
import static com.nvidia.nvcf.util.MockApiKeysServer.setResponse;
import static com.nvidia.nvcf.util.TestConstants.FAKE_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_INVOKE_FUNCTION;
import static com.nvidia.nvcf.util.TestConstants.SCOPE_QUEUE_DETAILS;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_OWNER_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestUtil.getToken;
import static org.assertj.core.api.Assertions.assertThat;

import com.github.tomakehurst.wiremock.matching.RequestPatternBuilder;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.LocalGrpcPort;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.function.invocation.TestWorker;
import com.nvidia.nvcf.rest.queue.dto.GetPositionInQueueResponse;
import com.nvidia.nvcf.rest.queue.dto.GetQueuesResponse;
import com.nvidia.nvcf.service.apikeys.ApiKeyValidationResult.Resource;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.service.token.GrpcTokenService.NvcfIssuedToken.TokenType;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import io.nats.client.Connection;
import jakarta.annotation.Nullable;
import java.io.IOException;
import java.net.URI;
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
class QueueControllerTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @Autowired
    private JsonMapper jsonMapper;

    @Autowired
    private TestDeploymentService testDeploymentService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private TestQueueService testQueueService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private GrpcTokenService grpcTokenService;

    @LocalGrpcPort
    private int grpcServerPort;

    @Autowired
    private Connection natsConnection;

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

    @Test
    void shouldCallApiKeysServerOnce() {
        setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                    List.of(new Resource("function", TEST_FUNCTION_ID + "/*")),
                    List.of(SCOPE_QUEUE_DETAILS));
        // Create a function with DEPLOYING status.
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);

        // Create entity in functions_deployment_v2 table for the function.
        testDeploymentService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                     TEST_DEPLOYMENT_ID, TEST_NCA_ID);

        var function =
                functionLookupService
                        .lookupUsingFunctionIdAndVersionIdOrThrow(TEST_FUNCTION_ID,
                                                                  TEST_VERSION_ID_1);
        assertThat(function).isNotNull();

        // Make the function active so that it can be invoked to add messages to its
        // queue.
        function.setFunctionStatus(FunctionStatus.ACTIVE);
        functionsRepository.save(function);
        var apiKeysStartCount = MockApiKeysServer.getMockApiKeysServer()
                .countRequestsMatching(RequestPatternBuilder.allRequests().build())
                .getCount();
        for (int i = 0; i < 20; i++) {
            var requestEntity = RequestEntity.get(URI.create("/v2/nvcf/queues/functions/"
                                                                     + TEST_FUNCTION_ID))
                    .header("Authorization", "Bearer nvapi-stg-some-key")
                    .build();
            var responseEntity = testRestTemplate.exchange(requestEntity, GetQueuesResponse.class);
            assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        }
        MockApiKeysServer.getMockApiKeysServer()
                .verify(apiKeysStartCount + 1, RequestPatternBuilder.allRequests());
    }

    Stream<Arguments> provideAuthTokensV2() {
        return Stream.of(
                // JWT auth with a known function and known version
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.OK),
                // JWT auth with a known function with no version id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_FUNCTION_ID, null, HttpStatus.OK),
                // Queue depth for public function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_PUBLIC_FUNCTION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1, HttpStatus.BAD_REQUEST),
                // JWT auth with no function id
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             null, TEST_VERSION_ID_1, HttpStatus.NOT_FOUND),
                // JWT auth with a known function and no scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                // JWT auth with a function defined in TEST_NCA_ID_2 account and TEST_NCA_ID
                // as an authorized party.
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_FUNCTION_ID_2, TEST_VERSION_ID_DIFF_ACCOUNT,
                             HttpStatus.OK),
                // JWT auth with someone else's function and no ID
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             null, TEST_VERSION_ID_1, HttpStatus.NOT_FOUND),
                // JWT auth with a fake function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             FAKE_FUNCTION_ID, UUID.randomUUID(), HttpStatus.NOT_FOUND),
                // apikey auth with a known function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             HttpStatus.OK),
                // apikey auth with a public function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             },
                             TEST_PUBLIC_FUNCTION_ID_1, TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                             HttpStatus.BAD_REQUEST),
                // apikey auth with a known function but no scope
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of());
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                // apikey auth with a known function but no resource permission
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID, List.of(),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                // apikey auth with a known function and version specific auth
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_1),
                                                     new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_2)),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             HttpStatus.OK),
                // apikey auth with a known function and version specific auth
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_1),
                                                     new Resource("function",
                                                                  TEST_FUNCTION_ID + "/" + TEST_VERSION_ID_2)),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             HttpStatus.OK),
                // apikey auth with a known function and incorrect version specific auth
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + UUID.randomUUID())),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             HttpStatus.FORBIDDEN),
                // apikey auth with a known function and no ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, null, TEST_VERSION_ID_1,
                             HttpStatus.NOT_FOUND),
                // apikey auth with someone else's function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID_2, TEST_VERSION_ID_DIFF_ACCOUNT,
                             HttpStatus.FORBIDDEN),
                // apikey auth with a function defined in TEST_NCA_ID_2 account and TEST_NCA_ID
                // as an authorized party.
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID_2 + "/*")),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID_2, TEST_VERSION_ID_DIFF_ACCOUNT,
                             HttpStatus.OK),
                // apikey auth with someone else's function and no ID
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, null, TEST_VERSION_ID_DIFF_ACCOUNT,
                             HttpStatus.NOT_FOUND),
                // apikey auth with a fake function
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/*")),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, FAKE_FUNCTION_ID, UUID.randomUUID(),
                             HttpStatus.NOT_FOUND),
                // missing auth for a known function
                Arguments.of(null, TEST_FUNCTION_ID, TEST_VERSION_ID_2, HttpStatus.UNAUTHORIZED),
                // missing auth for a known function and no ID
                Arguments.of(null, null, TEST_VERSION_ID_2, HttpStatus.UNAUTHORIZED),
                // missing auth for a fake function
                Arguments.of(null, FAKE_FUNCTION_ID, UUID.randomUUID(), HttpStatus.UNAUTHORIZED)
        );
    }

    Stream<Arguments> provideAuthTokensV2ForPositionInQueue() {
        return Stream.of(
                // JWT auth with a known function and known version
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.OK),
                // JWT auth with a known function and known version2
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_2, HttpStatus.OK),
                // Position in queue for public function
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(SCOPE_QUEUE_DETAILS),
                                                             100),
                             TEST_PUBLIC_FUNCTION_ID_1,
                             TEST_PUBLIC_FUNCTION_VERSION_ID_1, HttpStatus.OK),
                // JWT auth with a known function and no scopes
                Arguments.of(MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                             List.of(),
                                                             100),
                             TEST_FUNCTION_ID, TEST_VERSION_ID_1, HttpStatus.FORBIDDEN),
                // apikey auth with a known function and version specific auth
                Arguments.of((Supplier<String>) () -> {
                                 setResponse(TEST_NCA_ID, TEST_OWNER_ID,
                                             List.of(new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_1),
                                                     new Resource("function",
                                                                  TEST_FUNCTION_ID + "/"
                                                                          + TEST_VERSION_ID_2)),
                                             List.of(SCOPE_QUEUE_DETAILS));
                                 return "nvapi-stg-some-key";
                             }, TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                             HttpStatus.OK)
        );
    }

    @ParameterizedTest
    @MethodSource("provideAuthTokensV2")
    void shouldGetQueueDepthUsingFunctionVersionId(
            @Nullable Object tokenSupplier,
            @Nullable UUID functionId,
            @Nullable UUID functionVersionId,
            HttpStatus expectedStatus)
            throws IOException {
        String token = getToken(tokenSupplier);
        final int numInvocations = 4;
        testQueueService.initializeResourcesAndState();
        testQueueService.invokeFunctionsToBuildQueueDepth(numInvocations, functionId);

        var url = "/v2/nvcf/queues/functions";
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
            UUID functionId, UUID functionVersionId,
            UUID reqId, String token, int expectedPosition,
            HttpStatus expectedStatus) {
        var url = "/v2/nvcf/queues/" + reqId + "/position";
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
    @MethodSource("provideAuthTokensV2ForPositionInQueue")
    void getPositionInQueue(
            @Nullable Object tokenSupplier,
            @Nullable UUID functionId,
            @Nullable UUID functionVersionId,
            HttpStatus expectedStatus
    ) {

        testQueueService.setupForPositionInQueue(functionId, functionVersionId);
        final int numInvocations = 5;
        List<UUID> requestIds = new ArrayList<>();
        var invokeToken = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                          List.of(SCOPE_INVOKE_FUNCTION),
                                                          100);
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
        testGetPositionInQueue(functionId, functionVersionId, requestIds.getFirst(),
                               token, 0, expectedStatus);
        // Remaining all are in Pending state and will be at their (actual position in stream - 1)
        // as first request is in Xpending, so it will be reduced from the actual position
        testGetPositionInQueue(functionId, functionVersionId, requestIds.get(1),
                               token, 1, expectedStatus);
        testGetPositionInQueue(functionId, functionVersionId, requestIds.get(2),
                               token, 2, expectedStatus);
        testGetPositionInQueue(functionId, functionVersionId, requestIds.get(3),
                               token, 3, expectedStatus);
        testGetPositionInQueue(functionId, functionVersionId, requestIds.get(4),
                               token, 4, expectedStatus);
    }

    @Test
    void getPositionInQueueInvalidRequestId() {
        String token = MOCK_OAUTH2_TOKEN_SERVER.getJwt(TEST_CLIENT_SUBJECT,
                                                       List.of(SCOPE_QUEUE_DETAILS),
                                                       100);

        // Assert invalid UUID case, there won't be any position only NOT_FOUND error
        testGetPositionInQueue(TEST_FUNCTION_ID, TEST_VERSION_ID_1, UUID.randomUUID(),
                               token, -10, HttpStatus.NOT_FOUND);
    }
}
