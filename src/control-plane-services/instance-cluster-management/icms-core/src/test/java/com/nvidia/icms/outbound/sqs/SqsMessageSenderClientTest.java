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

import static com.nvidia.icms.util.InstanceServiceUtil.generateRandomUUID;
import static com.nvidia.icms.util.TestUtil.DUMMY_CONTAINER_IMAGE;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENVIRONMENT_VALUE;
import static com.nvidia.icms.util.TestUtil.DUMMY_NON_BYOC_INSTANCE_TYPE;
import static com.nvidia.icms.util.TestUtil.DUMMY_GPU_NAME;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.amazonaws.services.sqs.AmazonSQS;
import com.amazonaws.services.sqs.model.AmazonSQSException;
import com.amazonaws.services.sqs.model.BatchResultErrorEntry;
import com.amazonaws.services.sqs.model.SendMessageBatchRequestEntry;
import com.amazonaws.services.sqs.model.SendMessageBatchResult;
import com.amazonaws.services.sqs.model.SendMessageBatchResultEntry;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import java.util.ArrayList;
import java.util.List;

import com.nvidia.icms.service.telemetry.TelemetryEventClient;

import java.util.UUID;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;

import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
public class SqsMessageSenderClientTest {

    @Mock
    private AmazonSQS mockAmazonSQS;

    private SqsMessageSenderClient testClient;

    private ObjectMapper objectMapper;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private SqsMessageHelper sqsMessageHelper;

    @Captor
    private ArgumentCaptor<ArrayList<?>> gotRequestCaptor;

    private final String DUMMY_QUEUE = "dummy_queue_endpoint";

    @BeforeEach
    public void beforeEach() {
        objectMapper = new JsonMapper();
        testClient = new SqsMessageSenderClient(mockAmazonSQS,
                objectMapper,
                sqsMessageHelper,
                telemetryEventClient);
    }

    private List<SendMessageBatchRequestEntry> getDummyListOfSqsBatchRequest(
            String messageBodyPrefix, List<?> messageBody) throws JacksonException {

        String uuid = generateRandomUUID();
        return List.of(new SendMessageBatchRequestEntry()
                // id – An identifier for a message in this batch used to communicate the result.
                .withId(uuid)
                .withMessageGroupId(String.format("%s-%s", messageBodyPrefix, uuid))
                .withMessageDeduplicationId(uuid)
                .withMessageBody(objectMapper.writeValueAsString(messageBody))
                .withDelaySeconds(0));
    }

    @Test
    void sendSqsMessages_success() throws JacksonException {

        // Prepare
        List<ByocSqsMessageModel> messages = List.of(getSqsMessageToSend());
        String prefix = "byoc-create";

        SendMessageBatchResultEntry sendMessageBatchResultEntry =
                new SendMessageBatchResultEntry().withId("dummy_id");

        SendMessageBatchResult sendMessageBatchResult =
                new SendMessageBatchResult().withSuccessful(List.of(sendMessageBatchResultEntry));

        when(sqsMessageHelper.generateSendMessageBatchRequestEntryList(messages,
                prefix)).thenReturn(List.of(getDummyListOfSqsBatchRequest(prefix, messages)));

        when(mockAmazonSQS.sendMessageBatch(eq(DUMMY_QUEUE), any())).thenReturn(
                sendMessageBatchResult);

        // Act
        testClient.sendSqsMessages(DUMMY_QUEUE, messages, prefix);

        // Assert
        verify(mockAmazonSQS).sendMessageBatch(eq(DUMMY_QUEUE), any());
        verify(sqsMessageHelper).generateSendMessageBatchRequestEntryList(messages,
                prefix);
    }

    @Test
    void sendSqsMessages_withSendMessageFailure_throwsException() throws JacksonException {

        // Prepare
        List<ByocSqsMessageModel> messages = List.of(getSqsMessageToSend());
        String prefix = "byoc-create";

        when(sqsMessageHelper.generateSendMessageBatchRequestEntryList(messages,
                prefix)).thenReturn(List.of(getDummyListOfSqsBatchRequest(prefix, messages)));

        when(mockAmazonSQS.sendMessageBatch(eq(DUMMY_QUEUE), any())).thenThrow(
                new AmazonSQSException("Error"));

        // Act
        IcmsInternalServerException exception =
                Assertions.assertThrows(IcmsInternalServerException.class,
                        () -> testClient.sendSqsMessages(
                                DUMMY_QUEUE,
                                messages,
                                prefix));

        // Assert
        Assertions.assertEquals("Failed to send SQS message to queue: dummy_queue_endpoint, error: Error (Service: null; Status Code: 0; Error Code: null; Request ID: null; Proxy: null)",
                exception.getBody().getDetail());
       verify(mockAmazonSQS).sendMessageBatch(eq(DUMMY_QUEUE), any());
    }

