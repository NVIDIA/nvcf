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
package com.nvidia.nvct.grpc;

import static com.nvidia.nvct.proto.ArtifactsResponse.ArtifactResponse.ArtifactKindEnum.MODEL;
import static com.nvidia.nvct.proto.ArtifactsResponse.ArtifactResponse.ArtifactKindEnum.RESOURCE;
import static com.nvidia.nvct.proto.ExecutionStatus.COMPLETED;
import static com.nvidia.nvct.proto.ExecutionStatus.ERRORED;
import static com.nvidia.nvct.proto.ExecutionStatus.EXCEEDED_MAX_RUNTIME_DURATION;
import static com.nvidia.nvct.proto.ExecutionStatus.IN_PROGRESS;
import static com.nvidia.nvct.proto.ExecutionStatus.LAUNCHED;
import static com.nvidia.nvct.proto.ExecutionStatus.PENDING_EVALUATION;
import static com.nvidia.nvct.proto.ExecutionStatus.TASK_CONTAINER_INITIALIZING;
import static com.nvidia.nvct.proto.ExecutionStatus.WORKER_TERMINATED;
import static com.nvidia.nvct.util.ProtoMappingUtils.fromTimestamp;
import static com.nvidia.nvct.util.TestConstants.L40G_INSTANCE_TYPE;
import static com.nvidia.nvct.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvct.util.TestConstants.TEST_MODELS;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_ICMS_REQ_ID_3;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_3;
import static org.assertj.core.api.Assertions.assertThat;

