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

package com.nvidia.apikeys.persistance.dao;

import com.nvidia.apikeys.config.exceptions.CassandraException;
import com.nvidia.apikeys.persistance.models.KeyOperationModel;
import com.nvidia.apikeys.persistance.repositories.KeyOperationRepository;
import com.nvidia.apikeys.vo.KeyOperationStatus;
import java.time.Clock;
import java.time.Instant;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import org.springframework.data.cassandra.core.CassandraTemplate;
import org.springframework.data.cassandra.core.InsertOptions;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class KeyOperationsDao {

    private static final InsertOptions IF_NOT_EXISTS = InsertOptions.builder()
            .withIfNotExists()
            .build();

    private final KeyOperationRepository repository;
    private final CassandraTemplate cassandraTemplate;
    private final Clock clock;

    /**
     * Inserts a new operation. Assigns an id, PENDING status, and zero counters when unset.
     */
    public KeyOperationModel create(KeyOperationModel operation) {
        Instant now = clock.instant();
        KeyOperationModel model = operation.toBuilder()
                .operationId(Optional.ofNullable(operation.getOperationId())
                                     .orElseGet(UUID::randomUUID))
                .operationStatus(Optional.ofNullable(operation.getOperationStatus())
                                         .orElse(KeyOperationStatus.PENDING))
                .matchedCount(zeroIfNull(operation.getMatchedCount()))
                .completedCount(zeroIfNull(operation.getCompletedCount()))
                .failedCount(zeroIfNull(operation.getFailedCount()))
                .createdAt(now)
                .updatedAt(now)
                .build();

        if (!cassandraTemplate.insert(model, IF_NOT_EXISTS).wasApplied()) {
            throw new CassandraException("Key operation already exists: " + model.getOperationId());
        }
        return model;
    }

    public Optional<KeyOperationModel> get(UUID operationId) {
        return repository.findByOperationId(operationId);
    }

    /**
     * Writes progress for an existing operation and refreshes updated_at.
     */
    public KeyOperationModel update(KeyOperationModel operation) {
        return repository.save(operation.toBuilder()
                                       .updatedAt(clock.instant())
                                       .build());
    }

    private static Long zeroIfNull(Long value) {
        return value == null ? 0L : value;
    }
}
