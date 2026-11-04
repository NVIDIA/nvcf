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

import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.outbound.cassandra.sqsmessage.entity.SqsMessageEntity;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.List;
import java.util.Optional;

import io.micrometer.observation.annotation.Observed;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Repository;

@Slf4j
@Repository
@AllArgsConstructor
public class SqsMessageRepository {

    private final SqsMessageRepo sqsMessageRepo;


    @Observed
    public void insert(SqsMessageEntity entity) {

        if (entity.getCreationTime() == null) {
            entity.setCreationTime(Instant.now());
        }

        try {
            if (!sqsMessageRepo.isInsertedIfNotExists(entity)) {
                log.error("requestId {} and messageBatchId {} already exists",
                        entity.getKey().getRequestId(), entity.getKey().getMessageBatchId());

                // Calling function should catch this error and take appropriate action
                throw new IcmsConflictException(String.format(
                        "MessageBatch with %s request-id and %s messageBatchId already exists",
                        entity.getKey().getRequestId(), entity.getKey().getMessageBatchId()));
            }
        } catch (IcmsConflictException icmsConflictException) {
            // Rethrowing same exception
            throw icmsConflictException;

        } catch (Exception exception) {
            log.error(
                    "class:SqsMessageRepository function: insert, failed insert entity, requestId {}, messageBatchId {}, error - {}",
                    entity.getKey().getRequestId(), entity.getKey().getMessageBatchId(), exception.getMessage(),
                    exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to insert sqsMessageEntity, error: %s",
                            exception.getMessage()), exception);
        }
    }

    @Observed
    public Optional<SqsMessageEntity> findByRequestIdAndMessageBatchId(
            String requestId, String messageBatchId) {
        try {
            return sqsMessageRepo.findByKeyRequestIdAndKeyMessageBatchId(requestId, messageBatchId);

        } catch (Exception exception) {
            log.error(
                    "class:SqsMessageRepository function: findByRequestIdAndMessageBatchId, failed fetch entity, requestId {}, messageBatchId {}, error - {}",
                    requestId, messageBatchId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch sqsMessageEntity, error: %s",
                            exception.getMessage()), exception);
        }
    }

    @Observed
    public List<SqsMessageEntity> findByRequestId(String requestId) {
        try {
            return sqsMessageRepo.findAllByKeyRequestId(requestId);
        } catch (Exception exception) {
            log.error(
                    "class:SqsMessageRepository function: findByRequestId, failed fetch entity, requestId {}, error - {}",
                    requestId, exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to fetch sqsMessageEntities, error: %s",
                            exception.getMessage()), exception);
        }
    }

    @Observed
    public void update(@NotNull SqsMessageEntity sqsMessageEntity) {
        try {
            sqsMessageRepo.update(sqsMessageEntity);
        } catch (Exception exception) {
            log.error(
                    "class:SqsMessageRepository function: update, failed update entity, requestId {}, messageBatchId {}, error - {}",
                    sqsMessageEntity.getKey().getRequestId(), sqsMessageEntity.getKey().getMessageBatchId(),
                    exception.getMessage(), exception);
            throw new IcmsInternalServerException(
                    String.format("Failed to update sqsMessageEntity, error: %s",
                            exception.getMessage()), exception);
        }
    }
}
