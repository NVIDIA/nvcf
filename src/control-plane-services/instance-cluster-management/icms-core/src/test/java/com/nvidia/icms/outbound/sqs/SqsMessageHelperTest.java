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

import static com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction.TERMINATE_INSTANCES;
import static com.nvidia.icms.util.TestUtil.DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_RUNNING_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ZONE;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.params.provider.Arguments.arguments;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.when;

import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.cfg.DateTimeFeature;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.exception.SqsMessageSenderClientException;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocTerminatePodMessageModel;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;

import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.platform.commons.util.StringUtils;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class SqsMessageHelperTest {

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    private SqsMessageHelper sqsMessageHelper;

    @BeforeEach
    public  void beforeEach(){
        sqsMessageHelper =
                new SqsMessageHelper(getMockedObjectMapper(), icmsConfigurationProperties);
    }

    private ObjectMapper getMockedObjectMapper(){
        return JsonMapper.builder()
                .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
                .disable(DateTimeFeature.WRITE_DATES_AS_TIMESTAMPS)
                .build();
    }

    @Test
    void generateSendMessageBatchRequestEntryList_withValidInputs_returnsSuccess() {
        // Prepare
        var byocTerminateMessage = ByocTerminatePodMessageModel.builder()
                .action(TERMINATE_INSTANCES.getRequestAction()).availabilityZone(DUMMY_ZONE)
                .requestId(DUMMY_OPEN_REQUEST_HAVING_INSTANCE_ID)
                .instanceIds(Set.of(DUMMY_RUNNING_INSTANCE_ID))
                .ncaId("dummy-nac-id").build();
        var models = List.of(byocTerminateMessage);

        mockDefaultSqsParameters();
        when(icmsConfigurationProperties.getSqsBatchSize()).thenReturn(5);

        // Act
        var listOfListOfBatchRequestEntity =
                sqsMessageHelper.generateSendMessageBatchRequestEntryList(models, "dummy-prefix");

        // Assert
        assertNotNull(listOfListOfBatchRequestEntity);
        assertNotEquals(true, listOfListOfBatchRequestEntity.isEmpty());
        var listOfBatchRequestEntity = listOfListOfBatchRequestEntity.get(0);

        assertNotEquals(true, listOfBatchRequestEntity.isEmpty());

        var batchRequestEntity = listOfBatchRequestEntity.get(0);

        assertTrue(StringUtils.isNotBlank(batchRequestEntity.getMessageBody()));
        assertTrue(StringUtils.isNotBlank(batchRequestEntity.getMessageDeduplicationId()));
        assertTrue(StringUtils.isNotBlank(batchRequestEntity.getMessageGroupId()));
        assertTrue(StringUtils.isNotBlank(batchRequestEntity.getId()));
        assertEquals("{\"requestId\":\"dummy_open_request_having_instances_id\",\"ncaId\":\"dummy-nac-id\",\"action\":\"TerminateInstances\",\"instanceIds\":[\"dummy_running_instance_id\"],\"availabilityZone\":\"dummy_zone\"}",
                batchRequestEntity.getMessageBody());
    }

    private static Stream<Arguments> getMessageCountTestParameters() {
        return Stream.of(
                arguments(3,  150, 1),
                arguments(3, 260, 2),
                arguments(3, 350, 3)
        );
    }

    @ParameterizedTest
    @MethodSource("getMessageCountTestParameters")
    void getMaxMessagesInSqsBatch_ok (int batchMaxCount, int batchMaxSizeBytes, int expectedMessageCount) {
        // Note: minimal serialized size of an empty ByocSqsMessageModel is 105 bytes!
        //arrange

        when(icmsConfigurationProperties.getSqsBatchSize()).thenReturn(batchMaxCount);
        when(icmsConfigurationProperties.getSqsBatchMaxSizeInBytes()).thenReturn(batchMaxSizeBytes);

        //act
        int count = sqsMessageHelper.getMaxMessagesInSqsBatch(new ByocSqsMessageModel());

        //assert
        Assertions.assertEquals(expectedMessageCount, count);
    }

    @Test
    void getMaxMessagesInSqsBatch_null_message_exception () {
        //arrange

        //act
        SqsMessageSenderClientException exception =
                Assertions.assertThrows(SqsMessageSenderClientException.class, () -> {
                    sqsMessageHelper.getMaxMessagesInSqsBatch(null);
                });

        //assert
        Assertions.assertTrue(exception.getMessage().contains("SQS message is null"));
    }

    @Test
    void getMaxMessagesInSqsBatch_long_message_exception () {
        // Note: minimal serialized size of an empty ByocSqsMessageModel is 105 bytes!
        //arrange
        when(icmsConfigurationProperties.getSqsBatchMaxSizeInBytes()).thenReturn(10); // less than message size

        //act
        SqsMessageSenderClientException exception =
                Assertions.assertThrows(SqsMessageSenderClientException.class, () -> {
                    sqsMessageHelper.getMaxMessagesInSqsBatch(new ByocSqsMessageModel());
                });

        //assert
        Assertions.assertTrue(exception.getMessage().contains("is bigger than allowed by SQS"));
    }

    private void mockDefaultSqsParameters() {
        when(icmsConfigurationProperties.getSqsBatchSize()).thenReturn(10);
        when(icmsConfigurationProperties.getSqsBatchMaxSizeInBytes()).thenReturn(255*1024);
    }
}