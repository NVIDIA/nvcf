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

import static com.nvidia.nvct.persistence.task.entity.TaskStatus.ERRORED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.EXCEEDED_MAX_RUNTIME_DURATION;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.LAUNCHED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.QUEUED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.RUNNING;
import static com.nvidia.nvct.proto.ExecutionStatus.TASK_CONTAINER_INITIALIZING;
import static com.nvidia.nvct.proto.ExecutionStatus.WORKER_TERMINATED;
import static com.nvidia.nvct.service.event.EventService.STATUS_CHANGE_EVENT_MESSAGE;
import static com.nvidia.nvct.service.event.EventService.STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_TASK_ID;
import static com.nvidia.nvct.util.NvctConstants.TERMINAL_TASK_STATUSES;
import static com.nvidia.nvct.util.ProtoMappingUtils.toTimestamp;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import com.nvidia.nvct.proto.ArtifactsRequest;
import com.nvidia.nvct.proto.ArtifactsResponse;
import com.nvidia.nvct.proto.ArtifactsResponse.ArtifactResponse;
import com.nvidia.nvct.proto.ConnectRequest;
import com.nvidia.nvct.proto.ConnectResponse;
import com.nvidia.nvct.proto.ExecutionStatus;
import com.nvidia.nvct.proto.HeartbeatRequest;
import com.nvidia.nvct.proto.HeartbeatResponse;
import com.nvidia.nvct.proto.RefreshTokenRequest;
import com.nvidia.nvct.proto.RefreshTokenResponse;
import com.nvidia.nvct.proto.ResultMetadataRequest;
import com.nvidia.nvct.proto.ResultMetadataResponse;
import com.nvidia.nvct.proto.SecretCredentialsRequest;
import com.nvidia.nvct.proto.SecretCredentialsResponse;
import com.nvidia.nvct.proto.WorkerGrpc;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.event.EventService;
import com.nvidia.nvct.service.metrics.TaskErrorMetricsService;
import com.nvidia.nvct.service.metrics.TaskRunningMetricsService;
import com.nvidia.nvct.service.metrics.TaskSuccessMetricsService;
import com.nvidia.nvct.service.registry.RegistryArtifactService;
import com.nvidia.nvct.service.result.ResultService;
import com.nvidia.nvct.service.icms.IcmsService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.service.token.TokenService;
import com.nvidia.nvct.util.NvctUtils;
import io.grpc.stub.StreamObserver;
import io.micrometer.tracing.Tracer;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import net.devh.boot.grpc.server.service.GrpcService;
import org.springframework.beans.factory.annotation.Value;

@Slf4j
@GrpcService
public class GrpcWorkerService extends WorkerGrpc.WorkerImplBase {

    private static final String MESG_START_GRPC_ENDPOINT =
            "Task id '{}': Starting call to gRPC endpoint '{}'";
    private static final String MESG_END_GRPC_ENDPOINT =
            "Task id '{}': Completed call to gRPC endpoint '{}'";
    private static final String MESG_TASK_INFO =
            "Task id '{}': {}";
    private static final String MESG_RECEIVING_HEARTBEAT_AFTER_TERMINAL_STATUS =
            "Task id '{}': Receiving heartbeat after Task has reached terminal status '{}'";
    private static final String MESG_NOT_SUPPORTED_ARTIFACT_TYPE =
            "Task id '%s': Received unsupported artifact type '%s'";
    private static final String MESG_TASK_CONTAINER_INITIALIZING =
            "Task id '%s': Task Container initializing";
    private static final String MESG_WORKER_TERMINATED =
            "Task id '%s': Worker terminated due to SIGTERM from the control plane";

    private static final Duration VALIDITY = Duration.ofHours(3);

    private final String awsRegion;
    private final TokenService tokenService;
    private final IcmsService icmsService;
    private final AccountService accountService;
    private final RegistryArtifactService artifactService;
    private final EventService eventService;
    private final TaskService taskService;
    private final ResultService resultService;
    private final JsonMapper jsonMapper;
    private final TaskRunningMetricsService taskRunningMetricsService;
    private final TaskSuccessMetricsService taskSuccessMetricsService;
    private final TaskErrorMetricsService taskErrorMetricsService;
    private final Tracer tracer;

    public GrpcWorkerService(
            TokenService tokenService,
            IcmsService icmsService,
            AccountService accountService,
            RegistryArtifactService artifactService,
            EventService eventService,
            TaskService taskService,
            ResultService resultService,
            JsonMapper jsonMapper,
            TaskRunningMetricsService taskRunningMetricsService,
            TaskSuccessMetricsService taskSuccessMetricsService,
            TaskErrorMetricsService taskErrorMetricsService,
            Tracer tracer,
            @Value("${nvct.aws.region}") String awsRegion) {
        this.tokenService = tokenService;
        this.icmsService = icmsService;
        this.accountService = accountService;
        this.artifactService = artifactService;
        this.eventService = eventService;
        this.taskService = taskService;
        this.resultService = resultService;
        this.awsRegion = awsRegion;
        this.taskRunningMetricsService = taskRunningMetricsService;
        this.taskSuccessMetricsService = taskSuccessMetricsService;
        this.taskErrorMetricsService = taskErrorMetricsService;
        this.tracer = tracer;
        this.jsonMapper = jsonMapper;
    }

