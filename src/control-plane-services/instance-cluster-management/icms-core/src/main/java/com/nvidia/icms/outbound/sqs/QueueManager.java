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
import com.amazonaws.services.sqs.model.CreateQueueRequest;
import com.amazonaws.services.sqs.model.CreateQueueResult;
import com.amazonaws.services.sqs.model.DeleteQueueRequest;
import com.amazonaws.services.sqs.model.DeleteQueueResult;
import com.amazonaws.services.sqs.model.GetQueueAttributesRequest;
import com.amazonaws.services.sqs.model.GetQueueAttributesResult;
import com.amazonaws.services.sqs.model.GetQueueUrlResult;
import com.amazonaws.services.sqs.model.QueueDeletedRecentlyException;
import com.amazonaws.services.sqs.model.QueueDoesNotExistException;
import com.amazonaws.services.sqs.model.SetQueueAttributesRequest;
import com.nvidia.icms.configuration.aws.AwsQueueProperties;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.micrometer.observation.annotation.Observed;
import jakarta.annotation.Nullable;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Component;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

import static com.nvidia.icms.configuration.aws.AwsConfiguration.AWS_SQS_CLIENT_QUALIFIER;

@Component
@Slf4j
public class QueueManager {

    private final AmazonSQS sqsClient;
    private final AwsQueueProperties awsQueueProperties;
    private final TelemetryEventClient telemetryEventClient;


    public QueueManager(
            @Qualifier(AWS_SQS_CLIENT_QUALIFIER) AmazonSQS sqsClient,
            AwsQueueProperties awsQueueProperties, TelemetryEventClient telemetryEventClient) {
        this.awsQueueProperties = awsQueueProperties;
        this.sqsClient = sqsClient;
        this.telemetryEventClient = telemetryEventClient;
    }

    public boolean queueExists(@Nullable String queueName) {
        try {
            if (StringUtils.isBlank(queueName)) {
                log.warn("Provided SQS queue is null");
                return false;
            }

            GetQueueUrlResult result = sqsClient.getQueueUrl(queueName);
            log.debug("Queue with name {} exists and has the url {}", queueName,
                      result.getQueueUrl());

            return true;
        } catch (QueueDoesNotExistException queueDoesNotExistException) {
            // When QueueDoesNotExistException exception is thrown it means queue doesn't exist
            return false;

        } catch (Exception e) {
            log.error("queueExists, failed to check existence of queue, queueName: {}, error: {}, exception: ", queueName,
                     e.getMessage(), e);
            throw e;
        }
    }

    // TODO: Accept clusterId in this function add it in log messages, it will help in debugging
    public String createQueue(String queueName, Map<String, String> queueAttributes) {

        String errorMsg = String.format("Failed to create queue %s", queueName);
        try {
            CreateQueueResult result = sqsClient.createQueue(
                    new CreateQueueRequest()
                            .withQueueName(queueName)
                            .withAttributes(queueAttributes)
                            .withTags(awsQueueProperties.getQueueTags()));
            String queueUrl = result.getQueueUrl();
            log.info("createQueue: Created queue with name {}, with the queue url {}", queueName, queueUrl);
            return queueUrl;

        } catch (QueueDeletedRecentlyException e) {
            String detailedErrorMsg = String.format("%s, error %s ", errorMsg, e.getMessage());
            log.error("createQueue: queueName: {}, This queue has recently deleted, error: {}, exception: ", queueName, detailedErrorMsg, e);
            throw new QueueDeletedRecentlyException(detailedErrorMsg);

        } catch (Exception e) {
            String detailedErrorMsg = String.format("%s, error: %s", errorMsg, e.getMessage());
            log.error("createQueue: queueName: {}, error: {},", queueName, detailedErrorMsg, e);
            throw new IcmsInternalServerException(detailedErrorMsg, e);

        }
    }

    // TODO: Accept clusterId in this function add it in log messages, it will help in debugging
    public void deleteQueue(String queueUrl) {
        try {
            DeleteQueueRequest deleteQueueRequest = new DeleteQueueRequest();
            deleteQueueRequest.setQueueUrl(queueUrl);
            DeleteQueueResult deleteQueueResult = sqsClient.deleteQueue(deleteQueueRequest);
            log.info("deleteQueue: Deleted queue with url {}, output {}", queueUrl,
                     deleteQueueResult.toString());
        } catch (QueueDoesNotExistException queueDoesNotExistException) {
            log.warn(
                    "deleteQueue: Queue with URL {} doesn't exist hence not able to delete queue, error - {} exception - ",
                    queueUrl, queueDoesNotExistException.getErrorMessage(),
                    queueDoesNotExistException);
        } catch (Exception exception) {
            String errMsg =
                    String.format("Failed to delete Queue with URL %s, error - %s", queueUrl,
                            exception.getMessage());
            log.error("deleteQueue: queueUrl: {}, error: {}, exception: ", queueUrl, errMsg, exception);
            throw new IcmsInternalServerException(errMsg, exception);
        }
    }


