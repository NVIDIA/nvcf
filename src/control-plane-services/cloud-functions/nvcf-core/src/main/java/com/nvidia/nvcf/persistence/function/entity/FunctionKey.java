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

import static com.nvidia.nvcf.persistence.function.entity.FunctionEntity.COLUMN_FUNCTION_ID;
import static com.nvidia.nvcf.persistence.function.entity.FunctionEntity.COLUMN_FUNCTION_VERSION_ID;

import java.io.Serializable;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyClass;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor
@PrimaryKeyClass
public class FunctionKey implements Serializable {

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_FUNCTION_ID, type = PrimaryKeyType.PARTITIONED)
    private UUID functionId;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_FUNCTION_VERSION_ID, ordinal = 1, type = PrimaryKeyType.CLUSTERED)
    private UUID functionVersionId;

}