    @Test
    void sendSqsMessages_withBatchGenerationFailed_throwsException() throws JacksonException {

        // Prepare
        List<ByocSqsMessageModel> messages = List.of(getSqsMessageToSend());
        String prefix = "byoc-create";

        when(sqsMessageHelper.generateSendMessageBatchRequestEntryList(messages,
                prefix)).thenThrow(new IcmsInternalServerException("dummy_exception"));

        // Act
        IcmsInternalServerException exception =
                Assertions.assertThrows(IcmsInternalServerException.class,
                        () -> testClient.sendSqsMessages(
                                DUMMY_QUEUE,
                                messages,
                                prefix));

        // Assert
        Assertions.assertEquals("dummy_exception", exception.getBody().getDetail());
    }

    @Test
    void sendSqsMessages_withAllMessageInBatchOperationFailed() throws JacksonException {

        // Prepare
        List<ByocSqsMessageModel> messages = List.of(getSqsMessageToSend());
        String prefix = "byoc-create";

        when(sqsMessageHelper.generateSendMessageBatchRequestEntryList(messages,
                prefix)).thenReturn(List.of(getDummyListOfSqsBatchRequest(prefix, messages)));

        BatchResultErrorEntry batchResultErrorEntry =
                new BatchResultErrorEntry().withId("dummy_id");

        SendMessageBatchResult sendMessageBatchResult =
                new SendMessageBatchResult().withFailed(List.of(batchResultErrorEntry));

        when(mockAmazonSQS.sendMessageBatch(eq(DUMMY_QUEUE), any())).thenReturn(
                sendMessageBatchResult);

        // Act
        IcmsInternalServerException exception =
                Assertions.assertThrows(IcmsInternalServerException.class, () -> {
                    testClient.sendSqsMessages(DUMMY_QUEUE, messages, prefix);
                });

        // Assert. Jackson 3 does not guarantee bean property order, so assert on content
        // rather than an exact serialized string.
        String detail = exception.getBody().getDetail();
        Assertions.assertTrue(detail.startsWith("Failed to send SQS message to queue: "
                + "dummy_queue_endpoint, error: Failed to send all messages from a batch ["));
        Assertions.assertTrue(detail.contains("\"id\":\"dummy_id\""));
        Assertions.assertTrue(detail.contains("\"senderFault\":null"));
        Assertions.assertTrue(detail.contains("\"code\":null"));
        Assertions.assertTrue(detail.contains("\"message\":null"));

        verify(mockAmazonSQS).sendMessageBatch(eq(DUMMY_QUEUE), any());
        verify(sqsMessageHelper).generateSendMessageBatchRequestEntryList(messages,
                prefix);
    }

    // In this case the call will be marked as successful
    @Test
    void sendMessageBatch_withSomeMessageInBatchOperationFailedAndMinOneIsSuccessful()
            throws JacksonException {

        // Prepare
        List<ByocSqsMessageModel> messages = List.of(getSqsMessageToSend());
        String prefix = "create-";

        when(sqsMessageHelper.generateSendMessageBatchRequestEntryList(messages,
                prefix)).thenReturn(List.of(getDummyListOfSqsBatchRequest(prefix, messages)));

        SendMessageBatchResultEntry sendMessageBatchResultEntry =
                new SendMessageBatchResultEntry().withId("dummy_id_1");

        BatchResultErrorEntry batchResultErrorEntry =
                new BatchResultErrorEntry().withId("dummy_id_2");

        SendMessageBatchResult sendMessageBatchResult = new SendMessageBatchResult()
                .withFailed(List.of((batchResultErrorEntry)))
                .withSuccessful(List.of(sendMessageBatchResultEntry));

        when(mockAmazonSQS.sendMessageBatch(eq(DUMMY_QUEUE), any())).thenReturn(
                sendMessageBatchResult);

        // Act
        testClient.sendSqsMessages(DUMMY_QUEUE, messages, prefix);

        // Assert
        verify(mockAmazonSQS).sendMessageBatch(eq(DUMMY_QUEUE), any());
        verify(sqsMessageHelper).generateSendMessageBatchRequestEntryList(messages,
                prefix);
    }

    private ByocSqsMessageModel getSqsMessageToSend() {
        return ByocSqsMessageModel.builder()
                .sub("partner-sub")
                .instanceCount(3)
                .launchSpecification(ByocSqsMessageModel.ByocLaunchSpecification.builder()
                        .instanceType(DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .containerImage(DUMMY_CONTAINER_IMAGE)
                        .environment(DUMMY_ENVIRONMENT_VALUE)
                        .instanceTypeName(DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .instanceTypeValue(DUMMY_NON_BYOC_INSTANCE_TYPE)
                        .instanceCount(3)
                        .deploymentId(UUID.randomUUID())
                        .gpuSpecificationId(UUID.randomUUID())
                        .gpuType(DUMMY_GPU_NAME)
                        .build())
                .requestId("dummy-request-id")
                .action(SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES.getRequestAction())
                .build();
    }
}