import tools.jackson.databind.json.JsonMapper;
import com.google.protobuf.ByteString;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.PlainJWT;
import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.proto.ArtifactsRequest;
import com.nvidia.nvct.proto.ArtifactsResponse;
import com.nvidia.nvct.proto.ConnectRequest;
import com.nvidia.nvct.proto.ErrorDetails;
import com.nvidia.nvct.proto.HeartbeatRequest;
import com.nvidia.nvct.proto.HeartbeatResponse;
import com.nvidia.nvct.proto.RefreshTokenRequest;
import com.nvidia.nvct.proto.ResultMetadata;
import com.nvidia.nvct.proto.ResultMetadataRequest;
import com.nvidia.nvct.proto.SecretCredentialsRequest;
import com.nvidia.nvct.proto.WorkerGrpc;
import com.nvidia.nvct.rest.result.dto.ResultDto;
import com.nvidia.nvct.service.result.ResultService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.service.token.TokenService;
import com.nvidia.nvct.util.MockEssServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import com.nvidia.nvct.util.MockIcmsServer;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.Status;
import io.grpc.stub.MetadataUtils;
import io.grpc.stub.StreamObserver;
import java.net.URL;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Assertions;
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
@SpringBootTest(classes = {NvctTestApp.class,
        IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=test",
                "grpc.server.port=9090"
        })
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class GrpcWorkerServiceTest {

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private TaskService taskService;

    @Autowired
    private TokenService tokenService;

    @Autowired
    private ResultService resultService;

    @Autowired
    private JsonMapper jsonMapper;

    @Value("${nvct.registries.recognized.model.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvct.registries.recognized.model.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvct.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${spring.security.oauth2.client.registration.notary.client-id}")
    private String notaryClientId;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Value("${nvct.ess.base-url}")
    private String essBaseUrl;

    private ManagedChannel channel;
    private WorkerGrpc.WorkerBlockingStub workerBlockingStub;
    private WorkerGrpc.WorkerStub workerStub;

    @SneakyThrows
    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNvcfServer.start(nvcfBaseUrl);
        MockEssServer.start(essBaseUrl);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
    }

    @AfterAll
    void cleanup() {
        MockCasServer.stop();
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        MockEssServer.stop();
        MockNotaryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        log.info("{} reset", this.getClass().getSimpleName());
        channel.shutdown();
        testTaskService.clearAll();
        MockEssServer.clearSecrets();
    }

    @BeforeEach
    void setup() {
        log.info("{} setup", this.getClass().getSimpleName());
        testTaskService.createTask(TEST_NCA_ID, TEST_TASK_ID_1, TEST_ICMS_REQ_ID_1);
        testTaskService.createTaskWithModel(TEST_NCA_ID, TEST_TASK_ID_2, TEST_ICMS_REQ_ID_2,
                                            TEST_MODELS);
        testTaskService.createTaskWithModelAndResources(TEST_NCA_ID_2, TEST_TASK_ID_3,
                                                        TEST_ICMS_REQ_ID_3);
    }

    private void setupGrpc(String jwtToken) {
        var md = new Metadata();
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwtToken);
        channel = ManagedChannelBuilder
                .forAddress("localhost", 9090)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        workerBlockingStub = WorkerGrpc.newBlockingStub(channel);
        workerStub = WorkerGrpc.newStub(channel);
    }

    private void resetGrpc(String jwtToken) {
        channel.shutdown();
        setupGrpc(jwtToken);
    }

    Stream<Arguments> connectArgs() {
        return Stream.of(
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             Status.Code.OK),
                // task-id mismatch
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_2,
                             Status.Code.PERMISSION_DENIED)
        );
    }

    @ParameterizedTest
    @MethodSource("connectArgs")
    void testConnect(
            String token,
            UUID taskIdConnect,
            Status.Code expectedStatus) {
        setupGrpc(token);
        validateTaskStatus(TEST_NCA_ID, taskIdConnect, TaskStatus.QUEUED);
        var status = Status.Code.OK;
        try {
            connect(taskIdConnect);
            validateTaskStatus(TEST_NCA_ID, taskIdConnect, TaskStatus.LAUNCHED);
            assertThat(testTaskService.getEventCount(taskIdConnect)).isEqualTo(1);
        } catch (Exception e) {
            status = Status.fromThrowable(e).getCode();
        }
        assertThat(status).isEqualTo(expectedStatus);
    }

    @Test
    void testConnectRejectsUnsignedWorkerAssertion() {
        var unsignedToken = new PlainJWT(new JWTClaimsSet.Builder()
                .issuer(notaryBaseUrl)
                .subject(notaryClientId)
                .issueTime(new Date())
                .claim("assertion",
                        Map.of("ncaId", TEST_NCA_ID, "taskId", TEST_TASK_ID_1.toString()))
                .build()).serialize();

        setupGrpc(unsignedToken);
        validateTaskStatus(TEST_NCA_ID, TEST_TASK_ID_1, TaskStatus.QUEUED);

        var status = Status.Code.OK;
        try {
            connect(TEST_TASK_ID_1);
        } catch (Exception e) {
            status = Status.fromThrowable(e).getCode();
        }

        assertThat(status).isEqualTo(Status.Code.PERMISSION_DENIED);
        validateTaskStatus(TEST_NCA_ID, TEST_TASK_ID_1, TaskStatus.QUEUED);
    }

    Stream<Arguments> heartBeatArgs() {
        var requestOk = HeartbeatRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .build();
        var requestErr = HeartbeatRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setErrorMessage("I am in error")
                .setStatus(ERRORED)
                .setInstanceType(L40G_INSTANCE_TYPE)
                .build();
        var requestErrNoInstance = HeartbeatRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setErrorMessage("I am in error")
                .build();
        var requestErrBadInstance =
                HeartbeatRequest.newBuilder()
                        .setTaskId(TEST_TASK_ID_1.toString())
                        .setErrorMessage("I am in error")
                        .setInstanceType("I am a teapot!")
                        .build();
        var requestOkErrrored =
                HeartbeatRequest.newBuilder()
                        .setTaskId(TEST_TASK_ID_1.toString())
                        .setInstanceType(L40G_INSTANCE_TYPE)
                        .setStatus(ERRORED)
                        .setErrorMessage("System error - ESS Agent failed")
                        .build();
        var requestOkExceededMaxDuration =
                HeartbeatRequest.newBuilder()
                        .setTaskId(TEST_TASK_ID_1.toString())
                        .setInstanceType(L40G_INSTANCE_TYPE)
                        .setStatus(EXCEEDED_MAX_RUNTIME_DURATION)
                        .setErrorMessage("System error - Exceeded specified max runtime duration")
                        .build();
        var requestContainerInitializing =
                HeartbeatRequest.newBuilder()
                        .setTaskId(TEST_TASK_ID_1.toString())
                        .setInstanceType(L40G_INSTANCE_TYPE)
                        .setStatus(TASK_CONTAINER_INITIALIZING)
                        .build();
        var workerTerminated =
                HeartbeatRequest.newBuilder()
                        .setTaskId(TEST_TASK_ID_1.toString())
                        .setInstanceType(L40G_INSTANCE_TYPE)
                        .setStatus(WORKER_TERMINATED)
                        .build();

        return Stream.of(
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             TaskStatus.RUNNING,
                             TaskStatus.RUNNING,
                             requestOk,
                             null,
                             Status.Code.OK),
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             TaskStatus.RUNNING,
                             TaskStatus.ERRORED,
                             requestOk,
                             requestOkErrrored,
                             Status.Code.OK),
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             TaskStatus.RUNNING,
                             TaskStatus.EXCEEDED_MAX_RUNTIME_DURATION,
                             requestOk,
                             requestOkExceededMaxDuration,
                             Status.Code.OK),
                // ok followed by error
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             TaskStatus.RUNNING,
                             TaskStatus.ERRORED,
                             requestOk,
                             requestErr,
                             Status.Code.OK),
                // task-id mismatch
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             TaskStatus.LAUNCHED,
                             TaskStatus.LAUNCHED,
                             requestOk.toBuilder().setTaskId(TEST_TASK_ID_2.toString()).build(),
                             null,
                             Status.Code.PERMISSION_DENIED),
                // invalid instance in error details
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             TaskStatus.RUNNING,
                             TaskStatus.RUNNING,
                             requestOk,
                             requestErrNoInstance,
                             Status.Code.INTERNAL),
                // Heartbeat with TASK_CONTAINER_INITIALIZING as ExecutionStatus
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             TaskStatus.LAUNCHED,
                             TaskStatus.LAUNCHED,
                             requestContainerInitializing,
                             null,
                             Status.Code.OK),
                // Heartbeat with WORKER_TERMINATED as ExecutionStatus
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             TaskStatus.LAUNCHED,
                             TaskStatus.LAUNCHED,
                             workerTerminated,
                             null,
                             Status.Code.OK)
                // ### Temporarily commented out till NVCA sends correct instanceType to Utils.
                // Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                //                                                      TEST_TASK_ID_1),
                //              TEST_TASK_ID_1,
                //              TaskStatus.RUNNING,
                //              TaskStatus.RUNNING,
                //              requestOk,
                //              requestErrBadInstance,
                //              Status.Code.INTERNAL)
        );
    }

    @ParameterizedTest
    @MethodSource("heartBeatArgs")
    void testHeartBeat(
            String token,
            UUID taskId,
            TaskStatus initialStatus,
            TaskStatus finalStatus,
            HeartbeatRequest reqOk,
            HeartbeatRequest reqErr,
            Status.Code expectedStatus) {
        setupGrpc(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID, taskId));
        connect(taskId);
        resetGrpc(token);
        var status = Status.Code.OK;
        try {
            var responseOK = sendHeartbeat(reqOk);
            assertThat(responseOK.getTaskId()).isEqualTo(taskId.toString());
            assertThat(responseOK.getExecutionStatus()).isEqualTo(LAUNCHED.toString());
            validateTaskStatus(TEST_NCA_ID, taskId, initialStatus);
            connect(taskId);
            validateTaskStatus(TEST_NCA_ID, taskId, initialStatus);
            if ((reqOk.getStatus() == TASK_CONTAINER_INITIALIZING)
                    || (reqOk.getStatus() == WORKER_TERMINATED)) {
                assertThat(testTaskService.getEventCount(taskId)).isEqualTo(1);
            } else {
                assertThat(testTaskService.getEventCount(taskId)).isEqualTo(2);
            }
            // Send same heartbeat again
            // This time the task execution status from last grpc call should be reflected
            responseOK = sendHeartbeat(reqOk);
            assertThat(responseOK.getTaskId()).isEqualTo(taskId.toString());
            assertThat(responseOK.getExecutionStatus()).isEqualTo(initialStatus.toString());
            if (reqErr != null) {
                var responseErr = sendHeartbeat(reqErr);
                assertThat(responseErr.getTaskId()).isEqualTo(taskId.toString());
                assertThat(responseErr.getExecutionStatus()).isEqualTo(initialStatus.toString());
                validateTaskStatus(TEST_NCA_ID, taskId, finalStatus);
                // Send same heartbeat again
                // This time the errored task execution status from last grpc call should be reflected
                responseErr = sendHeartbeat(reqErr);
                assertThat(responseErr.getTaskId()).isEqualTo(taskId.toString());
                assertThat(responseErr.getExecutionStatus()).isEqualTo(finalStatus.toString());
            }
        } catch (Exception e) {
            status = Status.fromThrowable(e).getCode();
        }
        assertThat(status).isEqualTo(expectedStatus);
    }

    Stream<Arguments> artifactsArgs() {
        return Stream.of(
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             0, 0, 0, 0, 0,
                             Status.Code.OK),
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_2),
                             TEST_TASK_ID_2,
                             2, 2, 4, 0, 0,
                             Status.Code.OK),
                // task-id mismatch
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_2,
                             0, 0, 0, 0, 0,
                             Status.Code.PERMISSION_DENIED)
        );
    }

    @ParameterizedTest
    @MethodSource("artifactsArgs")
    void testGetArtifacts(
            String token,
            UUID taskId,
            int artifactCount,
            int modelCount,
            int modelFiles,
            int resourceCount,
            int resourceFiles,
            Status.Code expectedStatus) {
        setupGrpc(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID, taskId));
        connect(taskId);
        resetGrpc(token);
        var status = Status.Code.OK;
        var request = ArtifactsRequest.newBuilder()
                .setTaskId(taskId.toString())
                .build();
        try {
            var artifacts = getArtifacts(request);
            assertThat(artifacts).isNotNull();
            assertThat(artifacts.getArtifactsCount()).isEqualTo(artifactCount);
            for (int i = 0; i < artifactCount; i++) {
                if (artifacts.getArtifacts(i).getKind().equals(MODEL)) {
                    modelCount--;
                    modelFiles -= artifacts.getArtifacts(i).getFilesCount();
                } else if (artifacts.getArtifacts(i).getKind().equals(RESOURCE)) {
                    resourceCount--;
                    resourceFiles -= artifacts.getArtifacts(i).getFilesCount();
                }
            }
            assertThat(modelCount).isZero();
            assertThat(resourceCount).isZero();
            assertThat(modelFiles).isZero();
            assertThat(resourceFiles).isZero();
        } catch (Exception e) {
            status = Status.fromThrowable(e).getCode();
        }
        assertThat(status).isEqualTo(expectedStatus);
    }

    Stream<Arguments> resultMetadataArgs() {
        var metadataJSON = """
                {"step-number": 2000, "token_accuracy": 0.874}
                """;

        var metadata = ResultMetadata.newBuilder()
                .setBody(ByteString.copyFromUtf8(metadataJSON))
                .build();
        var metadataText = "I am a teapot!";
        var metadataErr = ResultMetadata.newBuilder()
                .setBody(ByteString.copyFromUtf8(metadataText))
                .build();
        var resultMetadataRequest = ResultMetadataRequest.newBuilder()
                .setTaskId(TEST_TASK_ID_1.toString())
                .setPercentComplete(10)
                .setResultName("Name 10")
                .setStatus(IN_PROGRESS)
                .setMetadata(metadata)
                .setInstanceType(L40G_INSTANCE_TYPE)
                .build();
        var errorDetails = ErrorDetails.newBuilder()
                .setType("Type1")
                .setTitle("Failed Execution")
                .setStatus(75)
                .setDetail("Failed Execution")
                .build();
        return Stream.of(
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest,
                             resultMetadataRequest.toBuilder()
                                     .setStatus(COMPLETED)
                                     .setPercentComplete(100)
                                     .build(),
                             100,
                             Status.Code.OK),
                // Null ResultMetadata and null ErrorDetails
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest,
                             ResultMetadataRequest.newBuilder()
                                     .setTaskId(TEST_TASK_ID_1.toString())
                                     .setPercentComplete(50)
                                     .setResultName("PERCENT_50")
                                     .setStatus(IN_PROGRESS)
                                     .setInstanceType(L40G_INSTANCE_TYPE)
                                     .build(),
                             50,
                             Status.Code.INTERNAL),
                // Empty ResultMetadata
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest,
                             resultMetadataRequest.toBuilder()
                                     .setStatus(COMPLETED)
                                     .setPercentComplete(100)
                                     .setMetadata(ResultMetadata.newBuilder().build())
                                     .build(),
                             100,
                             Status.Code.OK),
                // "null" ResultMetadata
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest,
                             resultMetadataRequest.toBuilder()
                                     .setStatus(COMPLETED)
                                     .setPercentComplete(100)
                                     .setMetadata(ResultMetadata.newBuilder()
                                                          .setBody(ByteString.copyFromUtf8("null"))
                                                          .build())
                                     .build(),
                             100,
                             Status.Code.OK),
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest,
                             resultMetadataRequest.toBuilder()
                                     .setStatus(ERRORED)
                                     .setPercentComplete(10)
                                     .setErrorDetails(errorDetails)
                                     .build(),
                             10,
                             Status.Code.OK),
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest,
                             resultMetadataRequest.toBuilder()
                                     .setStatus(EXCEEDED_MAX_RUNTIME_DURATION)
                                     .setPercentComplete(10)
                                     .setErrorDetails(errorDetails)
                                     .build(),
                             10,
                             Status.Code.OK),
                // bad metadata
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest.toBuilder()
                                     .setMetadata(metadataErr)
                                     .build(),
                             null,
                             null,
                             Status.Code.INTERNAL),
                // task-id mismatch
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest.toBuilder()
                                     .setTaskId(TEST_TASK_ID_2.toString())
                                     .build(),
                             null,
                             null,
                             Status.Code.PERMISSION_DENIED),
                // invalid state
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             resultMetadataRequest.toBuilder()
                                     .setStatus(PENDING_EVALUATION)
                                     .build(),
                             null,
                             null,
                             Status.Code.INTERNAL)
                // ### Temporarily commented out till NVCA sends correct instanceType to Utils.
                // invalid instance
                // Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                //                                                      TEST_TASK_ID_1),
                //              TEST_TASK_ID_1,
                //              resultMetadataRequest.toBuilder()
                //                      .setStatus(ERRORED)
                //                      .setInstanceType("Invalid Instance")
                //                      .setErrorDetails(errorDetails).build(),
                //              null,
                //              null,
                //              Status.Code.INTERNAL)
        );
    }

    @ParameterizedTest
    @MethodSource("resultMetadataArgs")
    void testResultMetadata(
            String token,
            UUID taskId,
            ResultMetadataRequest reqInitial,
            ResultMetadataRequest reqFinal,
            Integer percentComplete,
            Status.Code expectedStatus) {
        setupGrpc(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID, taskId));
        connect(taskId);
        sendHeartbeat(taskId);
        resetGrpc(token);

        var event = 2;
        final CountDownLatch finishLatch = new CountDownLatch(1);
        StreamObserver<ResultMetadataRequest> requestStreamObserver =
                workerStub.sendResultMetadata(
                        new TestResultMetadataStreamObserver(taskId,
                                                             TaskStatus.RUNNING,
                                                             finishLatch,
                                                             expectedStatus));
        requestStreamObserver.onNext(reqInitial);

        if (reqFinal != null) {
            assertThat(finishLatch.getCount()).isEqualTo(1);
            requestStreamObserver.onNext(reqFinal);
            assertThat(finishLatch.getCount()).isEqualTo(1);
            requestStreamObserver.onCompleted();
            event++;
        }

        // Receiving happens asynchronously
        try {
            if (!finishLatch.await(1, TimeUnit.MINUTES)) {
                log.error("ResultMetadataResponse can not finish within 1 minutes");
                assertThat(finishLatch.getCount()).isZero();
            }
        } catch (InterruptedException e) {
            log.error("Interrupted", e);
            Assertions.fail("Test failed: " + e.getMessage());
        }

        if (expectedStatus != Status.Code.OK) {
            return;
        }

        assertThat(testTaskService.getEventCount(taskId)).isEqualTo(event);
        if (reqFinal != null && reqFinal.hasPercentComplete()) {
            assertThat(testTaskService.getPercentComplete(taskId))
                    .isEqualTo(reqFinal.getPercentComplete());
        }
        if (percentComplete != null) {
            var results = resultService.fetchResults(TEST_NCA_ID, taskId);
            List<ResultDto> mutableList = new ArrayList<>(results);
            mutableList.sort(Comparator.comparing(ResultDto::createdAt));
            assertThat(mutableList.getLast().metadata().get("percentComplete").asInt())
                    .isEqualTo(percentComplete.intValue());
        }
    }

    Stream<Arguments> tokenArgs() {
        return Stream.of(
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_1,
                             Status.Code.OK),
                // task-id mismatch
                Arguments.of(tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                                     TEST_TASK_ID_1),
                             TEST_TASK_ID_2,
                             Status.Code.PERMISSION_DENIED)
        );
    }

    @ParameterizedTest
    @MethodSource("tokenArgs")
    void testRefreshToken(
            String token,
            UUID taskId,
            Status.Code expectedStatus) {
        var request = RefreshTokenRequest.newBuilder().setTaskId(taskId.toString()).build();
        setupGrpc(token);
        var status = Status.Code.OK;
        try {
            var newToken = refreshToken(request);
            resetGrpc(newToken);
            connect(taskId);
        } catch (Exception e) {
            status = Status.fromThrowable(e).getCode();
        }
        assertThat(status).isEqualTo(expectedStatus);
    }

    @Test
    void testRequestSecretCredentials() {
        var token = tokenService.issueWorkerAccessAssertion(TEST_NCA_ID,
                                                            TEST_TASK_ID_1);
        setupGrpc(token);
        var status = Status.Code.OK;
        var taskId = TEST_TASK_ID_1;
        var request = SecretCredentialsRequest.newBuilder()
                .setTaskId(taskId.toString())
                .build();
        try {
            requestSecretCredentials(request);
        } catch (Exception e) {
            status = Status.fromThrowable(e).getCode();
        }
        assertThat(status).isEqualTo(Status.Code.OK);
    }

    private void connect(UUID taskId) {
        var connect = workerBlockingStub.connect(ConnectRequest.newBuilder()
                                                         .setTaskId(taskId.toString())
                                                         .setInstanceId("local-instance")
                                                         .build());
        assertThat(connect).isNotNull();
        assertThat(connect.getConnectedRegion()).isNotNull();
        log.info("worker connected");
    }

    private void sendHeartbeat(UUID taskId) {
        var heartbeat = workerBlockingStub.sendHeartbeat(HeartbeatRequest.newBuilder()
                                                                 .setTaskId(taskId.toString())
                                                                 .build());
        assertThat(heartbeat).isNotNull();
    }

    private HeartbeatResponse sendHeartbeat(HeartbeatRequest heartbeatRequest) {
        var heartbeat = workerBlockingStub.sendHeartbeat(heartbeatRequest);
        assertThat(heartbeat).isNotNull();
        return heartbeat;
    }

    private String refreshToken(RefreshTokenRequest request) {
        var tokenResponse = workerBlockingStub.refreshToken(request);
        assertThat(tokenResponse).isNotNull();
        assertThat(tokenResponse.getToken()).isNotNull();
        return tokenResponse.getToken();
    }

    private void requestSecretCredentials(SecretCredentialsRequest request) {
        var response = workerBlockingStub.requestSecretCredentials(request);
        assertThat(response).isNotNull();
        assertThat(response.getExpiration()).isNotNull();
        assertThat(fromTimestamp(response.getExpiration()))
                .isAfterOrEqualTo(Instant.now().plus(Duration.ofHours(1)));
    }

    private ArtifactsResponse getArtifacts(ArtifactsRequest request) {
        return workerBlockingStub.getArtifacts(request);
    }

    private void validateTaskStatus(String ncaId, UUID taskId, TaskStatus status) {
        var taskEntity = taskService.fetchTask(taskId);
        assertThat(taskEntity.getStatus()).isEqualTo(status);
        assertThat(taskEntity.getNcaId()).isEqualTo(ncaId);
        if (status == TaskStatus.ERRORED) {
            assertThat(taskEntity.getHealth()).isNotBlank();
        }
    }

}
