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
package com.nvidia.icms.outbound.cassandra.request.entity;

import java.io.Serializable;
import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NonNull;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyClass;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;

@Builder(toBuilder = true)
@Data
@AllArgsConstructor
@PrimaryKeyClass
public class InstanceRequestV2ByDayKey implements Serializable {

    @NonNull
    @PrimaryKeyColumn(name = InstanceRequestV2ByDayEntity.COLUMN_TRUNCATED_TS_BY_DAY, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private Instant truncatedTsByDay;

    @NonNull
    @PrimaryKeyColumn(name = InstanceRequestV2ByDayEntity.COLUMN_REQUEST_ID, ordinal = 1, type = PrimaryKeyType.CLUSTERED)
    private String requestId;

}
