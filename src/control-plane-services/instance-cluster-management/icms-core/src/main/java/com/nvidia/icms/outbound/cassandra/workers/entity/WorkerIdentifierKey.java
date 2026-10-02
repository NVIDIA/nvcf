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

import java.io.Serializable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyClass;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;

/** Composite primary key for {@link WorkerIdentifierRecord}: (cluster_id, instance_id). */
@Builder
@Data
@AllArgsConstructor
@NoArgsConstructor
@PrimaryKeyClass
public class WorkerIdentifierKey implements Serializable {

    @NonNull
    @PrimaryKeyColumn(
            name = WorkerIdentifierRecord.COLUMN_CLUSTER_ID,
            ordinal = 0,
            type = PrimaryKeyType.PARTITIONED)
    private String clusterId;

    @NonNull
    @PrimaryKeyColumn(
            name = WorkerIdentifierRecord.COLUMN_INSTANCE_ID,
            ordinal = 1,
            type = PrimaryKeyType.CLUSTERED)
    private String instanceId;
}
