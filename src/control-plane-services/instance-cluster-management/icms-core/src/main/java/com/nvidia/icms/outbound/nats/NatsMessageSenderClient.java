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
package com.nvidia.icms.outbound.nats;

import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocTerminatePodMessageModel;
import com.nvidia.icms.util.GsonCompatMapper;
import io.nats.client.JetStream;
import io.nats.client.impl.NatsMessage;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;

/**
 * NatsMessageSenderClient is responsible for sending messages to a NATS server.
 * It supports sending function, task, and termination messages to specific NATS subjects.
 */
@Component
@Slf4j
@AllArgsConstructor
public class NatsMessageSenderClient {

    private static final String MESG_FAILED_TO_PUBLISH =
            "Failed to publish NATS message for cluster %s: %s";
    private final String FUNCTION = "Function";
    private final String TASK = "Task";

    /**
     * Enum representing the result of sending a NATS message.
     */
    public enum SendNatsMessageResult {
        SUCCESS,       // Message was successfully sent
        FAILURE,       // Message failed to send
        NO_RESPONDERS,  // No responders available for the message
        NOT_ENABLED // NATS is not enabled
    }

    private final FixedNatsPool fixedNatsPool;
    private final NatsConfigurationProperties natsConfigurationProperties;

    /**
     * Sends a list of function-related messages to the NATS server.
     *
     * @param messages  List of ByocSqsMessageModel messages to send.
     * @param clusterId The cluster ID associated with the messages.
     */
    public void sendFunctionMessages(
            @Nullable List<ByocSqsMessageModel> messages,
            @NotNull String clusterId) {

        if (!checkIfNatsEnabled() || messages == null || messages.isEmpty()) {
            return;
        }

        log.info("Sending {} function messages...", messages.size());
        messages.forEach(
                message -> requireSuccessfulSend(
                        sendFunctionMessage(message, clusterId), clusterId));
        log.info("Done with sending {} function messages", messages.size());
    }

    /**
     * Sends a single function-related message to the NATS server.
     *
     * @param message   The ByocSqsMessageModel message to send.
     * @param clusterId The cluster ID associated with the message.
     * @return The result of the message send operation.
     */
    public SendNatsMessageResult sendFunctionMessage(
            @NotNull ByocSqsMessageModel message,
            @NotNull String clusterId) {

        if (!checkIfNatsEnabled()) {
            return SendNatsMessageResult.NOT_ENABLED;
        }

        String messageAsJson = GsonCompatMapper.toJson(message);
        return sendCreationMessage(messageAsJson, true, clusterId,
                                   message.getLaunchSpecification().getGpuName(),
                                   message.getInstanceType(), getLogHeaderForFunction(message, clusterId));
    }

    /**
     * Sends a list of task-related messages to the NATS server.
     *
     * @param messages  List of ByocSqsMessageModel messages to send.
     * @param clusterId The cluster ID associated with the messages.
     */
    public void sendTaskMessages(
            @Nullable List<ByocSqsMessageModel> messages,
            @NotNull String clusterId) {

        if (!checkIfNatsEnabled() || messages == null || messages.isEmpty()) {
            return;
        }

        log.info("Sending {} task messages...", messages.size());
        messages.forEach(
                message -> requireSuccessfulSend(
                        sendTaskMessage(message, clusterId), clusterId));
        log.info("Done with sending {} task messages", messages.size());
    }

    /**
     * Sends a single task-related message to the NATS server.
     *
     * @param message   The ByocSqsMessageModel message to send.
     * @param clusterId The cluster ID associated with the message.
     * @return The result of the message send operation.
     */
    public SendNatsMessageResult sendTaskMessage(
            @NotNull ByocSqsMessageModel message,
            @NotNull String clusterId) {

        if (!checkIfNatsEnabled()) {
            return SendNatsMessageResult.NOT_ENABLED;
        }

        String messageAsJson = GsonCompatMapper.toJson(message);
        return sendCreationMessage(messageAsJson, false, clusterId,
                                   message.getLaunchSpecification().getGpuType(),
                                   message.getInstanceType(),
                                   getLogHeaderForTask(message, clusterId));
    }

