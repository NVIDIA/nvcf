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

import static com.nvidia.nvct.persistence.task.entity.TaskStatus.COMPLETED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.ERRORED;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.EXCEEDED_MAX_RUNTIME_DURATION;
import static com.nvidia.nvct.persistence.task.entity.TaskStatus.RUNNING;
import static com.nvidia.nvct.service.event.EventService.STATUS_CHANGE_EVENT_MESSAGE;
import static com.nvidia.nvct.service.event.EventService.STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_TASK_ID;
import static com.nvidia.nvct.util.NvctConstants.TERMINAL_TASK_STATUSES;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import com.google.protobuf.ByteString;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.proto.ResultMetadataRequest;
import com.nvidia.nvct.proto.ResultMetadataResponse;
import com.nvidia.nvct.rest.task.dto.HealthDto;
import com.nvidia.nvct.service.event.EventService;
import com.nvidia.nvct.service.metrics.TaskErrorMetricsService;
import com.nvidia.nvct.service.metrics.TaskSuccessMetricsService;
import com.nvidia.nvct.service.result.ResultService;
import com.nvidia.nvct.service.icms.IcmsService;
import com.nvidia.nvct.service.task.TaskService;
import com.nvidia.nvct.service.token.TokenService;
import com.nvidia.nvct.util.NvctUtils;
import io.grpc.stub.StreamObserver;
import io.micrometer.tracing.Tracer;
import java.time.Instant;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import lombok.NonNull;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;

@Slf4j
public class ResultMetadataStreamObserver implements StreamObserver<ResultMetadataRequest> {
    private static final String MESG_TASK_ERROR =
            "Task id '%s': Worker error '%s', status '%s', details '%s'";
    private static final String MESG_INVALID_STATUS =
            "Task id '%s': Invalid ExecutionStatus %s";
    private static final String MESG_INVALID_METADATA =
            "Task id '%s': Invalid ResultMetadata %s";

    private static final String MESG_TASK_INFO =
            "Task id '{}': {}";
    private static final String MESG_START_GRPC_ENDPOINT =
            "Task id '{}': Starting call to gRPC endpoint 'SendResultMetadata' with ExecutionStatus '{}'";
    private static final String MESG_END_GRPC_ENDPOINT =
            "Task id '{}': Completed gRPC call 'SendResultMetadata'";
    private static final String MESG_RECEIVING_RESULT_AFTER_TERMINAL_STATUS =
            "Task id '{}': Receiving result metadata after Task has reached terminal status '{}'";
    private static final String MESG_LOG_PERCENT_COMPLETE =
            "Task id '{}': '{}' percent complete";
    private static final String MESG_ERROR_IN_SEND_RESULT_METADATA =
            "Encountered error in sendResultMetadata";

    private final StreamObserver<ResultMetadataResponse> responseObserver;
    private final TaskService taskService;
    private final TokenService tokenService;
    private final EventService eventService;
    private final ResultService resultService;
    private final IcmsService icmsService;
    private final JsonMapper jsonMapper;
    private final TaskSuccessMetricsService taskSuccessMetricsService;
    private final TaskErrorMetricsService taskErrorMetricsService;
    private final Tracer tracer;

    private UUID taskId;
    private TaskStatus executionStatus;

    public ResultMetadataStreamObserver(
            StreamObserver<ResultMetadataResponse> responseObserver,
            TaskService taskService,
            TokenService tokenService,
            EventService eventService,
            ResultService resultService,
            IcmsService icmsService,
            TaskSuccessMetricsService taskSuccessMetricsService,
            TaskErrorMetricsService taskErrorMetricsService,
            JsonMapper jsonMapper,
            Tracer tracer) {
        this.responseObserver = responseObserver;
        this.taskService = taskService;
        this.tokenService = tokenService;
        this.eventService = eventService;
        this.resultService = resultService;
        this.icmsService = icmsService;
        this.taskSuccessMetricsService = taskSuccessMetricsService;
        this.taskErrorMetricsService = taskErrorMetricsService;
        this.jsonMapper = jsonMapper;
        this.tracer = tracer;
    }

