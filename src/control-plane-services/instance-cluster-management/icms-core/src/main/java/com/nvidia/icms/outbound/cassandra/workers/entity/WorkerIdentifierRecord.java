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
package com.nvidia.icms.outbound.cassandra.workers.entity;

import jakarta.annotation.Nullable;
import java.util.List;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;

/**
 * Per-instance worker-identity set stored in Cassandra.
 * Keyed by (cluster_id, instance_id); upsert replaces the whole identifier set.
 */
@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(WorkerIdentifierRecord.TABLE_NAME)
public class WorkerIdentifierRecord {

    public static final String TABLE_NAME = "worker_identifiers";
    public static final String COLUMN_CLUSTER_ID = "cluster_id";
    public static final String COLUMN_INSTANCE_ID = "instance_id";
    public static final String COLUMN_SUB = "sub";
    public static final String COLUMN_SA_UID = "sa_uid";
    public static final String COLUMN_IDENTIFIERS = "identifiers";

    @PrimaryKey
    private WorkerIdentifierKey key;

    @Column(COLUMN_SUB)
    private String sub;

    @Nullable
    @Column(COLUMN_SA_UID)
    private String saUid;

    /** Frozen list of worker_identifier UDTs. */
    @Column(COLUMN_IDENTIFIERS)
    private List<WorkerIdentifierUdt> identifiers;
}