    /**
     * Sends a list of termination-related messages to the NATS server.
     *
     * @param messages  List of ByocTerminatePodMessageModel messages to send.
     * @param clusterId The cluster ID associated with the messages.
     */
    public void sendTerminateInstanceMessages(
            @Nullable List<ByocTerminatePodMessageModel> messages,
            @NotNull String clusterId) {

        if (!checkIfNatsEnabled() || messages == null || messages.isEmpty()) {
            return;
        }

        log.info("Sending {} termination messages...", messages.size());
        messages.forEach(
                message -> requireSuccessfulSend(
                        sendTerminateInstanceMessage(message, clusterId), clusterId));
        log.info("Done with sending {} termination messages", messages.size());
    }

    /**
     * Sends a single termination-related message to the NATS server.
     *
     * @param message   The ByocTerminatePodMessageModel message to send.
     * @param clusterId The cluster ID associated with the message.
     * @return The result of the message send operation.
     */
    public SendNatsMessageResult sendTerminateInstanceMessage(
            @NotNull ByocTerminatePodMessageModel message,
            @NotNull String clusterId) {

        if (!checkIfNatsEnabled()) {
            return SendNatsMessageResult.NOT_ENABLED;
        }

        String messageAsJson = GsonCompatMapper.toJson(message);
        return sendTerminationMessage(messageAsJson, clusterId, getLogHeader(message, clusterId));
    }

    /**
     * Publishes a creation message to the NATS server.
     *
     * @param messageAsJson The message body in JSON format.
     * @param isFunction    Whether the message is function-related.
     * @param clusterId     The cluster ID associated with the message.
     * @param gpuName       The GPU name associated with the message.
     * @param instanceType  The instance type associated with the message.
     * @param logHeader     The log header for the message.
     * @return The result of the message send operation.
     */
    private SendNatsMessageResult sendCreationMessage(
            @NotNull String messageAsJson, boolean isFunction, String clusterId, String gpuName,
            String instanceType, String logHeader) {
        log.info("{}: Sending creation message...", logHeader);
        String stringSubject = getCreateStreamSubject(isFunction, clusterId, gpuName, instanceType);
        SendNatsMessageResult result = publishMessage(messageAsJson, stringSubject);
        log.info("{}: Sending creation message result {}", logHeader, result.toString());
        delayBetweenMessages();
        return result;
    }

    /**
     * Publishes a termination message to the NATS server.
     *
     * @param messageAsJson The message body in JSON format.
     * @param clusterId     The cluster ID associated with the message.
     * @param logHeader     The log header for the message.
     * @return The result of the message send operation.
     */
    private SendNatsMessageResult sendTerminationMessage(
            @NotNull String messageAsJson, String clusterId, String logHeader) {
        log.info("{}: Sending termination message...", logHeader);
        String stringSubject = getTerminateStreamSubject(clusterId);
        SendNatsMessageResult result = publishMessage(messageAsJson, stringSubject);
        log.info("{}: Sending termination message result {}", logHeader, result.toString());
        delayBetweenMessages();
        return result;
    }

    /**
     * Publishes a message to the NATS server.
     *
     * @param messageBody The message body in JSON format.
     * @param subject     The NATS subject to publish the message to.
     * @return The result of the message send operation.
     */
    SendNatsMessageResult publishMessage(
            @NotNull String messageBody, @NotNull String subject) {
        try {
            JetStream js = fixedNatsPool.borrowJetStream();
            js.publish(NatsMessage.builder()
                               .subject(subject)
                               .data(messageBody, StandardCharsets.UTF_8)
                               .build());
            return SendNatsMessageResult.SUCCESS;
        } catch (IOException e) {
            log.error("Error sending message to NATS: {}", e.getMessage(), e);
            if (e.getMessage() != null
                    && e.getMessage().contains("503 No Responders Available For Request")) {
                return SendNatsMessageResult.NO_RESPONDERS;
            } else {
                return SendNatsMessageResult.FAILURE;
            }
        } catch (Exception e) {
            log.error("Error sending message to NATS: {}", e.getMessage(), e);
        }
        return SendNatsMessageResult.FAILURE;
    }

