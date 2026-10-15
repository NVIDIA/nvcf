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
package com.nvidia.icms.outbound.sqs;


import com.amazonaws.services.sqs.AmazonSQS;
import com.amazonaws.services.sqs.model.BatchResultErrorEntry;
import com.amazonaws.services.sqs.model.SendMessageBatchRequestEntry;
import com.amazonaws.services.sqs.model.SendMessageBatchResult;
import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.exception.SqsMessageSenderClientException;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;

import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.List;

import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;

@Slf4j
@Component
@AllArgsConstructor
public class SqsMessageSenderClient {

    private final AmazonSQS client;

    private final ObjectMapper objectMapper;

    private final SqsMessageHelper sqsMessageHelper;

    private final TelemetryEventClient telemetryEventClient;

    public void sendSqsMessages(
            @NotNull String queueUrl,
            @Nullable List<?> messageModels,
            @NotNull String messageBodyPrefix) {

        try {
            sendBatchMessagesToQueue(queueUrl,
                                     sqsMessageHelper.generateSendMessageBatchRequestEntryList(
                                             messageModels, messageBodyPrefix));
        } catch (IcmsInternalServerException icmsInternalServerException) {
            log.error("Failed to send SQS message to queue: {}, internalServerError, error {}",
                    queueUrl, icmsInternalServerException.getBody().getDetail(),
                    icmsInternalServerException);
            sendSqsMessagingFailedEvent(icmsInternalServerException.getBody().getDetail());

            // rethrowing caught exception
            throw icmsInternalServerException;

        } catch (Exception exception) {

            String err = String.format("Failed to send SQS message to queue: %s, error: %s", queueUrl, exception.getMessage());
            log.error("error: {}, exception:", err, exception);
            sendSqsMessagingFailedEvent(exception.getMessage());

            throw new IcmsInternalServerException(err);
        }
    }

    private void sendBatchMessagesToQueue(
            @NotNull String queueUrl,
            @NotNull List<List<SendMessageBatchRequestEntry>> sendMessageBatchRequestEntryList) {

        sendMessageBatchRequestEntryList.forEach(sendMessageBatchRequestEntries -> {

            // The response will contain list of failed and successful request
            SendMessageBatchResult response =
                    client.sendMessageBatch(queueUrl, sendMessageBatchRequestEntries);

            if (response.getFailed() != null && !response.getFailed().isEmpty()) {
                log.error("SQS Batch send operation failed - {}",
                        getSqsFailedMessageAsString(response.getFailed()));
            }

            // If at least one batch operation is successful, marking the request as successful
            if (response.getSuccessful() == null || response.getSuccessful().isEmpty()) {
                String errMsg = String.format("Failed to send all messages from a batch %s",
                        getSqsFailedMessageAsString(response.getFailed()));
                log.error(errMsg);
                throw new SqsMessageSenderClientException(errMsg);
            }
        });
    }

    private String getSqsFailedMessageAsString(List<BatchResultErrorEntry> failedMessages) {
        try {
            return objectMapper.writeValueAsString(failedMessages);
        } catch (Exception exception) {
            log.error("Failed to convert failure message from SQS to string, original, error: {}, original message: {}",
                    exception.getMessage(), failedMessages);
        }

        // returning null for failure case
        return null;
    }

    private void sendSqsMessagingFailedEvent(String errorMsg) {
        GenericMetric genericMetric = new GenericMetric()
                .withEventName(Events.SQS_MESSAGING_FAILED.toString())
                .withError(errorMsg);
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }
}