    @Override
    public void connect(ConnectRequest request, StreamObserver<ConnectResponse> responseObserver) {
        var taskId = UUID.fromString(request.getTaskId());
        log.info(MESG_START_GRPC_ENDPOINT, taskId, "connect");

        var taskEntity = taskService.fetchTask(taskId);
        var ncaId = taskEntity.getNcaId();

        tokenService.validateWorkerAccessAssertion(ncaId, taskId);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_TASK_ID, request.getTaskId()));

        if (taskEntity.getStatus() == QUEUED) {
            taskService.updateTask(taskId, LAUNCHED);
            var mesg = STATUS_CHANGE_EVENT_MESSAGE.formatted(QUEUED, LAUNCHED);
            log.info(MESG_TASK_INFO, taskId, mesg);
            eventService.insertEvent(ncaId, taskId, mesg);
        }
        responseObserver.onNext(ConnectResponse.newBuilder()
                                        .setConnectedRegion(awsRegion).build());
        responseObserver.onCompleted();
        log.info(MESG_END_GRPC_ENDPOINT, taskId, "connect");
    }

    @Override
    public void sendHeartbeat(
            HeartbeatRequest request,
            StreamObserver<HeartbeatResponse> responseObserver) {
        var taskId = UUID.fromString(request.getTaskId());
        var uniqueStrForLogs = "sendHeartbeat-" + Instant.now().getEpochSecond();
        log.info(MESG_START_GRPC_ENDPOINT, taskId, uniqueStrForLogs);

        var taskEntity = taskService.fetchTask(taskId);
        var ncaId = taskEntity.getNcaId();

        tokenService.validateWorkerAccessAssertion(ncaId, taskId);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_TASK_ID, request.getTaskId()));

        if (TERMINAL_TASK_STATUSES.contains(taskEntity.getStatus())) {
            // Ignore heartbeat once the Task has already reached terminal status.
            var status = taskEntity.getStatus();
            log.warn(MESG_RECEIVING_HEARTBEAT_AFTER_TERMINAL_STATUS, taskId, status);
        } else {
            if (request.hasErrorMessage()) {
                // Map ExecutionStatus in proto to TaskStatus.
                var status = request.getStatus() == ExecutionStatus.EXCEEDED_MAX_RUNTIME_DURATION ?
                        EXCEEDED_MAX_RUNTIME_DURATION : ERRORED;
                var errorMessage = request.getErrorMessage();
                var mesg = STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR
                                .formatted(taskEntity.getStatus(), status, errorMessage);
                log.info(MESG_TASK_INFO, taskId, mesg);
                taskService.updateTask(taskId, status,
                                       icmsService.getHealthDto(taskId,
                                                                request.getInstanceType(),
                                                                errorMessage));
                log.info(MESG_TASK_INFO, taskId, "Updated with terminal status " + status);
                eventService.insertEvent(ncaId, taskId, mesg);
            } else if (taskEntity.getStatus() == LAUNCHED) {
                if (request.getStatus() == TASK_CONTAINER_INITIALIZING) {
                    var mesg = MESG_TASK_CONTAINER_INITIALIZING.formatted(taskId);
                    log.info(mesg);
                    taskService.updateTask(taskId);  // Update lastUpdatedAt timestamp
                } else if (request.getStatus() == WORKER_TERMINATED) {
                    // If worker's control plane fails to pull the container image or helm chart,
                    // either due to bad registry credentials or incorrect URL in the Task
                    // definition, then NVCT/NEWT will first send the error information to ICMS and
                    // then send SIGTERM signal to the Utils Container. On receiving the SIGTERM
                    // signal, Utils Container will send a heartbeat with WORKER_TERMINATED
                    // ExecutionStatus to the NVCT API. NVCT API will just ignore such a heartbeat
                    // and NOT update the Task's lastUpdatedAt timestamp so that the async
                    // MonitorLaunchedTaskRoutine can get the health information from ICMS during
                    // the next iteration. This way, the user can see the actual error message that
                    // the control plane received when pulling the container image or helm chart.
                    var mesg = MESG_WORKER_TERMINATED.formatted(taskId);
                    log.info(mesg);
                } else {
                    var mesg = STATUS_CHANGE_EVENT_MESSAGE.formatted(LAUNCHED, RUNNING);
                    log.info(MESG_TASK_INFO, taskId, mesg);
                    taskService.updateTask(taskId, RUNNING, Instant.now());
                    eventService.insertEvent(ncaId, taskId, mesg);
                    var accountName = accountService.getAccountName(ncaId);
                    taskRunningMetricsService.recordRunningTask(ncaId, accountName);
                }
            } else if (taskEntity.getStatus() == RUNNING) {
                log.info(MESG_TASK_INFO, taskId, "Received heartbeat");
                taskService.updateTask(taskId, Instant.now());
            }
        }
        responseObserver.onNext(HeartbeatResponse.newBuilder()
                                        .setTaskId(taskId.toString())
                                        .setExecutionStatus(taskEntity.getStatus().toString())
                                        .build());
        responseObserver.onCompleted();
        log.info(MESG_END_GRPC_ENDPOINT, taskId, uniqueStrForLogs);
    }

    @Override
    public void getArtifacts(
            ArtifactsRequest request,
            StreamObserver<ArtifactsResponse> responseObserver) {
        var taskId = UUID.fromString(request.getTaskId());
        log.info(MESG_START_GRPC_ENDPOINT, taskId, "getArtifacts");

        var ncaId = taskService.fetchTask(taskId).getNcaId();
        tokenService.validateWorkerAccessAssertion(ncaId, taskId);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_TASK_ID, request.getTaskId()));

        var artifacts = artifactService.getPresignedUrls(ncaId, taskId);
        var artifactResponse = new ArrayList<ArtifactResponse>();
        for (var artifact : artifacts) {
            var files = new ArrayList<ArtifactResponse.ArtifactFile>();
            for (var file : artifact.files()) {
                files.add(ArtifactResponse.ArtifactFile.newBuilder()
                                  .setPath(Objects.requireNonNullElse(file.path(), ""))
                                  .setUrl(file.url())
                                  .build());
            }
            var kind = switch (artifact.artifactType()) {
                case ArtifactTypeEnum.MODEL -> ArtifactResponse.ArtifactKindEnum.MODEL;
                case ArtifactTypeEnum.RESOURCE -> ArtifactResponse.ArtifactKindEnum.RESOURCE;
                default -> {
                    var errorMsg = MESG_NOT_SUPPORTED_ARTIFACT_TYPE.formatted(taskId,
                                                                              artifact.artifactType());
                    log.error(errorMsg);
                    throw new IllegalStateException(errorMsg);
                }
            };
            artifactResponse.add(ArtifactResponse.newBuilder()
                                         .setKind(kind)
                                         .setName(artifact.name())
                                         .setVersion(artifact.version())
                                         .addAllFiles(files)
                                         .build());
        }
        var response = ArtifactsResponse.newBuilder()
                .addAllArtifacts(artifactResponse)
                .build();
        responseObserver.onNext(response);
        responseObserver.onCompleted();
        log.info(MESG_END_GRPC_ENDPOINT, taskId, "getArtifacts");
    }

    @Override
    public StreamObserver<ResultMetadataRequest> sendResultMetadata(
            StreamObserver<ResultMetadataResponse> responseObserver) {
        return new ResultMetadataStreamObserver(responseObserver,
                                                taskService,
                                                tokenService,
                                                eventService,
                                                resultService,
                                                icmsService,
                                                taskSuccessMetricsService,
                                                taskErrorMetricsService,
                                                jsonMapper,
                                                tracer);
    }

    @Override
    public void refreshToken(
            RefreshTokenRequest request,
            StreamObserver<RefreshTokenResponse> responseObserver) {
        var taskId = UUID.fromString(request.getTaskId());
        log.info(MESG_START_GRPC_ENDPOINT, taskId, "refreshToken");

        var ncaId = taskService.fetchTask(taskId).getNcaId();
        tokenService.validateWorkerAccessAssertion(ncaId, taskId);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_TASK_ID, request.getTaskId()));

        var newToken = tokenService.issueWorkerAccessAssertion(ncaId, taskId);
        var response = RefreshTokenResponse.newBuilder().setToken(newToken).build();
        responseObserver.onNext(response);
        responseObserver.onCompleted();
        log.info(MESG_END_GRPC_ENDPOINT, taskId, "refreshToken");
    }

    @Override
    public void requestSecretCredentials(
            SecretCredentialsRequest request,
            StreamObserver<SecretCredentialsResponse> responseObserver) {
        var taskId = UUID.fromString(request.getTaskId());
        log.info(MESG_START_GRPC_ENDPOINT, taskId, "requestSecretCredentials");

        var task = taskService.fetchTask(taskId);
        var ncaId = task.getNcaId();
        tokenService.validateWorkerAccessAssertion(ncaId, taskId);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_TASK_ID, request.getTaskId()));

        var assertionToken = tokenService.issueSecretsAssertion(task);
        var response = SecretCredentialsResponse.newBuilder()
                .setSecretCredentialsToken(assertionToken)
                .setExpiration(toTimestamp(Instant.now().plus(VALIDITY))).build();
        responseObserver.onNext(response);
        responseObserver.onCompleted();
        log.info(MESG_END_GRPC_ENDPOINT, taskId, "requestSecretCredentials");
    }

}