    public boolean isQueueAttributesUpdateNeeded(@Nullable String queueUrl, Map<String, String> queueAttributesNeeded) {
        try {
            Map<String, String> existingQueueAttributes = getExistingQueue(queueUrl);

            if (queueAttributesNeeded != null && !queueAttributesNeeded.equals(
                    existingQueueAttributes)) {
                log.info(
                        "Queue attributes update needed. Old existing attributes: {}, attributes needed: {}",
                        existingQueueAttributes,
                        queueAttributesNeeded);
                return true;
            }
        } catch (Exception e) {
            log.error(
                    "Failed to retrieve the queue attributes for queue with url {} to ensure if the attributes needs update. Exception message {}",
                    queueUrl,
                    e.getMessage(),
                    e);
        }
        return false;
    }

    public void updateQueueAttributes(@Nullable String queueUrl, Map<String, String> queueAttributes) {
        try {
            sqsClient.setQueueAttributes(
                    new SetQueueAttributesRequest()
                            .withQueueUrl(queueUrl)
                            .withAttributes(queueAttributes));
        } catch (Exception e) {
            log.error(
                    "Failed to update the queue attributes for queue with url {}, error: {}, exception: ",
                    queueUrl,
                    e.getMessage(),
                    e);

            Map<String, Object> metadata = new HashMap<>();
            metadata.put(EventMetaData.QUEUE_URL.getName(), queueUrl);
            telemetryEventClient.triggerEvent(List.of(new GenericMetric()
                                                               .withError(e.getMessage())
                                                               .withMetadata(metadata)
                                                               .withEventName(
                                                                       Events.SQS_QUEUE_ATTRIBUTE_UPDATE_FAILED.toString())));
        }
    }

    private Map<String, String> getExistingQueue(@Nullable String queueUrl) {
        try {
            GetQueueAttributesResult result = sqsClient.getQueueAttributes(
                    new GetQueueAttributesRequest()
                            .withQueueUrl(queueUrl)
                            .withAttributeNames(awsQueueProperties.getQueueAttributes().keySet()));
            return result.getAttributes();
        } catch (Exception e) {
            log.error(
                    "Failed to retrieve the queue attributes for queue with url {}. Exception message {}",
                    queueUrl,
                    e.getMessage());
        }
        return Map.of();
    }

    /**
     * Retrieves the URL of an Amazon SQS queue based on its name.
     *
     * @param queueName                The name of the SQS queue. Can be {@code null}.
     * @param throwExceptionIfNotExist Flag indicating whether to throw an exception
     *                                 if the queue does not exist.
     * @return The URL of the SQS queue as a {@code String}, or {@code null} if the
     *         queue does not exist and {@code throwExceptionIfNotExist} is set to {@code false}.
     * @throws IcmsInternalServerException If {@code throwExceptionIfNotExist} is {@code true} and the
     *                                    queue does not exist.
     * @throws IllegalStateException      If an error occurs while invoking Amazon SQS to retrieve the queue URL.
     *
     * This method first checks if the queue exists using {@link #queueExists(String)}.
     * If the queue does not exist and {@code throwExceptionIfNotExist} is {@code true},
     * it throws an exception of type {@link IcmsInternalServerException}.
     */
    @Observed
    public @Nullable String getQueueUrl(@Nullable String queueName, boolean throwExceptionIfNotExist) {
        if (!queueExists(queueName)) {
            String errMsg = String.format("SQS Queue %s does not exist", queueName);
            log.error(errMsg);
            if (throwExceptionIfNotExist) {
                throw new IcmsInternalServerException(errMsg);
            } else {
                return null;
            }
        }
        try {
            GetQueueUrlResult result = sqsClient.getQueueUrl(queueName);
            return result.getQueueUrl();
        } catch (Exception e) {
            String errMsg = String.format("Failed to get URL for SQS Queue %s. Reason: %s", queueName, e.getMessage());
            log.error(errMsg, e);
            throw new IllegalStateException(errMsg, e);
        }
    }

}
