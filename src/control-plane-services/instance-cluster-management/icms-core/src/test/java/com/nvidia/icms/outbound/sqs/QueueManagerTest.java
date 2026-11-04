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

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.amazonaws.services.sqs.AmazonSQS;
import com.amazonaws.services.sqs.model.AmazonSQSException;
import com.amazonaws.services.sqs.model.CreateQueueRequest;
import com.amazonaws.services.sqs.model.CreateQueueResult;
import com.amazonaws.services.sqs.model.DeleteQueueRequest;
import com.amazonaws.services.sqs.model.DeleteQueueResult;
import com.amazonaws.services.sqs.model.GetQueueAttributesResult;
import com.amazonaws.services.sqs.model.GetQueueUrlResult;
import com.amazonaws.services.sqs.model.QueueDoesNotExistException;
import com.amazonaws.services.sqs.model.SetQueueAttributesRequest;
import com.nvidia.icms.configuration.aws.AwsQueueProperties;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import java.util.Map;

import com.nvidia.icms.errors.IcmsInternalServerException;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
public class QueueManagerTest {

    private static final String queueName = "gdn-spot-instance-requests-g6.fifo";
    private AmazonSQS mockAmazonSQS;
    private AwsQueueProperties mockAwsQueueProperties;
    private QueueManager queueManager;
    private TelemetryEventClient telemetryEventClient;

    @BeforeEach
    public void beforeEach(
            @Mock AmazonSQS mockAmazonSQS, @Mock
    AwsQueueProperties mockAwsQueueProperties,
            @Mock TelemetryEventClient telemetryEventClient) {
        this.mockAmazonSQS = mockAmazonSQS;
        this.mockAwsQueueProperties = mockAwsQueueProperties;
        this.telemetryEventClient = telemetryEventClient;
        this.queueManager = new QueueManager(mockAmazonSQS, mockAwsQueueProperties, telemetryEventClient);
    }

    @Test
    void queueExists_true() {
        // Prepare
        when(mockAmazonSQS.getQueueUrl(queueName)).thenReturn(
                new GetQueueUrlResult().withQueueUrl("queue_url"));

        // Act
        boolean result = queueManager.queueExists(queueName);

        // Assert
        Assertions.assertTrue(result);
        verify(mockAmazonSQS).getQueueUrl(queueName);
    }

    @Test
    void queueExists_false() {
        // Prepare
        when(mockAmazonSQS.getQueueUrl(queueName)).thenThrow(
                new QueueDoesNotExistException("Error"));

        // Act
        boolean result = queueManager.queueExists(queueName);

        // Assert
        Assertions.assertFalse(result);
        verify(mockAmazonSQS).getQueueUrl(queueName);
    }

    @Test
    void createQueue_success() {
        // Prepare
        when(mockAmazonSQS.createQueue(any(CreateQueueRequest.class))).thenReturn(
                new CreateQueueResult().withQueueUrl("queue_url"));
        when(mockAwsQueueProperties.getQueueTags()).thenReturn(Map.of());

        // Act
        String createdQueueUrl = queueManager.createQueue(queueName, Map.of());

        // Assert
        Assertions.assertEquals("queue_url", createdQueueUrl);
        verify(mockAmazonSQS).createQueue(any(CreateQueueRequest.class));
        verify(mockAwsQueueProperties).getQueueTags();
    }

    @Test
    void createQueue_error() {
        // Prepare
        when(mockAmazonSQS.createQueue(any(CreateQueueRequest.class))).thenThrow(
                new AmazonSQSException("error"));
        when(mockAwsQueueProperties.getQueueTags()).thenReturn(Map.of());

        // Act
        IcmsInternalServerException e = Assertions.assertThrows(IcmsInternalServerException.class,
                                                       () -> queueManager.createQueue(queueName, Map.of()));

        // Assert
        Assertions.assertEquals(
                "Failed to create queue gdn-spot-instance-requests-g6.fifo, error: error (Service: null; Status Code: 0; Error Code: null; Request ID: null; Proxy: null)",
                e.getBody().getDetail());
        verify(mockAmazonSQS).createQueue(any(CreateQueueRequest.class));
        verify(mockAwsQueueProperties).getQueueTags();
    }

    @Test
    void isQueueAttributesUpdateNeeded_true() {
        // Prepare
        Map<String, String> existingQueueAttributes = Map.of("key1", "value1", "key2", "value2");
        Map<String, String> updatedQueueAttributes = Map.of("key1", "value1", "key2", "value3");
        when(mockAmazonSQS.getQueueAttributes(any())).thenReturn(
                new GetQueueAttributesResult().withAttributes(existingQueueAttributes));

        // Act
        boolean result = queueManager.isQueueAttributesUpdateNeeded("queue_url", updatedQueueAttributes);

        // Assert
        Assertions.assertTrue(result);
    }

    @Test
    void isQueueAttributesUpdateNeeded_false() {
        // Prepare
        Map<String, String> existingQueueAttributes = Map.of("key1", "value1", "key2", "value2");
        Map<String, String> updatedQueueAttributes = Map.of("key1", "value1", "key2", "value2");
        when(mockAmazonSQS.getQueueAttributes(any())).thenReturn(
                new GetQueueAttributesResult().withAttributes(existingQueueAttributes));

        // Act
        boolean result = queueManager.isQueueAttributesUpdateNeeded(queueName, updatedQueueAttributes);

        // Assert
        Assertions.assertFalse(result);
        verify(mockAmazonSQS).getQueueAttributes(any());
    }

    @Test
    void updateQueueAttributes() {
        // Prepare
        Map<String, String> queueAttributes = Map.of("key1", "value1", "key2", "value2");

        // Act
        queueManager.updateQueueAttributes("queue_url", queueAttributes);

        // Assert
        verify(mockAmazonSQS).setQueueAttributes(new SetQueueAttributesRequest()
                                                         .withQueueUrl("queue_url")
                                                         .withAttributes(queueAttributes));
    }

    @Test
    void updateQueueAttributes_failure_emitsTelemetryEvent() {
        Map<String, String> queueAttributes = Map.of("key1", "value1");
        doThrow(new AmazonSQSException("sqs error"))
                .when(mockAmazonSQS).setQueueAttributes(any(SetQueueAttributesRequest.class));

        queueManager.updateQueueAttributes("queue_url", queueAttributes);

        verify(telemetryEventClient).triggerEvent(argThat(events ->
                events.size() == 1
                        && "SqsQueueAttributeUpdateFailed".equals(events.get(0).getEventName())));
    }

    @Test
    void deleteQueue_success() {
        // Prepare
        DeleteQueueRequest deleteQueueRequest = new DeleteQueueRequest("queue_url");
        when(mockAmazonSQS.deleteQueue(deleteQueueRequest)).thenReturn(
                new DeleteQueueResult());

        // Act
        queueManager.deleteQueue("queue_url");

        // Assert
        verify(mockAmazonSQS).deleteQueue(deleteQueueRequest);
    }
}
