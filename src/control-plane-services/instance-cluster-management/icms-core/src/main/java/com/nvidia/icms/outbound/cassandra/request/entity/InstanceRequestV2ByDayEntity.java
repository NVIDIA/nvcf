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

import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;


@Builder(toBuilder = true)
@Data
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(InstanceRequestV2ByDayEntity.TABLE_NAME)

public class InstanceRequestV2ByDayEntity {

    public static final String TABLE_NAME = "requests_by_day";
    public static final String COLUMN_TRUNCATED_TS_BY_DAY = "truncated_ts_by_day";
    public static final String COLUMN_REQUEST_ID = "request_id";
    public static final String COLUMN_CREATE_TIME_UUID= "create_timeuuid";
    public static final String COLUMN_MARK_AS_DELETED= "marked_as_deleted";

    @PrimaryKey
    private InstanceRequestV2ByDayKey key;

    @Column(COLUMN_CREATE_TIME_UUID)
    private UUID createTimeuuid;

    @Column(COLUMN_MARK_AS_DELETED)
    private Boolean markedAsDeleted;

    public boolean isMarkedAsDeleted() {
        return getMarkedAsDeleted() != null && getMarkedAsDeleted();
    }
}