    @Override
    public void onNext(ResultMetadataRequest request) {
        this.taskId = UUID.fromString(request.getTaskId());
        log.info(MESG_START_GRPC_ENDPOINT, taskId, request.getStatus().name());
        var taskEntity = taskService.fetchTask(taskId);
        this.executionStatus = taskEntity.getStatus();
        // Ignore result metadata if the Task has reached terminal status.
        if (TERMINAL_TASK_STATUSES.contains(taskEntity.getStatus())) {
            log.warn(MESG_RECEIVING_RESULT_AFTER_TERMINAL_STATUS, taskId, taskEntity.getStatus());
            log.info(MESG_END_GRPC_ENDPOINT, taskId);
            return;
        }

        var ncaId = taskEntity.getNcaId();
        tokenService.validateWorkerAccessAssertion(ncaId, taskId);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_TASK_ID, request.getTaskId()));

        Optional<Integer> percentComplete = request.hasPercentComplete() ?
                                    Optional.of(request.getPercentComplete()) : Optional.empty();
        var errorMessage = StringUtils.EMPTY;

        var status = switch (request.getStatus()) {
            case IN_PROGRESS -> {
                log.debug(MESG_LOG_PERCENT_COMPLETE, taskId, percentComplete);
                validateResultMetadata(taskId, request.getMetadata().getBody());
                var metaData = includePercentCompleteInMetadata(request);
                taskSuccessMetricsService.recordTaskSuccess(ncaId); // Record for every result.
                resultService.insertResult(taskEntity, request.getResultName(), metaData);
                yield RUNNING;
            }
            case ERRORED -> {
                var errorDetail = errorDetail(taskId, request);
                taskService.updateTask(taskId, ERRORED, errorDetail);
                taskErrorMetricsService.recordTaskError(ncaId);
                errorMessage = request.getErrorDetails().getDetail();
                yield ERRORED;
            }
            case EXCEEDED_MAX_RUNTIME_DURATION -> {
                var errorDetail = errorDetail(taskId, request);
                taskService.updateTask(taskId, EXCEEDED_MAX_RUNTIME_DURATION, errorDetail);
                taskErrorMetricsService.recordTaskError(ncaId);
                errorMessage = request.getErrorDetails().getDetail();
                yield EXCEEDED_MAX_RUNTIME_DURATION;
            }
            case COMPLETED -> {
                validateResultMetadata(taskId, request.getMetadata().getBody());
                var metaData = includePercentCompleteInMetadata(request);
                resultService.insertResult(taskEntity, request.getResultName(), metaData);
                taskSuccessMetricsService.recordTaskSuccess(ncaId); // Record when completed.
                icmsService.terminateInstanceByTaskId(ncaId, taskId);
                yield COMPLETED;
            }
            default -> {
                var mesg = MESG_INVALID_STATUS.formatted(taskId, request.getStatus());
                log.error(mesg);
                throw new IllegalArgumentException(mesg);
            }
        };

        taskService.updateTask(taskId, status, Instant.now(), percentComplete);
        if (taskEntity.getStatus() != status) {
            var mesg = StringUtils.isBlank(errorMessage) ?
                    STATUS_CHANGE_EVENT_MESSAGE.formatted(taskEntity.getStatus(), status) :
                    STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR
                            .formatted(taskEntity.getStatus(), status, errorMessage);
            log.info(MESG_TASK_INFO, taskId, mesg);
            eventService.insertEvent(ncaId, taskId, mesg);
        }
        log.info(MESG_END_GRPC_ENDPOINT, taskId);
    }

    @Override
    public void onError(Throwable throwable) {
        log.error(MESG_ERROR_IN_SEND_RESULT_METADATA, throwable);
        responseObserver.onError(throwable);
    }

    @Override
    public void onCompleted() {
        responseObserver.onNext(ResultMetadataResponse.newBuilder()
                                        .setExecutionStatus(executionStatus.toString())
                                        .setTaskId(taskId.toString())
                                        .build());
        responseObserver.onCompleted();
    }

    private void validateResultMetadata(@NonNull UUID taskId, ByteString body) {
        var metadata = body.toStringUtf8();

        if (StringUtils.isBlank(metadata)) {
            return;
        }

        try {
            jsonMapper.readValue(metadata, ObjectNode.class);
        } catch (JacksonException e) {
            var mesg = MESG_INVALID_METADATA.formatted(taskId, metadata);
            log.error(mesg);
            throw new IllegalArgumentException(mesg, e);
        }
    }

    @SneakyThrows
    private String includePercentCompleteInMetadata(@NonNull ResultMetadataRequest request) {
        var rawJson = request.getMetadata().getBody().toStringUtf8();
        if (request.hasPercentComplete()) {
            // Utils Container can marshall a "null" value as result metadata. It gets
            // deserialized as NullNode and cause ClassCastException when it is cast as
            // ObjectNode.
            var objectNode = StringUtils.isBlank(rawJson) || rawJson.equals("null") ?
                    jsonMapper.createObjectNode() : (ObjectNode) jsonMapper.readTree(rawJson);
            objectNode.put("percentComplete", request.getPercentComplete());
            return jsonMapper.writeValueAsString(objectNode);
        }
        return rawJson;
    }

    private HealthDto errorDetail(
            UUID taskId,
            ResultMetadataRequest request) {
        var errorDetails = request.getErrorDetails();
        var mesg = MESG_TASK_ERROR.formatted(taskId,
                                             errorDetails.getType(),
                                             errorDetails.getStatus(),
                                             errorDetails.getDetail());
        return icmsService.getHealthDto(taskId, request.getInstanceType(), mesg);
    }

}
