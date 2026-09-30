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

package com.nvidia.apikeys.persistance.models;

import com.nvidia.apikeys.vo.KeyOperationStatus;
import java.time.Instant;
import java.util.Set;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.Frozen;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

/**
 * Progress record for a bulk key operation. The selection sets are the operation scope. The
 * paging state lets a worker resume key selection after a restart.
 */
@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(KeyOperationModel.TABLE_NAME)
public class KeyOperationModel {

    public static final String TABLE_NAME = "key_operations_by_id";
    public static final String COLUMN_OPERATION_ID = "operation_id";
    public static final String COLUMN_ACTOR_TYPE = "actor_type";
    public static final String COLUMN_ACTOR_ID = "actor_id";
    public static final String COLUMN_OPERATION = "operation";
    public static final String COLUMN_NCA_IDS = "nca_ids";
    public static final String COLUMN_ISSUER_SERVICE_IDS = "issuer_service_ids";
    public static final String COLUMN_USER_IDS = "user_ids";
    public static final String COLUMN_OPERATION_STATUS = "operation_status";
    public static final String COLUMN_MATCHED_COUNT = "matched_count";
    public static final String COLUMN_COMPLETED_COUNT = "completed_count";
    public static final String COLUMN_FAILED_COUNT = "failed_count";
    public static final String COLUMN_SELECTION_STATE = "selection_state";
    public static final String COLUMN_PAGING_STATE = "paging_state";
    public static final String COLUMN_REASON = "reason";
    public static final String COLUMN_CUTOFF_AT = "cutoff_at";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_UPDATED_AT = "updated_at";

    @PrimaryKeyColumn(name = COLUMN_OPERATION_ID, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private UUID operationId;

    @Column(COLUMN_ACTOR_TYPE)
    private String actorType;

    @Column(COLUMN_ACTOR_ID)
    private String actorId;

    @Column(COLUMN_OPERATION)
    private String operation;

    @Column(COLUMN_NCA_IDS)
    private @Frozen Set<String> ncaIds;

    @Column(COLUMN_ISSUER_SERVICE_IDS)
    private @Frozen Set<String> issuerServiceIds;

    @Column(COLUMN_USER_IDS)
    private @Frozen Set<String> userIds;

    @Column(COLUMN_OPERATION_STATUS)
    private KeyOperationStatus operationStatus;

    @Column(COLUMN_MATCHED_COUNT)
    private Long matchedCount;

    @Column(COLUMN_COMPLETED_COUNT)
    private Long completedCount;

    @Column(COLUMN_FAILED_COUNT)
    private Long failedCount;

    @Column(COLUMN_SELECTION_STATE)
    private String selectionState;

    @Column(COLUMN_PAGING_STATE)
    private String pagingState;

    @Column(COLUMN_REASON)
    private String reason;

    @Column(COLUMN_CUTOFF_AT)
    private Instant cutoffAt;

    @Column(COLUMN_CREATED_AT)
    private Instant createdAt;

    @Column(COLUMN_UPDATED_AT)
    private Instant updatedAt;
}
