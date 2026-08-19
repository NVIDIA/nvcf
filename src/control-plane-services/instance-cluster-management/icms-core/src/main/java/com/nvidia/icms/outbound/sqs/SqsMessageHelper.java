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

import static com.nvidia.icms.util.InstanceServiceUtil.findStringSizeInKb;
import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;

import com.amazonaws.services.sqs.model.SendMessageBatchRequestEntry;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.ArrayList;
import java.util.List;

import com.nvidia.icms.outbound.exception.SqsMessageSenderClientException;
import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@AllArgsConstructor
public class SqsMessageHelper {


    private final ObjectMapper objectMapper;
    private final IcmsConfigurationProperties icmsConfigurationProperties;

    public List<List<SendMessageBatchRequestEntry>> generateSendMessageBatchRequestEntryList(
            @Nullable List<?> messageModels,
            @NotNull String messageBodyPrefix) {
        List<List<SendMessageBatchRequestEntry>> lists = new ArrayList<>();

        if (messageModels == null || messageModels.isEmpty()) {
            return new ArrayList<>();
        }

        int batchCount = getMaxMessagesInSqsBatch(messageModels.getFirst());
        log.info("Packing message to SQS in batches, batchSize {}", batchCount);

        int size = messageModels.size();
        for (int i = 0; i < size; i = i + batchCount) {
            lists.add(generateSendMessageBatchRequestEntry(
                    messageModels.subList(i, Math.min(i + batchCount, size)),
                    messageBodyPrefix));
        }
        return lists;
    }

    private List<SendMessageBatchRequestEntry> generateSendMessageBatchRequestEntry(
            @NotNull List<?> messageModels,
            @NotNull String messageBodyPrefix) {

        List<SendMessageBatchRequestEntry> sendMessageBatchRequestEntries = new ArrayList<>();
        messageModels.forEach(message -> {
            String uuid = generateRandomUUID();
            SendMessageBatchRequestEntry sendMessageBatchRequestEntry =
                    null;
            try {
                String messageBody = objectMapper.writeValueAsString(message);
                sendMessageBatchRequestEntry = new SendMessageBatchRequestEntry()
                        // id – An identifier for a message in this batch used to communicate the result.
                        .withId(uuid)
                        .withMessageGroupId(String.format("%s-%s", messageBodyPrefix, uuid))
                        .withMessageDeduplicationId(uuid)
                        .withMessageBody(messageBody)
                        .withDelaySeconds(0);
            } catch (JacksonException e) {
                String errorMsg = String.format(
                        "Json Processing exception when forming SQS batch entry for BYOC instance request, error: %s",
                        e.getMessage());
                log.error(errorMsg, e);
                throw new IcmsInternalServerException(errorMsg, e);
            }

            sendMessageBatchRequestEntries.add(sendMessageBatchRequestEntry);
        });

        return sendMessageBatchRequestEntries;
    }

    int getMessageSize(Object message) {
        return objectMapper.writeValueAsString(message).getBytes().length;
    }

    /**
     * @param message Message we will put in SQS queue
     * @return Max number of messages we can put in single SQS batch
     */
    @Observed
    @VisibleForTesting
    int getMaxMessagesInSqsBatch(@Nullable Object message) {

        // validating message
        if (message == null) {
            String errorMessage = "SQS message is null";
            log.error(errorMessage);
            throw new SqsMessageSenderClientException(errorMessage);
        }

        try {

            // validating message size, should be more than 0
            int messageSizeInBytes = getMessageSize(message);
            if (messageSizeInBytes == 0) {
                String errorMessage = "SQS message is empty";
                log.error(errorMessage);
                throw new SqsMessageSenderClientException(errorMessage);
            }

            // Finding max allowed messages for given message size
            int maxMessageNumberBySize =
                    icmsConfigurationProperties.getSqsBatchMaxSizeInBytes() /
                            messageSizeInBytes;

            if (maxMessageNumberBySize == 0) {
                String errorMessage = String.format(
                        "Single message size ( %d bytes ) is bigger than allowed by SQS (%d bytes)",
                        messageSizeInBytes,
                        icmsConfigurationProperties.getSqsBatchMaxSizeInBytes());

                log.error(errorMessage);
                throw new SqsMessageSenderClientException(errorMessage);
            }

            int messagesInBatchCount = Math.min(maxMessageNumberBySize,
                    icmsConfigurationProperties.getSqsBatchSize());
            log.info(
                    "Number of messages per batch {}, messageSizeInBytes={}, maxMessageNumberBySize={}, getSqsBatchMaxSizeInBytes={}, getSqsBatchSize={}",
                    messagesInBatchCount,
                    messageSizeInBytes,
                    maxMessageNumberBySize,
                    icmsConfigurationProperties.getSqsBatchMaxSizeInBytes(),
                    icmsConfigurationProperties.getSqsBatchSize());
            return messagesInBatchCount;

        } catch (SqsMessageSenderClientException sqsMessageSenderClientException) {

            // rethrowing caught exception
            throw sqsMessageSenderClientException;

            // Catching any exception during process
        } catch (Exception exception) {
            String err = String.format("Failed to find max messages for SQS batch, error: %s",
                    exception.getMessage());
            log.error(err, exception);
            throw new IcmsInternalServerException(err);
        }
    }
}
