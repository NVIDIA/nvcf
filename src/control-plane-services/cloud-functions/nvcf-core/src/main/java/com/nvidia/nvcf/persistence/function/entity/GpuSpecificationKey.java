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
package com.nvidia.nvcf.persistence.function.entity;

import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyClass;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@PrimaryKeyClass
public class GpuSpecificationKey {
    public static final String COLUMN_GPU_SPECIFICATION_ID = "gpu_specification_id";
    public static final String COLUMN_DEPLOYMENT_ID = "deployment_id";
    public static final String COLUMN_NCA_ID = "nca_id";

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_NCA_ID, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String ncaId;


    @NonNull
    @PrimaryKeyColumn(name = COLUMN_DEPLOYMENT_ID, ordinal = 1, type = PrimaryKeyType.CLUSTERED)
    private UUID deploymentId;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_GPU_SPECIFICATION_ID, ordinal = 2,
            type = PrimaryKeyType.CLUSTERED)
    private UUID gpuSpecificationId;
}
