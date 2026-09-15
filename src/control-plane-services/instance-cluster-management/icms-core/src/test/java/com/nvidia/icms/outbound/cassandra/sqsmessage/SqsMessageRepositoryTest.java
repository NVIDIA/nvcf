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
package com.nvidia.icms.outbound.cassandra.sqsmessage;


import static com.nvidia.icms.util.TestUtil.DUMMY_MESSAGE_BATCH_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageKey;
import java.util.Optional;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;

class SqsMessageRepositoryTest extends IntegrationTest {

    @Autowired
    private SqsMessageRepository sqsMessageRepository;

    @Test
    void insert() {
        SqsMessageEntity sqsMessageEntity =
                SqsMessageEntity.builder()
                        .key(SqsMessageKey.builder()
                                     .messageBatchId(DUMMY_MESSAGE_BATCH_ID)
                                     .requestId(DUMMY_REQUEST_ID)
                                     .build())
                        .acknowledgedInstances(2)
                        .build();

        SqsMessageEntity sqsMessageEntity1 =
                SqsMessageEntity.builder()
                        .key(SqsMessageKey.builder()
                                     .messageBatchId(DUMMY_MESSAGE_BATCH_ID + "_1")
                                     .requestId(DUMMY_REQUEST_ID)
                                     .build())
                        .acknowledgedInstances(2)
                        .build();

        sqsMessageRepository.insert(sqsMessageEntity);
        sqsMessageRepository.insert(sqsMessageEntity1);

        // Find by request-id and message-batch-id
        Optional<SqsMessageEntity>
                optionalSqsMessageByRequestAndMessageId =
                sqsMessageRepository.findByRequestIdAndMessageBatchId(DUMMY_REQUEST_ID,
                                                                      DUMMY_MESSAGE_BATCH_ID);

        // Assert
        assertTrue(optionalSqsMessageByRequestAndMessageId.isPresent());
        SqsMessageEntity response1 =
                optionalSqsMessageByRequestAndMessageId.get();
        assertEquals(DUMMY_REQUEST_ID, response1.getKey().getRequestId());
        assertEquals(DUMMY_MESSAGE_BATCH_ID, response1.getKey().getMessageBatchId());
        assertEquals(2, response1.getAcknowledgedInstances());
    }

    @Test
    void findByRequestIdAndMessageBatchId_requestIdNotFound() {

        Optional<SqsMessageEntity> response =
                sqsMessageRepository.findByRequestIdAndMessageBatchId("req1", "messageId1");

        // Assert
        assertFalse(response.isPresent());
    }

    @Test
    void insert_sameRequestAndMessageId_throwsException() {
        SqsMessageEntity sqsMessageEntity =
                SqsMessageEntity.builder()
                        .key(SqsMessageKey.builder()
                                     .messageBatchId(DUMMY_MESSAGE_BATCH_ID)
                                     .requestId(DUMMY_REQUEST_ID)
                                     .build())
                        .acknowledgedInstances(2)
                        .build();

        sqsMessageRepository.insert(sqsMessageEntity);

        // Find by request-id
        Optional<SqsMessageEntity> response =
                sqsMessageRepository.findByRequestIdAndMessageBatchId(DUMMY_REQUEST_ID,
                                                                      DUMMY_MESSAGE_BATCH_ID);

        // Assert
        assertTrue(response.isPresent());
        SqsMessageEntity entity = response.get();
        assertEquals(DUMMY_REQUEST_ID, entity.getKey().getRequestId());
        assertEquals(DUMMY_MESSAGE_BATCH_ID, entity.getKey().getMessageBatchId());
        assertEquals(2, entity.getAcknowledgedInstances());

        IcmsConflictException icmsConflictException =
                Assertions.assertThrows(IcmsConflictException.class, () -> {
                    sqsMessageRepository.insert(sqsMessageEntity);
                });

        assertEquals(
                "MessageBatch with dummy_request_id request-id and dummy_message_batch_id messageBatchId already exists",
                icmsConflictException.getBody().getDetail());
    }
}