    private void requireSuccessfulSend(SendNatsMessageResult result, String clusterId) {
        if (result != SendNatsMessageResult.SUCCESS) {
            var msg = String.format(MESG_FAILED_TO_PUBLISH, clusterId, result);
            log.error(msg);
            throw new IcmsInternalServerException(msg);
        }
    }

    /**
     * Constructs the log header for a function-related message.
     *
     * @param message   The ByocFunctionSqsMessageModel message.
     * @param clusterId The cluster ID associated with the message.
     * @return The log header string.
     */
    private String getLogHeaderForFunction(@NotNull ByocSqsMessageModel message, String clusterId) {
        return String.format("RequestId %s, FunctionId %s, ClusterId %s, GPU %s, instanceType %s: ",
                             message.getRequestId(),
                             message.getFunctionId(),
                             clusterId,
                             message.getLaunchSpecification().getGpuName(),
                             message.getInstanceType());
    }

    /**
     * Constructs the log header for a task-related message.
     *
     * @param message   The ByocTaskSqsMessageModel message.
     * @param clusterId The cluster ID associated with the message.
     * @return The log header string.
     */
    private String getLogHeaderForTask(@NotNull ByocSqsMessageModel message, String clusterId) {
        return String.format("RequestId %s, TaskId %s, ClusterId %s, GPU %s, instanceType %s: ",
                             message.getRequestId(),
                             message.getTaskDetails().getTaskId(),
                             clusterId,
                             message.getLaunchSpecification().getGpuType(),
                             message.getInstanceType());
    }

    /**
     * Constructs the log header for a termination-related message.
     *
     * @param message   The ByocTerminatePodMessageModel message.
     * @param clusterId The cluster ID associated with the message.
     * @return The log header string.
     */
    private String getLogHeader(@NotNull ByocTerminatePodMessageModel message, String clusterId) {
        String instanceIds = "";
        if (message.getInstanceIds() == null) {
            log.error("List of instance IDs is null");
            instanceIds = "NULL LIST";
        } else {
            StringBuilder sb = new StringBuilder();
            for (String s : message.getInstanceIds()) {
                sb.append(s);
                sb.append(", ");
            }
            instanceIds = sb.toString();
        }
        return String.format("RequestId %s, ClusterId %s, InstanceId %s: ",
                             message.getRequestId(),
                             clusterId,
                             instanceIds);
    }

    /**
     * Constructs the subject for creation messages.
     *
     * @param isFunction   Whether the message is function-related.
     * @param clusterId    The cluster ID associated with the message.
     * @param gpuName      The GPU name associated with the message.
     * @param instanceType The instance type associated with the message.
     * @return The subject string.
     */
    private String getCreateStreamSubject(
            boolean isFunction, String clusterId, String gpuName, String instanceType) {
        return String.format("Create.NVCA.%s.%s.%s.%s",
                             isFunction ? FUNCTION : TASK,
                             clusterId,
                             gpuName,
                             getSanitizedInstanceId(instanceType));
    }

    /**
     * Sanitizes the instance type by replacing dots with pipes.
     *
     * @param instanceType The instance type string.
     * @return The sanitized instance type string.
     */
    private String getSanitizedInstanceId(String instanceType) {
        return instanceType.replace(".", "|");
    }

    /**
     * Constructs the subject for termination messages.
     *
     * @param clusterId The cluster ID associated with the message.
     * @return The subject string.
     */
    private String getTerminateStreamSubject(String clusterId) {
        return String.format("Terminate.NVCA.%s", clusterId);
    }

    /**
     * Introduces a delay between sending messages, as configured in NatsConfigurationProperties.
     */
    void delayBetweenMessages() {
        try {
            Thread.sleep(natsConfigurationProperties.getDelayBetweenMessages().toMillis());
        } catch (InterruptedException e) {
            log.error("Exception while sleeping", e);
        }
    }

    private boolean checkIfNatsEnabled() {
        if (natsConfigurationProperties.isNatsEnabled()) {
            return true;
        }
        else {
            log.warn("NATS is not enabled, message is not sent");
            return false;
        }
    }
}
