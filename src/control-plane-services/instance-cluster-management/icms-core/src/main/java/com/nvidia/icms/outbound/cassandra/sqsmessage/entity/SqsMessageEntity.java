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
package com.nvidia.icms.outbound.cassandra.sqsmessage.entity;

import com.nvidia.icms.inbound.rest.model.SpotRequestStatusCode;
import com.nvidia.icms.outbound.sqs.model.CapacityType;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.UUID;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKey;
import org.springframework.data.cassandra.core.mapping.Table;

@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(SqsMessageEntity.TABLE_NAME)
public class SqsMessageEntity {

    public static final String TABLE_NAME = "sqs_message_by_request_and_batch_id";
    public static final String COLUMN_REQUEST_ID = "request_id";
    public static final String COLUMN_ZONE = "zone";
    public static final String COLUMN_STATUS = "status";
    public static final String COLUMN_MESSAGE_BATCH_ID = "message_batch_id";
    public static final String COLUMN_ACKNOWLEDGED_INSTANCES = "acknowledged_instances";
    public static final String COLUMN_CREATION_TIME = "creation_time";
    public static final String COLUMN_CLOUD_PROVIDER = "cloud_provider";
    public static final String COLUMN_RESERVATION_ID = "reservation_id";
    public static final String COLUMN_CAPACITY_TYPE = "capacity_type";

    @PrimaryKey
    @NotNull
    private SqsMessageKey key;

    @Column(COLUMN_ACKNOWLEDGED_INSTANCES)
    private Integer acknowledgedInstances;

    @Column(COLUMN_CREATION_TIME)
    private Instant creationTime;

    @Column(COLUMN_ZONE)
    private String zone;

    /*
     Expected values: pending-fulfillment, cannot-fulfill
     */
    @Column(COLUMN_STATUS)
    private SpotRequestStatusCode status;

    @Column(COLUMN_CLOUD_PROVIDER)
    private String cloudProvider;

    @Nullable
    @Column(COLUMN_RESERVATION_ID)
    private UUID reservationId;

    @Nullable
    @Column(COLUMN_CAPACITY_TYPE)
    private CapacityType capacityType;
}
