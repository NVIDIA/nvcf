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

import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import java.time.Instant;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import org.springframework.data.annotation.PersistenceCreator;
import org.springframework.data.cassandra.core.cql.PrimaryKeyType;
import org.springframework.data.cassandra.core.mapping.Column;
import org.springframework.data.cassandra.core.mapping.PrimaryKeyColumn;
import org.springframework.data.cassandra.core.mapping.Table;

/**
 * Owner status within one account. Applies to keys from every issuer service.
 */
@Builder(toBuilder = true)
@Data
@NoArgsConstructor
@AllArgsConstructor(onConstructor_ = @PersistenceCreator)
@Table(OwnerStatusByAccountModel.TABLE_NAME)
public class OwnerStatusByAccountModel {

    public static final String TABLE_NAME = "owner_status_by_account";
    public static final String COLUMN_NCA_ID = "nca_id";
    public static final String COLUMN_OWNER_TYPE = "owner_type";
    public static final String COLUMN_OWNER_ID = "owner_id";
    public static final String COLUMN_OWNER_STATUS = "owner_status";
    public static final String COLUMN_CREATED_AT = "created_at";
    public static final String COLUMN_UPDATED_AT = "updated_at";

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_NCA_ID, ordinal = 0, type = PrimaryKeyType.PARTITIONED)
    private String ncaId;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_OWNER_TYPE, ordinal = 1, type = PrimaryKeyType.PARTITIONED)
    private KeyOwnerType ownerType;

    @NonNull
    @PrimaryKeyColumn(name = COLUMN_OWNER_ID, ordinal = 2, type = PrimaryKeyType.PARTITIONED)
    private String ownerId;

    @Column(COLUMN_OWNER_STATUS)
    private KeyOwnerStatus ownerStatus;

    @Column(COLUMN_CREATED_AT)
    @Builder.Default
    private Instant createdAt = Instant.now();

    @Column(COLUMN_UPDATED_AT)
    @Builder.Default
    private Instant updatedAt = Instant.now();
}
